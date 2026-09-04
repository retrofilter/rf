package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/logger"
	"github.com/retrofilter/rf/models"
)

func asString(val Value) (string, bool) {
	switch v := val.(type) {
	case String:
		return string(v), true
	default:
		return "", false
	}
}

func asMap(val Value) (map[string]interface{}, bool) {
	switch dict := val.(type) {
	case map[string]interface{}:
		return dict, true
	case Dictionary:
		m := make(map[string]interface{}, len(dict))
		for k, v := range dict {
			m[k] = v
		}
		return m, true
	default:
		return nil, false
	}
}

func asNumber(val Value) (uint32, bool) {
	if f, ok := numFloat(val); ok {
		return uint32(f), true
	}
	return 0, false
}

const defaultGraphName = "knowledge-base"

func resolveGraphArg(arg Value, env *Environment, gs *core.GraphStore) (*core.Graph, error) {
	syncEmbeddings(env, gs)
	if name, ok := asString(arg); ok {
		cg, err := gs.GetGraph(name)
		if err != nil {
			return nil, fmt.Errorf("graph not found: %s", name)
		}
		return cg, nil
	}
	if val, err := env.Lookup("_current_graph"); err == nil {
		if name, ok := asString(val); ok {
			cg, err := gs.GetGraph(name)
			if err != nil {
				return nil, fmt.Errorf("graph not found: %s", name)
			}
			return cg, nil
		}
	}
	name := defaultGraphName
	if val, err := env.Lookup("default-graph"); err == nil {
		if s, ok := asString(val); ok && s != "" {
			name = s
		}
	}
	cg, err := gs.GetGraph(name)
	if err == nil {
		return cg, nil
	}
	if _, err := gs.CreateGraph(name); err != nil {
		return nil, fmt.Errorf("failed to create default graph %q: %w", name, err)
	}
	return gs.GetGraph(name)
}

func graphBuiltins(env *Environment, ev *Evaluator, gs *core.GraphStore) {
	Register("create-graph", "create a new named graph", CommandMeta{
		Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name"})
	env.Set("create-graph", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("create-graph expects a graph name: (create-graph \"name\")")
		}
		name, ok := asString(args[0])
		if !ok {
			return nil, errors.New("create-graph expects a graph name")
		}
		_, err := gs.CreateGraph(name)
		if err != nil {
			return nil, fmt.Errorf("failed to create graph: %w", err)
		}
		return Symbol("ok"), nil
	}))

	Register("delete-graph", "delete a named graph and its contents (asks y/N)", CommandMeta{
		Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name"})
	env.Set("delete-graph", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if err := ev.RequireUser("delete-graph"); err != nil {
			return nil, err
		}
		if len(args) != 1 {
			return nil, errors.New("delete-graph expects a graph name: (delete-graph \"name\")")
		}
		name, ok := asString(args[0])
		if !ok {
			return nil, errors.New("delete-graph expects a graph name")
		}
		cg, err := gs.GetGraph(name)
		if err != nil {
			return nil, fmt.Errorf("graph not found: %s", name)
		}
		confirm := ev.Confirmer()
		if confirm == nil {
			return nil, fmt.Errorf("delete-graph %q is irreversible and needs a y/N confirmation — run it in the rf shell", name)
		}
		nodeCount, _ := cg.CountNodes()
		edgeCount, _ := cg.CountEdges()
		if !confirm(fmt.Sprintf("delete graph %q (%d nodes, %d edges)", name, nodeCount, edgeCount)) {
			return nil, fmt.Errorf("delete-graph %q: canceled", name)
		}
		if err := gs.DeleteGraph(name); err != nil {
			return nil, fmt.Errorf("failed to delete graph: %w", err)
		}
		return Symbol("ok"), nil
	}))

	Register("graph", "a named graph's stats — node/edge counts and metadata", CommandMeta{
		Command: true, MinArgs: 1, MaxArgs: 1, Usage: "name"})
	env.Set("graph", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("graph expects a graph name: (graph \"name\")")
		}
		name, ok := asString(args[0])
		if !ok {
			return nil, errors.New("graph expects a string (graph name)")
		}
		cg, err := gs.GetGraph(name)
		if err != nil {
			return nil, fmt.Errorf("graph not found: %s", name)
		}
		meta, err := cg.GetGraphMeta()
		if err != nil {
			return nil, fmt.Errorf("failed to get graph meta: %w", err)
		}
		nodeCount, _ := cg.CountNodes()
		edgeCount, _ := cg.CountEdges()
		return Dictionary{
			"name":       String(meta.Name),
			"nodes":      Integer(nodeCount),
			"edges":      Integer(edgeCount),
			"created_at": String(meta.CreatedAt.Format(time.RFC3339)),
			"updated_at": String(meta.UpdatedAt.Format(time.RFC3339)),
		}, nil
	}))

	Register("with-graph", "evaluate body forms with the named graph as the target", CommandMeta{})
	env.SetSpecialForm("with-graph", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		if len(args) < 2 {
			return nil, errors.New("with-graph expects at least 2 arguments: (with-graph graph-name body...)")
		}
		// Evaluate graph name
		name, ok := asString(args[0])
		if !ok {
			return nil, errors.New("with-graph expects a string (graph name)")
		}
		// Create new environment with current graph
		newEnv := NewEnvironment(env)
		newEnv.Set("_current_graph", String(name))
		// Evaluate body expressions
		var result Value
		var err error
		for i, expr := range args[1:] {
			if i == len(args)-2 { // last expression in tail position
				return e.tail(expr, newEnv), nil
			}
			result, err = e.Eval(expr, newEnv)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	})

	env.SetBuiltin("add-node", "add a node from a dictionary, returning its id (default graph)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var cg *core.Graph
		var props map[string]interface{}
		var err error
		var ok bool
		if len(args) == 1 {
			cg, err = resolveGraphArg(nil, env, gs)
			if err != nil {
				return nil, err
			}
			props, ok = asMap(args[0])
			if !ok {
				return nil, errors.New("add-node expects a dictionary")
			}
		} else if len(args) == 2 {
			cg, err = resolveGraphArg(args[0], env, gs)
			if err != nil {
				return nil, err
			}
			props, ok = asMap(args[1])
			if !ok {
				return nil, errors.New("add-node expects a dictionary")
			}
		} else {
			return nil, errors.New("add-node expects 1 or 2 arguments: (add-node dict) or (add-node graph dict)")
		}

		var nodeType *string
		if t, ok := props["type"]; ok {
			if s, ok := t.(String); ok {
				ts := string(s)
				nodeType = &ts
			}
		}
		propsJSON, err := json.Marshal(props)
		if err != nil {
			return nil, err
		}
		node := &models.Node{
			GraphID:    cg.ID,
			Type:       nodeType,
			Properties: (*json.RawMessage)(&propsJSON),
		}
		id, err := cg.InsertNode(context.Background(), node)
		if err != nil {
			return nil, err
		}
		return Integer(id), nil
	}))

	env.SetBuiltin("update-node", "replace a node's properties from a dictionary with an id key", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var cg *core.Graph
		var props map[string]interface{}
		var err error
		var ok bool
		if len(args) == 1 {
			cg, err = resolveGraphArg(nil, env, gs)
			if err != nil {
				return nil, err
			}
			props, ok = asMap(args[0])
			if !ok {
				return nil, errors.New("update-node expects a dictionary")
			}
		} else if len(args) == 2 {
			cg, err = resolveGraphArg(args[0], env, gs)
			if err != nil {
				return nil, err
			}
			props, ok = asMap(args[1])
			if !ok {
				return nil, errors.New("second argument must be a dictionary")
			}
		} else {
			return nil, errors.New("update-node expects 1 or 2 arguments: (update-node dict) or (update-node graph dict)")
		}

		// Check for mandatory "id" field
		idVal, ok := props["id"]
		if !ok {
			return nil, errors.New("update-node requires 'id' in dictionary")
		}
		nodeID, ok := asNumber(idVal)
		if !ok {
			return nil, errors.New("update-node 'id' must be a number")
		}

		// Get the existing node to preserve fields not being updated
		existingNode, err := cg.GetNode(context.Background(), nodeID)
		if err != nil {
			return nil, fmt.Errorf("node not found: %w", err)
		}

		// Update type if provided
		var nodeType *string
		if t, ok := props["type"]; ok {
			if s, ok := t.(String); ok {
				ts := string(s)
				nodeType = &ts
			}
		} else {
			nodeType = existingNode.Type
		}

		// Update properties
		propsJSON, err := json.Marshal(props)
		if err != nil {
			return nil, err
		}

		node := &models.Node{
			ID:         nodeID,
			GraphID:    cg.ID,
			Type:       nodeType,
			Properties: (*json.RawMessage)(&propsJSON),
		}

		updated, err := cg.UpdateNode(context.Background(), node)
		if err != nil {
			return nil, err
		}
		if updated {
			return true, nil
		}
		return false, nil
	}))

	env.SetBuiltin("add-edge", "add an edge from a dictionary with source/target node ids", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var cg *core.Graph
		var props map[string]interface{}
		var err error
		var ok bool
		if len(args) == 1 {
			cg, err = resolveGraphArg(nil, env, gs)
			if err != nil {
				return nil, err
			}
			props, ok = asMap(args[0])
			if !ok {
				return nil, errors.New("add-edge expects a dictionary")
			}
		} else if len(args) == 2 {
			cg, err = resolveGraphArg(args[0], env, gs)
			if err != nil {
				return nil, err
			}
			props, ok = asMap(args[1])
			if !ok {
				return nil, errors.New("second argument must be a dictionary")
			}
		} else {
			return nil, errors.New("add-edge expects 1 or 2 arguments: (add-edge dict) or (add-edge graph dict)")
		}

		// Check for mandatory "source" and "target" fields
		sourceVal, ok := props["source"]
		if !ok {
			return nil, errors.New("add-edge requires 'source' in dictionary")
		}
		source, ok := asNumber(sourceVal)
		if !ok {
			return nil, errors.New("add-edge 'source' must be a number")
		}

		targetVal, ok := props["target"]
		if !ok {
			return nil, errors.New("add-edge requires 'target' in dictionary")
		}
		target, ok := asNumber(targetVal)
		if !ok {
			return nil, errors.New("add-edge 'target' must be a number")
		}

		var edgeType *string
		if t, ok := props["type"]; ok {
			if s, ok := t.(String); ok {
				ts := string(s)
				edgeType = &ts
			}
		}
		propsJSON, err := json.Marshal(props)
		if err != nil {
			return nil, err
		}
		edge := &models.Edge{
			GraphID:    cg.ID,
			Source:     source,
			Target:     target,
			Type:       edgeType,
			Properties: (*json.RawMessage)(&propsJSON),
		}
		id, err := cg.InsertEdge(context.Background(), edge)
		if err != nil {
			return nil, err
		}
		return Integer(id), nil
	}))

	env.SetBuiltin("node", "fetch a node by id as a structured row with its edges attached", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var cg *core.Graph
		var err error
		var nodeID uint32
		var ok bool
		if len(args) == 1 {
			cg, err = resolveGraphArg(nil, env, gs)
			if err != nil {
				return nil, err
			}
			nodeID, ok = asNumber(args[0])
			if !ok {
				return nil, errors.New("node expects a node id as number")
			}
		} else if len(args) == 2 {
			cg, err = resolveGraphArg(args[0], env, gs)
			if err != nil {
				return nil, err
			}
			nodeID, ok = asNumber(args[1])
			if !ok {
				return nil, errors.New("second argument must be a number (node-id)")
			}
		} else {
			return nil, errors.New("node expects 1 or 2 arguments: (node id) or (node graph id)")
		}
		node, err := cg.GetNode(context.Background(), nodeID)
		if err != nil {
			return nil, fmt.Errorf("node not found: %w", err)
		}
		row := nodeRow(node)
		es, err := cg.GetNodeEdges(context.Background(), nodeID)
		if err != nil {
			return nil, fmt.Errorf("node edges: %w", err)
		}
		edgeRows := make([]Value, len(es))
		for i, edge := range es {
			edgeRows[i] = edgeRow(edge)
		}
		row["edges"] = edgeRows
		return row, nil
	}))

	Register("neighbors", "a node's neighboring nodes as rows with a direction column", CommandMeta{
		Command: true, MinArgs: 1, MaxArgs: 1, Usage: "id", Options: []Option{
			{Long: "graph", Short: "g", Kind: OptionString, Placeholder: "NAME", Doc: "target the named graph"},
		}})
	env.Set("neighbors", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("neighbors", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, errors.New("neighbors expects a node id: (neighbors id [{:graph \"name\"}])")
		}
		id, ok := optionInt(pos[0])
		if !ok {
			return nil, errors.New("neighbors expects a numeric node id")
		}
		var graphArg Value
		if name := OptString(opts, "graph", ""); name != "" {
			graphArg = String(name)
		}
		cg, err := resolveGraphArg(graphArg, env, gs)
		if err != nil {
			return nil, err
		}
		nodeID := uint32(id)
		if _, err := cg.GetNode(context.Background(), nodeID); err != nil {
			return nil, fmt.Errorf("node not found: %d", id)
		}
		out, err := cg.GetNeighbors(context.Background(), nodeID)
		if err != nil {
			return nil, fmt.Errorf("neighbors failed: %w", err)
		}
		in, err := cg.GetIncomingNeighbors(context.Background(), nodeID)
		if err != nil {
			return nil, fmt.Errorf("neighbors failed: %w", err)
		}
		result := make([]Value, 0, len(out)+len(in))
		for _, n := range out {
			row := nodeRow(n)
			row["direction"] = String("out")
			result = append(result, row)
		}
		for _, n := range in {
			row := nodeRow(n)
			row["direction"] = String("in")
			result = append(result, row)
		}
		return result, nil
	}))

	Register("delete-node", "delete nodes by id (their edges cascade)", CommandMeta{
		Command: true, MinArgs: 1, MaxArgs: -1, Usage: "id ..."})
	env.Set("delete-node", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("delete-node expects node ids: (delete-node id ...)")
		}
		cg, err := resolveGraphArg(nil, env, gs)
		if err != nil {
			return nil, err
		}
		for _, arg := range args {
			id, ok := optionInt(arg)
			if !ok {
				return nil, errors.New("delete-node expects numeric node ids")
			}
			if _, err := cg.GetNode(context.Background(), uint32(id)); err != nil {
				return nil, fmt.Errorf("node not found: %d", id)
			}
			if err := cg.DeleteNode(context.Background(), uint32(id)); err != nil {
				return nil, err
			}
		}
		return Symbol("ok"), nil
	}))

	Register("delete-edge", "delete edges by id", CommandMeta{
		Command: true, MinArgs: 1, MaxArgs: -1, Usage: "id ..."})
	env.Set("delete-edge", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("delete-edge expects edge ids: (delete-edge id ...)")
		}
		cg, err := resolveGraphArg(nil, env, gs)
		if err != nil {
			return nil, err
		}
		for _, arg := range args {
			id, ok := optionInt(arg)
			if !ok {
				return nil, errors.New("delete-edge expects numeric edge ids")
			}
			if _, err := cg.GetEdge(context.Background(), uint32(id)); err != nil {
				return nil, fmt.Errorf("edge not found: %d", id)
			}
			if err := cg.DeleteEdge(context.Background(), uint32(id)); err != nil {
				return nil, err
			}
		}
		return Symbol("ok"), nil
	}))

	env.SetBuiltin("shortest-path", "shortest path between nodes matched by attribute dicts, as node rows", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		var cg *core.Graph
		var startAttrs, endAttrs map[string]interface{}
		var err error
		var ok bool

		if len(args) == 3 {
			cg, err = resolveGraphArg(args[0], env, gs)
			if err != nil {
				return nil, err
			}
			startAttrs, ok = asMap(args[1])
			if !ok {
				return nil, errors.New("second argument must be a dictionary (start attributes)")
			}
			endAttrs, ok = asMap(args[2])
			if !ok {
				return nil, errors.New("third argument must be a dictionary (end attributes)")
			}
		} else if len(args) == 2 {
			cg, err = resolveGraphArg(nil, env, gs)
			if err != nil {
				return nil, err
			}
			startAttrs, ok = asMap(args[0])
			if !ok {
				return nil, errors.New("first argument must be a dictionary (start attributes)")
			}
			endAttrs, ok = asMap(args[1])
			if !ok {
				return nil, errors.New("second argument must be a dictionary (end attributes)")
			}
		} else {
			return nil, errors.New("shortest-path expects 2 or 3 arguments: (shortest-path start-attrs end-attrs) or (shortest-path graph start-attrs end-attrs)")
		}

		// Find the shortest path
		pathNodes, err := cg.ShortestPath(context.Background(), startAttrs, endAttrs)
		if err != nil {
			return nil, fmt.Errorf("shortest path error: %w", err)
		}

		// Convert path nodes to a list of dictionaries
		pathResult := make([]interface{}, len(pathNodes))
		for i, node := range pathNodes {
			// Convert node properties to a map
			var props map[string]interface{}
			if node.Properties != nil {
				if err := json.Unmarshal(*node.Properties, &props); err != nil {
					props = make(map[string]interface{})
				}
			} else {
				props = make(map[string]interface{})
			}

			// Add node metadata
			nodeInfo := map[string]interface{}{
				"id":         node.ID,
				"type":       node.Type,
				"properties": props,
			}
			pathResult[i] = nodeInfo
		}

		return pathResult, nil
	}))

	Register("graphs", "list graphs as rows with node/edge counts", CommandMeta{
		Command: true, MaxArgs: 0})
	env.Set("graphs", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("graphs expects no arguments")
		}

		graphNames, err := gs.ListGraphs()
		if err != nil {
			return nil, fmt.Errorf("failed to list graphs: %w", err)
		}

		result := make([]Value, 0, len(graphNames))
		for _, name := range graphNames {
			cg, err := gs.GetGraph(name)
			if err != nil {
				continue
			}
			nodeCount, _ := cg.CountNodes()
			edgeCount, _ := cg.CountEdges()
			result = append(result, Dictionary{
				"name":  String(name),
				"nodes": Integer(nodeCount),
				"edges": Integer(edgeCount),
			})
		}

		return result, nil
	}))

	topMeta := CommandMeta{Command: true, MaxArgs: 0, Options: []Option{
		{Long: "graph", Short: "g", Kind: OptionString, Placeholder: "NAME", Doc: "target the named graph"},
		{Long: "limit", Short: "n", Kind: OptionInt, Placeholder: "N", Doc: "max rows returned"},
	}}
	topEdges := func(name string, fetch func(cg *core.Graph, limit int) ([]*models.Node, []int, error)) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			pos, opts, err := ParseOptions(name, args)
			if err != nil {
				return nil, err
			}
			if len(pos) != 0 {
				return nil, fmt.Errorf("%s takes no positional arguments: (%s [{:graph \"name\" :limit n}])", name, name)
			}
			var graphArg Value
			if gname := OptString(opts, "graph", ""); gname != "" {
				graphArg = String(gname)
			}
			cg, err := resolveGraphArg(graphArg, env, gs)
			if err != nil {
				return nil, err
			}
			nodes, edgeCounts, err := fetch(cg, OptInt(opts, "limit", 0))
			if err != nil {
				return nil, fmt.Errorf("%s failed: %w", name, err)
			}
			result := make([]Value, len(nodes))
			for i, node := range nodes {
				row := nodeRow(node)
				row["count"] = Integer(edgeCounts[i])
				result[i] = row
			}
			return result, nil
		}
	}
	Register("top-incoming", "nodes ranked by incoming edge count, as rows with a count column", topMeta)
	env.Set("top-incoming", topEdges("top-incoming", func(cg *core.Graph, limit int) ([]*models.Node, []int, error) {
		return cg.GetTopNodesByIncomingEdges(limit)
	}))
	Register("top-outgoing", "nodes ranked by outgoing edge count, as rows with a count column", topMeta)
	env.Set("top-outgoing", topEdges("top-outgoing", func(cg *core.Graph, limit int) ([]*models.Node, []int, error) {
		return cg.GetTopNodesByOutgoingEdges(limit)
	}))

	Register("nodes", "list a graph's nodes as rows; a query full-text searches them ranked", CommandMeta{
		Command: true, MaxArgs: 1, Usage: "[query]", Options: []Option{
			{Long: "type", Short: "t", Kind: OptionString, Placeholder: "TYPE", Doc: "only nodes of this type"},
			{Long: "graph", Short: "g", Kind: OptionString, Placeholder: "NAME", Doc: "target the named graph"},
			{Long: "limit", Short: "n", Kind: OptionInt, Placeholder: "N", Doc: "max rows returned"},
		}})
	env.Set("nodes", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("nodes", args)
		if err != nil {
			return nil, err
		}
		var query string
		if len(pos) > 1 {
			return nil, errors.New("nodes expects at most one query: (nodes [\"query\"] [{:type \"task\" :graph \"name\" :limit n}])")
		}
		if len(pos) == 1 {
			s, ok := asString(pos[0])
			if !ok {
				return nil, errors.New("nodes expects a query string")
			}
			query = s
		}
		var graphArg Value
		if name := OptString(opts, "graph", ""); name != "" {
			graphArg = String(name)
		}
		cg, err := resolveGraphArg(graphArg, env, gs)
		if err != nil {
			return nil, err
		}
		limit := OptInt(opts, "limit", 0)
		typ := OptString(opts, "type", "")

		if query != "" {
			terms, _ := bm25QueryTerms(query)
			if gs.Embeddings().Enabled() {
				k := limit
				if k <= 0 {
					k = 10
				}
				ns, scores, err := cg.HybridSearchNodes(query, typ, k)
				if err == nil {
					result := make([]Value, len(ns))
					for i, node := range ns {
						row := nodeRow(node)
						row["score"] = roundScore(scores[i])
						markTermMatches(row, node.SearchText(), terms)
						result[i] = row
					}
					return result, nil
				}
				logger.Warn().Err(err).Msg("hybrid node search failed; falling back to FTS")
			}
			ns, ranks, err := cg.SearchNodesLexical(query, typ, limit)
			if err != nil {
				return nil, fmt.Errorf("nodes search failed: %w", err)
			}
			result := make([]Value, len(ns))
			for i, node := range ns {
				row := nodeRow(node)
				row["score"] = roundScore(-ranks[i])
				markTermMatches(row, node.SearchText(), terms)
				result[i] = row
			}
			return result, nil
		}

		var ns []*models.Node
		if typ != "" {
			ns, err = cg.NodesByType(typ, limit)
		} else {
			ns, err = cg.GetNodesByGraph(limit, 0)
		}
		if err != nil {
			return nil, fmt.Errorf("nodes failed: %w", err)
		}
		result := make([]Value, len(ns))
		for i, node := range ns {
			result[i] = nodeRow(node)
		}
		return result, nil
	}))

	Register("edges", "list a graph's edges as id/source/target/type rows", CommandMeta{
		Command: true, MaxArgs: 0, Options: []Option{
			{Long: "type", Short: "t", Kind: OptionString, Placeholder: "TYPE", Doc: "only edges of this type"},
			{Long: "graph", Short: "g", Kind: OptionString, Placeholder: "NAME", Doc: "target the named graph"},
			{Long: "limit", Short: "n", Kind: OptionInt, Placeholder: "N", Doc: "max rows returned"},
		}})
	env.Set("edges", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("edges", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 0 {
			return nil, errors.New("edges takes no positional arguments: (edges [{:type \"t\" :graph \"name\" :limit n}])")
		}
		var graphArg Value
		if name := OptString(opts, "graph", ""); name != "" {
			graphArg = String(name)
		}
		cg, err := resolveGraphArg(graphArg, env, gs)
		if err != nil {
			return nil, err
		}
		limit := OptInt(opts, "limit", 0)
		var es []*models.Edge
		if typ := OptString(opts, "type", ""); typ != "" {
			es, err = cg.GetEdgesByType(typ, limit, 0)
		} else {
			es, err = cg.GetEdgesByGraph(limit, 0)
		}
		if err != nil {
			return nil, fmt.Errorf("edges failed: %w", err)
		}
		result := make([]Value, len(es))
		for i, edge := range es {
			result[i] = edgeRow(edge)
		}
		return result, nil
	}))

	env.Set("default-graph", String(defaultGraphName))

	env.Set("graph-embeddings", true)

	Register("remember", "save a note to the default graph", CommandMeta{Command: true, MinArgs: 1, MaxArgs: -1, Usage: "text ..."})
	env.Set("remember", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("remember expects text: (remember \"text\")")
		}
		parts := make([]string, len(args))
		for i, arg := range args {
			s, ok := asString(arg)
			if !ok {
				return nil, errors.New("remember expects strings")
			}
			parts[i] = s
		}
		cg, err := resolveGraphArg(nil, env, gs)
		if err != nil {
			return nil, err
		}

		text := strings.Join(parts, " ")
		propsJSON, err := json.Marshal(map[string]interface{}{"type": "memory", "text": text})
		if err != nil {
			return nil, err
		}
		memoryType := "memory"
		node := &models.Node{
			GraphID:    cg.ID,
			Type:       &memoryType,
			Properties: (*json.RawMessage)(&propsJSON),
		}
		id, err := cg.InsertNode(context.Background(), node)
		if err != nil {
			return nil, fmt.Errorf("remember failed: %w", err)
		}
		return Integer(id), nil
	}))

	Register("recall", "search remembered notes", CommandMeta{
		Command: true, MaxArgs: 1, Usage: "[query]",
		Options: []Option{{Long: "limit", Short: "n", Kind: OptionInt, Placeholder: "N", Doc: "max memories returned"}}})
	env.Set("recall", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("recall", args)
		if err != nil {
			return nil, err
		}
		var query string
		if len(pos) > 1 {
			return nil, errors.New("recall expects at most one query: (recall), (recall query), or (recall query {:limit n})")
		}
		if len(pos) == 1 {
			s, ok := asString(pos[0])
			if !ok {
				return nil, errors.New("recall expects a query string: (recall query {:limit n})")
			}
			query = s
		}
		limit := OptInt(opts, "limit", 0)
		cg, err := resolveGraphArg(nil, env, gs)
		if err != nil {
			return nil, err
		}

		var nodes []*models.Node
		var scores []float64
		if query == "" {
			nodes, err = cg.NodesByType("memory", limit)
		} else {
			hybrid := false
			if gs.Embeddings().Enabled() {
				// Same hybrid arm as (nodes "query"), filtered to memories.
				k := limit
				if k <= 0 {
					k = 10
				}
				nodes, scores, err = cg.HybridSearchNodes(query, "memory", k)
				if err == nil {
					hybrid = true
				} else {
					logger.Warn().Err(err).Msg("hybrid recall failed; falling back to FTS")
				}
			}
			if !hybrid {
				var ranks []float64
				nodes, ranks, err = cg.SearchNodesLexical(query, "memory", limit)
				scores = make([]float64, len(ranks))
				for i, r := range ranks {
					scores[i] = -r
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("recall failed: %w", err)
		}

		// Memories render as a flat table: id, text, created_at (+ rank).
		result := make([]Value, len(nodes))
		for i, node := range nodes {
			dict := make(Dictionary)
			dict["id"] = Integer(node.ID)
			text := ""
			if v, ok := node.FormattedProperties()["text"].(string); ok {
				text = v
			}
			dict["text"] = String(text)
			dict["created_at"] = String(node.CreatedAt.Format(time.RFC3339))
			if scores != nil {
				dict["score"] = roundScore(scores[i])
			}
			if query != "" {
				terms, _ := bm25QueryTerms(query)
				markTermMatches(dict, text, terms)
			}
			result[i] = dict
		}
		return result, nil
	}))
}

func markTermMatches(row Dictionary, text string, terms []string) {
	if len(terms) == 0 {
		return
	}
	lower := strings.ToLower(text)
	hit := false
	for _, t := range terms {
		if strings.Contains(lower, t) {
			hit = true
			break
		}
	}
	if !hit {
		return
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = regexp.QuoteMeta(t)
	}
	row["_match"] = String("(?i)(" + strings.Join(quoted, "|") + ")")
}

func nodeRow(node *models.Node) Dictionary {
	dict := make(Dictionary)
	dict["id"] = Integer(node.ID)
	if node.Type != nil {
		dict["type"] = String(*node.Type)
	} else {
		dict["type"] = String("")
	}
	dict["created_at"] = String(node.CreatedAt.Format(time.RFC3339))
	props := make(Dictionary)
	for k, v := range node.FormattedProperties() {
		switch val := v.(type) {
		case string:
			props[k] = String(val)
		case float64:
			props[k] = Number(val)
		case bool:
			props[k] = val
		default:
			props[k] = String(fmt.Sprintf("%v", val))
		}
	}
	dict["properties"] = props
	return dict
}

func edgeRow(edge *models.Edge) Dictionary {
	dict := make(Dictionary)
	dict["id"] = Integer(edge.ID)
	dict["source"] = Integer(edge.Source)
	dict["target"] = Integer(edge.Target)
	if edge.Type != nil {
		dict["type"] = String(*edge.Type)
	} else {
		dict["type"] = String("")
	}
	return dict
}
