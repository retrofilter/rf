package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
)

// RegisteredProject is one registry entry, decoded from a project node's
// properties.
type RegisteredProject struct {
	NodeID uint32
	Name   string
	Path   string // "" for a project with no backing directory
	Kind   string // "hub" (trees are siblings of .bare) or "dir" (trees in .trees/)
	Text   string // markdown notes, edited with project -e
}

const projectRegistryTTL = 5 * time.Second

var (
	projectRegMu    sync.Mutex
	projectRegStore *core.GraphStore
	projectRegCache []RegisteredProject
	projectRegAt    time.Time
)

func registryGraphName() string {
	if projectRootEnv != nil {
		if v, err := projectRootEnv.Lookup("default-graph"); err == nil {
			if s, ok := asString(v); ok && s != "" {
				return s
			}
		}
	}
	return defaultGraphName
}

func registryGraph() *core.Graph {
	projectRegMu.Lock()
	gs := projectRegStore
	projectRegMu.Unlock()
	if gs == nil {
		return nil
	}
	cg, err := gs.GetGraph(registryGraphName())
	if err != nil {
		return nil
	}
	return cg
}

func registryGraphWrite() (*core.Graph, error) {
	projectRegMu.Lock()
	gs := projectRegStore
	projectRegMu.Unlock()
	if gs == nil {
		return nil, errors.New("no database available")
	}
	name := registryGraphName()
	cg, err := gs.GetGraph(name)
	if err == nil {
		return cg, nil
	}
	if _, err := gs.CreateGraph(name); err != nil {
		return nil, fmt.Errorf("failed to create default graph %q: %w", name, err)
	}
	return gs.GetGraph(name)
}

func registeredProjects() []RegisteredProject {
	projectRegMu.Lock()
	if time.Since(projectRegAt) < projectRegistryTTL {
		defer projectRegMu.Unlock()
		return projectRegCache
	}
	projectRegMu.Unlock()

	var entries []RegisteredProject
	if cg := registryGraph(); cg != nil {
		if nodes, err := cg.NodesByType("project", 0); err == nil {
			for _, node := range nodes {
				props := node.FormattedProperties()
				name, _ := props["name"].(string)
				path, _ := props["path"].(string)
				kind, _ := props["kind"].(string)
				text, _ := props["text"].(string)
				if name == "" {
					continue
				}
				entries = append(entries, RegisteredProject{NodeID: node.ID, Name: name, Path: path, Kind: kind, Text: text})
			}
		}
	}

	projectRegMu.Lock()
	defer projectRegMu.Unlock()
	projectRegCache = entries
	projectRegAt = time.Now()
	return entries
}

func invalidateProjectRegistry() {
	projectRegMu.Lock()
	projectRegAt = time.Time{}
	projectRegMu.Unlock()
}

func registeredProjectFor(dir string) (RegisteredProject, bool) {
	var best RegisteredProject
	found := false
	for _, rp := range registeredProjects() {
		if rp.Path == "" || (dir != rp.Path && !strings.HasPrefix(dir, rp.Path+string(filepath.Separator))) {
			continue
		}
		if !found || len(rp.Path) > len(best.Path) {
			best, found = rp, true
		}
	}
	return best, found
}

func registeredProjectByName(name string) (RegisteredProject, bool) {
	for _, rp := range registeredProjects() {
		if rp.Name == name {
			return rp, true
		}
	}
	return RegisteredProject{}, false
}

func registeredProjectByNodeID(id uint32) (RegisteredProject, bool) {
	for _, rp := range registeredProjects() {
		if rp.NodeID == id {
			return rp, true
		}
	}
	return RegisteredProject{}, false
}

func projectNodesByAttrs(cg *core.Graph, attrs map[string]interface{}) ([]*models.Node, error) {
	attrs["type"] = "project"
	return cg.GetNodesByAttributes(context.Background(), attrs)
}

func detectProjectKind(path string) string {
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		if info, err := os.Stat(filepath.Join(path, ".bare")); err == nil && info.IsDir() {
			return "hub"
		}
		return "dir"
	}
	return "hub"
}

func registerProjectNode(cg *core.Graph, name, path, kind string) (uint32, error) {
	if existing, err := projectNodesByAttrs(cg, map[string]interface{}{"name": name}); err != nil {
		return 0, err
	} else if len(existing) > 0 {
		return 0, fmt.Errorf("project %q is already registered", name)
	}
	props := map[string]interface{}{"type": "project", "name": name}
	if path != "" {
		if existing, err := projectNodesByAttrs(cg, map[string]interface{}{"path": path}); err != nil {
			return 0, err
		} else if len(existing) > 0 {
			return 0, fmt.Errorf("%s is already registered as a project", path)
		}
		props["path"] = path
		props["kind"] = kind
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return 0, err
	}
	nodeType := "project"
	id, err := cg.InsertNode(context.Background(), &models.Node{
		GraphID:    cg.ID,
		Type:       &nodeType,
		Properties: (*json.RawMessage)(&propsJSON),
	})
	if err != nil {
		return 0, err
	}
	invalidateProjectRegistry()
	return id, nil
}

func canonicalProjectPath(path string) (string, error) {
	abs, err := filepath.Abs(expandHome(path))
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}

func registryBuiltins(env *Environment, ev *Evaluator, gs *core.GraphStore) {
	projectRegMu.Lock()
	projectRegStore = gs
	projectRegAt = time.Time{}
	projectRegMu.Unlock()

	Register("register-project", "register a directory (cwd by default) as a project (user-only)", CommandMeta{
		Command: true, MaxArgs: 1, Usage: "[path]",
		Options: []Option{{Long: "name", Short: "n", Kind: OptionString, Placeholder: "NAME", Doc: "project name (default: the directory's base name)"}}})
	env.Set("register-project", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if err := ev.RequireUser("register-project"); err != nil {
			return nil, err
		}
		pos, opts, err := ParseOptions("register-project", args)
		if err != nil {
			return nil, err
		}
		path := ""
		if len(pos) > 1 {
			return nil, errors.New("register-project expects at most one path: (register-project [path] [{:name NAME}])")
		}
		if len(pos) == 1 {
			s, ok := pos[0].(String)
			if !ok {
				return nil, errors.New("register-project expects a string path")
			}
			path = string(s)
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			path = cwd
		}
		path, err = canonicalProjectPath(path)
		if err != nil {
			return nil, err
		}
		name := OptString(opts, "name", filepath.Base(path))
		cg, err := registryGraphWrite()
		if err != nil {
			return nil, err
		}
		id, err := registerProjectNode(cg, name, path, detectProjectKind(path))
		if err != nil {
			return nil, err
		}
		return Integer(id), nil
	}))

	Register("unregister-project", "remove a project from the registry (user-only)", CommandMeta{
		Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name",
		Options: []Option{{Long: "tasks", Kind: OptionBool, Doc: "delete the project's tasks too"}}})
	env.Set("unregister-project", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if err := ev.RequireUser("unregister-project"); err != nil {
			return nil, err
		}
		pos, opts, err := ParseOptions("unregister-project", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, errors.New("unregister-project expects a name: (unregister-project \"name\" [{:tasks #t}])")
		}
		s, ok := pos[0].(String)
		if !ok {
			return nil, errors.New("unregister-project expects a string name")
		}
		cg, err := registryGraphWrite()
		if err != nil {
			return nil, err
		}
		nodes, err := projectNodesByAttrs(cg, map[string]interface{}{"name": string(s)})
		if err != nil {
			return nil, err
		}
		if len(nodes) == 0 {
			return nil, fmt.Errorf("project %q is not registered", string(s))
		}
		ctx := context.Background()
		for _, project := range nodes {
			if OptBool(opts, "tasks") {
				tasks, err := cg.GetIncomingNeighbors(ctx, project.ID)
				if err != nil {
					return nil, err
				}
				for _, task := range tasks {
					if task.Type != nil && *task.Type == "task" {
						if err := cg.DeleteNode(ctx, task.ID); err != nil {
							return nil, err
						}
					}
				}
			}
			if err := cg.DeleteNode(ctx, project.ID); err != nil {
				return nil, err
			}
		}
		invalidateProjectRegistry()
		return true, nil
	}))
}

// DefaultGraph resolves the default graph (the default-graph binding, else
// the stock name) for callers outside eval that write registry-style nodes.
// Created lazily on first use.
func DefaultGraph() (*core.Graph, error) {
	return registryGraphWrite()
}
