package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
)

const (
	taskStatusOpen = "open"
	taskStatusDone = "done"
)

func taskProjectNode(cg *core.Graph, name, path string) (uint32, error) {
	if path != "" {
		nodes, err := projectNodesByAttrs(cg, map[string]interface{}{"path": path})
		if err != nil {
			return 0, err
		}
		if len(nodes) > 0 {
			return nodes[0].ID, nil
		}
	}
	nodes, err := projectNodesByAttrs(cg, map[string]interface{}{"name": name})
	if err != nil {
		return 0, err
	}
	if len(nodes) > 0 {
		return nodes[0].ID, nil
	}
	if path == "" {
		return 0, fmt.Errorf("no registered project %q (register it with (register-project path {:name %q}))", name, name)
	}
	return registerProjectNode(cg, name, path, detectProjectKind(path))
}

func taskByID(cg *core.Graph, id uint32) (*models.Node, error) {
	node, err := cg.GetNode(context.Background(), id)
	if err != nil {
		return nil, fmt.Errorf("no task %d", id)
	}
	if node.Type == nil || *node.Type != "task" {
		return nil, fmt.Errorf("node %d is not a task", id)
	}
	return node, nil
}

func closeTask(cg *core.Graph, id uint32) error {
	node, err := taskByID(cg, id)
	if err != nil {
		return err
	}
	props := node.FormattedProperties()
	props["status"] = taskStatusDone
	props["closed-at"] = time.Now().Format(time.RFC3339)
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return err
	}
	node.Properties = (*json.RawMessage)(&propsJSON)
	_, err = cg.UpdateNode(context.Background(), node)
	return err
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func taskBuiltins(env *Environment, ev *Evaluator) {
	Register("task", "file a task for the current project", CommandMeta{
		Command: true, MinArgs: 0, MaxArgs: -1, Usage: "text ...",
		Options: []Option{
			{Long: "project", Short: "p", Kind: OptionString, Placeholder: "NAME", Doc: "file under the named project instead of the cwd's"},
			{Long: "blocks", Kind: OptionInt, Placeholder: "ID", Doc: "the new task blocks task ID"},
			{Long: "complete", Short: "c", Kind: OptionInt, Placeholder: "ID", Doc: "close task ID instead of filing one"},
			{Long: "delete", Short: "d", Kind: OptionInt, Placeholder: "ID", Doc: "delete task ID outright (permanent; --complete closes instead)"},
		}})
	env.Set("task", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("task", args)
		if err != nil {
			return nil, err
		}
		if OptInt(opts, "complete", 0) != 0 && OptInt(opts, "delete", 0) != 0 {
			return nil, errors.New("task --delete and --complete are exclusive")
		}
		if id := OptInt(opts, "complete", 0); id != 0 {
			if len(pos) != 0 {
				return nil, errors.New("task --complete closes an existing task and takes no text")
			}
			cg, err := registryGraphWrite()
			if err != nil {
				return nil, err
			}
			if err := closeTask(cg, uint32(id)); err != nil {
				return nil, err
			}
			return true, nil
		}
		if id := OptInt(opts, "delete", 0); id != 0 {
			if len(pos) != 0 {
				return nil, errors.New("task --delete removes an existing task and takes no text")
			}
			cg, err := registryGraphWrite()
			if err != nil {
				return nil, err
			}
			if err := deleteTask(cg, uint32(id)); err != nil {
				return nil, err
			}
			return true, nil
		}
		if len(pos) == 0 {
			return nil, errors.New("task expects text: (task \"text\" [{:project NAME :blocks ID}]) — or (task {:complete ID}) to close one, (task {:delete ID}) to remove one")
		}
		parts := make([]string, len(pos))
		for i, arg := range pos {
			s, ok := asString(arg)
			if !ok {
				return nil, errors.New("task expects strings")
			}
			parts[i] = s
		}
		cg, err := registryGraphWrite()
		if err != nil {
			return nil, err
		}

		var projectID uint32
		if name := OptString(opts, "project", ""); name != "" {
			projectID, err = taskProjectNode(cg, name, "")
			if err != nil {
				return nil, err
			}
		} else if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			if name, hub, ok := FindProject(cwd); ok {
				projectID, err = taskProjectNode(cg, name, hub)
				if err != nil {
					return nil, err
				}
			}
		}

		props := map[string]interface{}{
			"type":   "task",
			"text":   strings.Join(parts, " "),
			"status": taskStatusOpen,
		}
		if ev.sessionID != "" {
			props["session"] = ev.sessionID
		}
		propsJSON, err := json.Marshal(props)
		if err != nil {
			return nil, err
		}
		nodeType := "task"
		id, err := cg.InsertNode(context.Background(), &models.Node{
			GraphID:    cg.ID,
			Type:       &nodeType,
			Properties: (*json.RawMessage)(&propsJSON),
		})
		if err != nil {
			return nil, fmt.Errorf("task failed: %w", err)
		}
		edge := func(edgeType string, source, target uint32) error {
			propsJSON, err := json.Marshal(map[string]interface{}{"type": edgeType})
			if err != nil {
				return err
			}
			_, err = cg.InsertEdge(context.Background(), &models.Edge{
				GraphID:    cg.ID,
				Source:     source,
				Target:     target,
				Type:       &edgeType,
				Properties: (*json.RawMessage)(&propsJSON),
			})
			return err
		}
		if projectID != 0 {
			if err := edge("for", id, projectID); err != nil {
				return nil, err
			}
		}
		if blocks := OptInt(opts, "blocks", 0); blocks != 0 {
			if _, err := taskByID(cg, uint32(blocks)); err != nil {
				return nil, err
			}
			if err := edge("blocks", id, uint32(blocks)); err != nil {
				return nil, err
			}
		}
		if _, err := syncLinks(cg, id, props["text"].(string)); err != nil {
			return nil, err
		}
		return Integer(id), nil
	}))

	Register("tasks", "list open tasks, by default scoped to the current project", CommandMeta{
		Command: true,
		Options: []Option{
			{Long: "all", Short: "a", Kind: OptionBool, Doc: "every project (adds a project column)"},
			{Long: "project", Short: "p", Kind: OptionString, Placeholder: "NAME", Doc: "the named project's tasks"},
			{Long: "done", Kind: OptionBool, Doc: "closed tasks instead of open"},
			{Long: "ready", Kind: OptionBool, Doc: "only tasks with no open blocker"},
			{Long: "limit", Short: "n", Kind: OptionInt, Placeholder: "N", Doc: "newest N tasks"},
		}})
	env.Set("tasks", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("tasks", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 0 {
			return nil, errors.New("tasks expects no arguments: (tasks [{:all :project NAME :done :ready :limit N}])")
		}
		cg := registryGraph()
		if cg == nil {
			return []Value{}, nil
		}
		q := models.TaskQuery{Status: taskStatusOpen, Limit: OptInt(opts, "limit", 0), Ready: OptBool(opts, "ready")}
		if OptBool(opts, "done") {
			q.Status = taskStatusDone
		}
		all := OptBool(opts, "all")
		if name := OptString(opts, "project", ""); name != "" {
			q.Project = name
		} else if !all {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			if name, _, ok := FindProject(cwd); ok {
				q.Project = name
			} else {
				all = true
			}
		}
		rows, err := cg.SearchTasks(q)
		if err != nil {
			return nil, err
		}
		result := make([]Value, 0, len(rows))
		for _, row := range rows {
			props := row.FormattedProperties()
			text, _ := props["text"].(string)
			status, _ := props["status"].(string)
			age := time.Since(row.CreatedAt)
			dict := Dictionary{
				"id":        Integer(row.ID),
				"status":    String(status),
				"age":       String(humanAge(age)),
				"text":      String(text),
				"_age-days": Number(age.Hours() / 24),
			}
			if all {
				dict["project"] = String(row.Project)
			}
			result = append(result, dict)
		}
		return result, nil
	}))

	Register("delete-task", "delete tasks by id — permanent; task -c closes instead", CommandMeta{Command: true, MinArgs: 1, MaxArgs: -1, Usage: "id ..."})
	env.Set("delete-task", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("delete-task expects task ids: (delete-task id ...)")
		}
		cg, err := registryGraphWrite()
		if err != nil {
			return nil, err
		}
		for _, arg := range args {
			id, ok := optionInt(arg)
			if !ok {
				return nil, errors.New("delete-task expects numeric task ids")
			}
			if err := deleteTask(cg, uint32(id)); err != nil {
				return nil, err
			}
		}
		return true, nil
	}))
}

func deleteTask(cg *core.Graph, id uint32) error {
	if _, err := taskByID(cg, id); err != nil {
		return err
	}
	return cg.DeleteNode(context.Background(), id)
}
