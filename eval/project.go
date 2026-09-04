package eval

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultProjectRoot = "~/src"

var projectRootEnv *Environment

// ProjectRoot returns the projects root: the project-root binding when it names
// a non-empty string (config-as-data, like default-graph — e.g. (define
// project-root "~/code") in the prelude), else ~/src.
func ProjectRoot() string {
	if projectRootEnv != nil {
		if v, err := projectRootEnv.Lookup("project-root"); err == nil {
			if s, ok := v.(String); ok && s != "" {
				return expandHome(string(s))
			}
		}
	}
	return expandHome(defaultProjectRoot)
}

// FindProject reports the project containing dir.
func FindProject(dir string) (name, hub string, ok bool) {
	if rp, ok := registeredProjectFor(dir); ok {
		return rp.Name, rp.Path, true
	}
	src := ProjectRoot()
	for d := dir; ; d = filepath.Dir(d) {
		if filepath.Dir(d) == src {
			return filepath.Base(d), d, true
		}
		if info, err := os.Stat(filepath.Join(d, ".bare")); err == nil && info.IsDir() {
			return filepath.Base(d), d, true
		}
		if d == filepath.Dir(d) {
			return "", "", false
		}
	}
}

func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

type worktreeInfo struct {
	path   string
	branch string
	head   string
	bare   bool
}

func parseWorktrees(out string) []worktreeInfo {
	var trees []worktreeInfo
	var cur worktreeInfo
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			if cur.path != "" {
				trees = append(trees, cur)
			}
			cur = worktreeInfo{path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "HEAD "):
			cur.head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "bare":
			cur.bare = true
		case line == "detached":
			cur.branch = "(detached)"
		}
	}
	if cur.path != "" {
		trees = append(trees, cur)
	}
	return trees
}

func treeContext() (gitDir, treesRoot, home string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", "", err
	}
	// A registered directory project: trees go to <path>/.trees/<branch>.
	if rp, ok := registeredProjectFor(cwd); ok && rp.Kind != "hub" {
		top, err := gitRun(cwd, "rev-parse", "--show-toplevel")
		if err != nil {
			return "", "", "", err
		}
		return top, filepath.Join(rp.Path, ".trees"), rp.Path, nil
	}
	var hub string
	if _, h, ok := FindProject(cwd); ok {
		hub = h
		if _, err := os.Stat(filepath.Join(h, ".git")); err == nil {
			return h, h, h, nil
		}
	}
	if top, err := gitRun(cwd, "rev-parse", "--show-toplevel"); err == nil {
		if hub != "" {
			return top, hub, hub, nil
		}
		return top, filepath.Join(top, ".trees"), top, nil
	}
	if hub != "" {
		entries, _ := os.ReadDir(hub)
		for _, e := range entries {
			if e.IsDir() {
				if _, err := os.Stat(filepath.Join(hub, e.Name(), ".git")); err == nil {
					return filepath.Join(hub, e.Name()), hub, hub, nil
				}
			}
		}
	}
	return "", "", "", errors.New("not inside a project or git repository")
}

func ensureTreesExcluded(gitDir string) {
	common, err := gitRun(gitDir, "rev-parse", "--git-common-dir")
	if err != nil {
		return
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	path := filepath.Join(common, "info", "exclude")
	data, _ := os.ReadFile(path)
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == ".trees/" {
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(".trees/\n")
}

func listTrees() ([]worktreeInfo, string, error) {
	gitDir, _, home, err := treeContext()
	if err != nil {
		return nil, "", err
	}
	out, err := gitRun(gitDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, "", err
	}
	var trees []worktreeInfo
	for _, wt := range parseWorktrees(out) {
		if !wt.bare {
			trees = append(trees, wt)
		}
	}
	return trees, home, nil
}

func findTree(trees []worktreeInfo, name string) (worktreeInfo, bool) {
	for _, wt := range trees {
		if filepath.Base(wt.path) == name || wt.branch == name {
			return wt, true
		}
	}
	return worktreeInfo{}, false
}

// ProjectNames returns the completion source for `project <tab>`: the
// registered project names, sorted. Like the projects listing, nothing is
// enumerated from disk — an empty registry completes nothing.
func ProjectNames() []string {
	var names []string
	for _, rp := range registeredProjects() {
		names = append(names, rp.Name)
	}
	sort.Strings(names)
	return names
}

// TreeNames returns the current project's worktree names — directory base
// names plus branch names where they differ, since findTree matches either —
// sorted.
func TreeNames() []string {
	trees, _, err := listTrees()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, wt := range trees {
		seen[filepath.Base(wt.path)] = true
		if wt.branch != "" && wt.branch != "(detached)" {
			seen[wt.branch] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func createTree(name string) (Value, error) {
	gitDir, treesRoot, _, err := treeContext()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(treesRoot, name)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("tree %q already exists at %s (switch with (tree %q))", name, path, name)
	}
	if err := os.MkdirAll(treesRoot, 0755); err != nil {
		return nil, err
	}
	if filepath.Base(treesRoot) == ".trees" {
		ensureTreesExcluded(gitDir)
	}
	branchExists := func() bool {
		_, err := gitRun(gitDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
		return err == nil
	}
	headUnborn := func() bool {
		_, err := gitRun(gitDir, "rev-parse", "--verify", "--quiet", "HEAD")
		return err != nil
	}
	var args []string
	switch {
	case branchExists():
		args = []string{"worktree", "add", path, name}
	case headUnborn():
		args = []string{"worktree", "add", "--orphan", "-b", name, path}
	default:
		args = []string{"worktree", "add", "-b", name, path}
	}
	if _, err := gitRun(gitDir, args...); err != nil {
		return nil, err
	}
	if err := os.Chdir(path); err != nil {
		return nil, err
	}
	return String(path), nil
}

func hubGitFile(hub string) error {
	return os.WriteFile(filepath.Join(hub, ".git"), []byte("gitdir: ./.bare\n"), 0644)
}

func projectBuiltins(env *Environment, approval *approvalGate) {
	env.Set("project-root", String(defaultProjectRoot))
	projectRootEnv = env
	projectRegMu.Lock()
	projectRegStore = nil
	projectRegAt = time.Time{}
	projectRegMu.Unlock()

	oneName := func(who string, args []Value) (string, error) {
		if len(args) != 1 {
			return "", fmt.Errorf("%s expects 1 argument: (%s \"name\")", who, who)
		}
		s, ok := args[0].(String)
		if !ok {
			return "", fmt.Errorf("%s expects a string name", who)
		}
		return string(s), nil
	}

	countTrees := func(path, kind string) int {
		dir := path
		if kind != "hub" {
			dir = filepath.Join(path, ".trees")
		}
		trees := 0
		if subs, err := os.ReadDir(dir); err == nil {
			for _, s := range subs {
				if s.IsDir() && !strings.HasPrefix(s.Name(), ".") {
					trees++
				}
			}
		}
		return trees
	}

	Register("projects", "list registered projects as rows", CommandMeta{Command: true})
	env.Set("projects", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("projects expects no arguments")
		}
		counts := map[uint32]int{}
		if cg := registryGraph(); cg != nil {
			counts, _ = cg.OpenTaskCounts()
		}
		result := []Value{}
		for _, rp := range registeredProjects() {
			result = append(result, Dictionary{
				"name":  String(rp.Name),
				"path":  String(rp.Path),
				"trees": Integer(countTrees(rp.Path, rp.Kind)),
				"tasks": Integer(counts[rp.NodeID]),
			})
		}
		sort.Slice(result, func(i, j int) bool {
			return result[i].(Dictionary)["name"].(String) < result[j].(Dictionary)["name"].(String)
		})
		return result, nil
	}))

	Register("project", "cd into a project's worktree directory", CommandMeta{Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name"})
	env.Set("project", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		name, err := oneName("project", args)
		if err != nil {
			return nil, err
		}
		// Registered lookup first, then the legacy project-root scan.
		var hub string
		if rp, ok := registeredProjectByName(name); ok {
			if rp.Kind != "hub" {
				// A directory project is its own working directory.
				if err := os.Chdir(rp.Path); err != nil {
					return nil, err
				}
				return String(rp.Path), nil
			}
			hub = rp.Path
		} else {
			hub = filepath.Join(ProjectRoot(), name)
		}
		if info, err := os.Stat(hub); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("no project %q under %s (create it with (create-project %q))", name, ProjectRoot(), name)
		}
		target := hub
		candidates := []string{"main"}
		if def, err := gitRun(hub, "symbolic-ref", "--short", "HEAD"); err == nil {
			candidates = append(candidates, def)
		}
		for _, c := range candidates {
			if info, err := os.Stat(filepath.Join(hub, c)); err == nil && info.IsDir() {
				target = filepath.Join(hub, c)
				break
			}
		}
		if target == hub {
			if subs, err := os.ReadDir(hub); err == nil {
				for _, s := range subs {
					if s.IsDir() && !strings.HasPrefix(s.Name(), ".") {
						target = filepath.Join(hub, s.Name())
						break
					}
				}
			}
		}
		if err := os.Chdir(target); err != nil {
			return nil, err
		}
		return String(target), nil
	}))

	Register("create-project", "create a bare-repo project hub under the project root and cd in", CommandMeta{Command: true, MinArgs: 1, MaxArgs: 2, Usage: "name [clone-url]"})
	env.Set("create-project", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("create-project expects 1 or 2 arguments: (create-project \"name\" [\"clone-url\"])")
		}
		name, ok := args[0].(String)
		if !ok {
			return nil, errors.New("create-project expects a string name")
		}
		url := ""
		if len(args) == 2 {
			u, ok := args[1].(String)
			if !ok {
				return nil, errors.New("create-project expects a string clone url")
			}
			url = string(u)
		}
		hub := filepath.Join(ProjectRoot(), string(name))
		if _, err := os.Stat(hub); err == nil {
			return nil, fmt.Errorf("project %q already exists at %s", string(name), hub)
		}
		if err := os.MkdirAll(hub, 0755); err != nil {
			return nil, err
		}
		bare := filepath.Join(hub, ".bare")
		branch := "main"
		if url == "" {
			if _, err := gitRun(hub, "init", "--bare", "-b", "main", bare); err != nil {
				return nil, err
			}
		} else {
			if _, err := gitRun(hub, "clone", "--bare", url, bare); err != nil {
				return nil, err
			}
			if _, err := gitRun(bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
				return nil, err
			}
			if def, err := gitRun(bare, "symbolic-ref", "--short", "HEAD"); err == nil && def != "" {
				branch = def
			}
			if _, err := gitRun(bare, "fetch", "origin"); err != nil {
				return nil, err
			}
		}
		if err := hubGitFile(hub); err != nil {
			return nil, err
		}
		path := filepath.Join(hub, branch)
		wtArgs := []string{"worktree", "add", path, branch}
		if url == "" {
			wtArgs = []string{"worktree", "add", "--orphan", "-b", branch, path}
		}
		if _, err := gitRun(hub, wtArgs...); err != nil {
			return nil, err
		}
		if err := os.Chdir(path); err != nil {
			return nil, err
		}
		if cg, err := registryGraphWrite(); err == nil {
			_, _ = registerProjectNode(cg, string(name), hub, "hub")
		}
		return String(path), nil
	}))

	Register("trees", "list the current project's worktrees as rows", CommandMeta{Command: true})
	env.Set("trees", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("trees expects no arguments")
		}
		trees, _, err := listTrees()
		if err != nil {
			return nil, err
		}
		result := make([]Value, 0, len(trees))
		for _, wt := range trees {
			head := wt.head
			if len(head) > 7 {
				head = head[:7]
			}
			result = append(result, Dictionary{
				"name":   String(filepath.Base(wt.path)),
				"branch": String(wt.branch),
				"head":   String(head),
				"path":   String(wt.path),
			})
		}
		return result, nil
	}))

	Register("create-tree", "add a branch worktree to the current project", CommandMeta{Command: true, MinArgs: 1, MaxArgs: 1, Usage: "branch"})
	env.Set("create-tree", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		name, err := oneName("create-tree", args)
		if err != nil {
			return nil, err
		}
		return createTree(name)
	}))

	Register("tree", "cd into a project worktree, creating it if missing", CommandMeta{Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name"})
	env.Set("tree", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		name, err := oneName("tree", args)
		if err != nil {
			return nil, err
		}
		trees, _, err := listTrees()
		if err != nil {
			return nil, err
		}
		if wt, ok := findTree(trees, name); ok {
			if err := os.Chdir(wt.path); err != nil {
				return nil, err
			}
			return String(wt.path), nil
		}
		return createTree(name)
	}))

	Register("delete-tree", "remove a project worktree", CommandMeta{
		Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name",
		Options: []Option{{Long: "force", Short: "f", Kind: OptionBool, Doc: "discard uncommitted changes"}}})
	env.Set("delete-tree", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("delete-tree", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, errors.New("delete-tree expects a name: (delete-tree \"name\" [{:force #t}])")
		}
		s, ok := pos[0].(String)
		if !ok {
			return nil, errors.New("delete-tree expects a string name")
		}
		name := string(s)
		force := OptBool(opts, "force")
		trees, hub, err := listTrees()
		if err != nil {
			return nil, err
		}
		wt, ok := findTree(trees, name)
		if !ok {
			return nil, fmt.Errorf("no tree %q in this project", name)
		}
		if err := approval.requireWrite(fmt.Sprintf("delete-tree %q", wt.path), wt.path); err != nil {
			return nil, err
		}
		// Run the removal from a directory that survives it.
		runDir := hub
		for _, other := range trees {
			if other.path != wt.path {
				runDir = other.path
				break
			}
		}
		rmArgs := []string{"worktree", "remove"}
		if force {
			rmArgs = append(rmArgs, "--force")
		}
		rmArgs = append(rmArgs, wt.path)
		if _, err := gitRun(runDir, rmArgs...); err != nil {
			return nil, err
		}
		// If we were standing in the removed tree, land in the hub.
		if cwd, err := os.Getwd(); err != nil || cwd == wt.path || strings.HasPrefix(cwd, wt.path+string(filepath.Separator)) {
			_ = os.Chdir(hub)
		}
		return true, nil
	}))
}
