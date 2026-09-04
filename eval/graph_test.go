package eval

import (
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func createTestDB(t *testing.T) *sqlx.DB {
	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	err = schema.CreateTables(db)
	require.NoError(t, err)
	return db
}

func evalGraphExpr(expr string, eval *Evaluator, env *Environment) (Value, error) {
	ast, err := Parse(expr)
	if err != nil {
		return nil, err
	}
	return eval.Eval(ast, env)
}

func TestGraphBuiltins(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	gs := core.NewGraphStore(db)
	eval := NewEvaluatorWithEnvironment(db, gs)
	env := eval.globalEnv
	eval.SetConfirmer(func(string) bool { return true })

	tests := []struct {
		desc      string
		expr      string
		expectErr bool
		expectNil bool        // expect result to be nil
		check     func(Value) // optional custom check
	}{
		{"graphs empty", `(graphs)`, false, false, func(result Value) {
			// Should return an empty list when no graphs exist
			list, ok := result.([]Value)
			require.True(t, ok, "expected list result")
			require.Empty(t, list, "expected empty list when no graphs exist")
		}},
		// Create with string
		{"create-graph string", `(create-graph "gstr")`, false, false, nil},
		// Get graph
		{"graph string", `(graph "gstr")`, false, false, nil},
		// Get non-existent graph
		{"graph non-existent", `(graph "non-existent")`, true, true, nil},
		// Delete with string
		{"delete-graph string", `(delete-graph "gstr")`, false, false, nil},
		// Error: wrong type
		{"create-graph wrong type", `(create-graph 123)`, true, true, nil},
		{"delete-graph wrong type", `(delete-graph 1.2)`, true, true, nil},
		// Error: missing arg
		{"create-graph missing arg", `(create-graph)`, true, true, nil},
		{"delete-graph missing arg", `(delete-graph)`, true, true, nil},
		{"graph missing arg", `(graph)`, true, true, nil},

		// Insert node
		{"add-node", `(begin
		(create-graph "test")
		(add-node "test" {:type "person" :name "Alice" :id 1})
		(add-node "test" {:type "person" :name "Bob" :id 2})
		)`, false, false, nil},

		{"graphs with graphs", `(graphs)`, false, false, func(result Value) {
			// Rows with name and node/edge counts
			list, ok := result.([]Value)
			require.True(t, ok, "expected list result")
			require.Len(t, list, 1, "expected 1 graph")
			row, ok := list[0].(Dictionary)
			require.True(t, ok, "expected dictionary rows")
			require.Equal(t, String("test"), row["name"])
			require.Equal(t, Integer(2), row["nodes"])
			require.Equal(t, Integer(0), row["edges"])
		}},

		{"graph stats dict", `(graph "test")`, false, false, func(result Value) {
			row, ok := result.(Dictionary)
			require.True(t, ok, "expected a stats dictionary")
			require.Equal(t, String("test"), row["name"])
			require.Equal(t, Integer(2), row["nodes"])
			require.Contains(t, row, "created_at")
		}},

		// Insert node using with-graph
		{"add-node with-graph", `(begin
		(create-graph "with-graph-test")
		(with-graph "with-graph-test" (add-node {:type "person" :name "Charlie" :id 3}))
		)`, false, false, nil},

		// Update node
		{"update-node", `(begin
		(create-graph "update-test")
		(add-node "update-test" {:type "person" :name "Alice" :id 1})
		(update-node "update-test" {:id 1 :name "Alice Updated" :age 30})
		)`, false, false, nil},

		// Update node using with-graph
		{"update-node with-graph", `(begin
		(create-graph "update-with-graph-test")
		(with-graph "update-with-graph-test" (add-node {:type "person" :name "Bob" :id 2}))
		(with-graph "update-with-graph-test" (update-node {:id 2 :name "Bob Updated" :age 25}))
		)`, false, false, nil},

		// Update node without id (should error)
		{"update-node without id", `(begin
		(create-graph "update-error-test")
		(add-node "update-error-test" {:type "person" :name "Charlie" :id 3})
		(update-node "update-error-test" {:name "Charlie Updated"})
		)`, true, true, nil},

		// Insert edge
		{"add-edge", `(begin
		(create-graph "edge-test")
		(add-node "edge-test" {:type "person" :name "Alice" :id 1})
		(add-node "edge-test" {:type "person" :name "Bob" :id 2})
		(add-edge "edge-test" {:source 1 :target 2 :type "knows" :since 2020})
		)`, false, false, nil},

		// Insert edge using with-graph
		{"add-edge with-graph", `(begin
		(create-graph "edge-with-graph-test")
		(with-graph "edge-with-graph-test" (add-node {:type "person" :name "Charlie" :id 3}))
		(with-graph "edge-with-graph-test" (add-node {:type "person" :name "David" :id 4}))
		(with-graph "edge-with-graph-test" (add-edge {:source 3 :target 4 :type "works_with" :department "engineering"}))
		)`, false, false, nil},

		// Insert edge without source (should error)
		{"add-edge without source", `(begin
		(create-graph "edge-error-test")
		(add-node "edge-error-test" {:type "person" :name "Eve" :id 5})
		(add-node "edge-error-test" {:type "person" :name "Frank" :id 6})
		(add-edge "edge-error-test" {:target 6 :type "knows"})
		)`, true, true, nil},

		// Insert edge without target (should error)
		{"add-edge without target", `(begin
		(create-graph "edge-error-test2")
		(add-node "edge-error-test2" {:type "person" :name "Grace" :id 7})
		(add-node "edge-error-test2" {:type "person" :name "Henry" :id 8})
		(add-edge "edge-error-test2" {:source 7 :type "knows"})
		)`, true, true, nil},

		// Get node
		{"node", `(begin
		(create-graph "node-test")
		(add-node "node-test" {:type "person" :name "Alice" :id 1})
		(node "node-test" 1)
		)`, false, false, nil},

		// Get node using with-graph
		{"node with-graph", `(begin
		(create-graph "node-with-graph-test")
		(with-graph "node-with-graph-test" (add-node {:type "person" :name "Bob" :id 2}))
		(with-graph "node-with-graph-test" (node 2))
		)`, false, false, nil},

		// Get non-existent node (should error)
		{"node non-existent", `(begin
		(create-graph "node-error-test")
		(node "node-error-test" 999)
		)`, true, true, nil},

		// Shortest path tests
		{"shortest-path basic", `(begin
		(create-graph "shortest-path-test")
		(define munich-id (add-node "shortest-path-test" {:type "airport" :city "Munich" :code "MUC"}))
		(define brisbane-id (add-node "shortest-path-test" {:type "airport" :city "Brisbane" :code "BNE"}))
		(add-edge "shortest-path-test" {:source munich-id :target brisbane-id :type "route" :weight 5})
		(shortest-path "shortest-path-test" {:city "Munich"} {:city "Brisbane"})
		)`, false, false, nil},

		{"shortest-path with-graph", `(begin
		(create-graph "shortest-path-with-graph-test")
		(define sydney-id (with-graph "shortest-path-with-graph-test" (add-node {:type "airport" :city "Sydney" :code "SYD"})))
		(define melbourne-id (with-graph "shortest-path-with-graph-test" (add-node {:type "airport" :city "Melbourne" :code "MEL"})))
		(with-graph "shortest-path-with-graph-test" (add-edge {:source sydney-id :target melbourne-id :type "route" :weight 3}))
		(with-graph "shortest-path-with-graph-test" (shortest-path {:city "Sydney"} {:city "Melbourne"}))
		)`, false, false, nil},

		// Shortest path error cases
		{"shortest-path missing start attrs", `(begin
		(create-graph "shortest-path-error-test")
		(shortest-path "shortest-path-error-test" {} {:city "Brisbane"})
		)`, true, true, nil},

		{"shortest-path missing end attrs", `(begin
		(create-graph "shortest-path-error-test2")
		(shortest-path "shortest-path-error-test2" {:city "Munich"} {})
		)`, true, true, nil},

		{"shortest-path wrong arg types", `(begin
		(create-graph "shortest-path-error-test3")
		(shortest-path "shortest-path-error-test3" "not-a-map" {:city "Brisbane"})
		)`, true, true, nil},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got, err := evalGraphExpr(tc.expr, eval, env)
			if tc.expectErr {
				if err == nil {
					t.Errorf("expected error for %q, got value %v", tc.expr, got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
				return
			}
			if tc.expectNil && got != nil {
				t.Errorf("expected nil for %q, got %v", tc.expr, got)
			}
			if tc.check != nil {
				tc.check(got)
			}
		})
	}
}

func TestNodesSearch(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	gs := core.NewGraphStore(db)
	eval := NewEvaluatorWithEnvironment(db, gs)
	env := eval.globalEnv

	_, err := evalGraphExpr(`(begin
		(create-graph "kb")
		(add-node "kb" {:type "note" :title "meeting with alice"})
		(add-node "kb" {:type "idea" :title "lunch with alice"})
		(add-node "kb" {:type "note" :title "lunch with bob"}))`, eval, env)
	require.NoError(t, err)

	v, err := evalGraphExpr(`(nodes "bob" {:graph "kb"})`, eval, env)
	require.NoError(t, err)
	list, ok := v.([]Value)
	require.True(t, ok, "expected list result")
	require.Len(t, list, 1)
	dict, ok := list[0].(Dictionary)
	require.True(t, ok, "expected dictionary rows")
	require.Equal(t, String("note"), dict["type"])
	props, ok := dict["properties"].(Dictionary)
	require.True(t, ok, "expected nested properties")
	require.Equal(t, String("lunch with bob"), props["title"])
	score, ok := dict["score"].(Number)
	require.True(t, ok, "expected a score column")
	require.Greater(t, float64(score), 0.0, "score is the negated FTS rank")
	require.NotContains(t, dict, "rank")

	// The query composes with :type — FTS restricted to one node type
	v, err = evalGraphExpr(`(nodes "alice" {:graph "kb" :type "idea"})`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)

	v, err = evalGraphExpr(`(bm25 "alice" (nodes "with" {:graph "kb"}))`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 2)
	for _, item := range v.([]Value) {
		title := string(item.(Dictionary)["properties"].(Dictionary)["title"].(String))
		require.Contains(t, title, "alice")
	}
	// Dotted :key selects the nested column explicitly
	v, err = evalGraphExpr(`(bm25 "bob" (nodes {:graph "kb"}) {:key "properties.title"})`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)

	// Current graph via with-graph
	v, err = evalGraphExpr(`(with-graph "kb" (nodes "lunch"))`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 2)

	// Limit
	v, err = evalGraphExpr(`(with-graph "kb" (nodes "with" {:limit 1}))`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)

	// No match
	v, err = evalGraphExpr(`(nodes "zebra" {:graph "kb"})`, eval, env)
	require.NoError(t, err)
	require.Empty(t, v)

	// Errors
	_, err = evalGraphExpr(`(nodes "a" "b")`, eval, env)
	require.Error(t, err)
	_, err = evalGraphExpr(`(nodes 42)`, eval, env)
	require.Error(t, err)
}

func TestNodesAndDeletes(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	gs := core.NewGraphStore(db)
	eval := NewEvaluatorWithEnvironment(db, gs)
	env := eval.globalEnv

	_, err := evalGraphExpr(`(begin
		(create-graph "kb")
		(define a (add-node "kb" {:type "note" :title "first"}))
		(define b (add-node "kb" {:type "idea" :title "second"}))
		(define e (add-edge "kb" {:source a :target b :type "refines"})))`, eval, env)
	require.NoError(t, err)

	// nodes lists structured rows; :type filters; :limit caps
	v, err := evalGraphExpr(`(nodes {:graph "kb"})`, eval, env)
	require.NoError(t, err)
	list := v.([]Value)
	require.Len(t, list, 2)
	row := list[0].(Dictionary)
	require.Equal(t, String("first"), row["properties"].(Dictionary)["title"])
	require.Contains(t, row, "id")
	v, err = evalGraphExpr(`(nodes {:graph "kb" :type "idea"})`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)
	v, err = evalGraphExpr(`(nodes {:graph "kb" :limit 1})`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)

	// with-graph scopes nodes like every other graph builtin
	v, err = evalGraphExpr(`(with-graph "kb" (nodes {:type "note"}))`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)

	// delete-edge removes just the edge
	_, err = evalGraphExpr(`(delete-edge e)`, eval, env)
	require.NoError(t, err)
	_, err = evalGraphExpr(`(delete-edge e)`, eval, env)
	require.Error(t, err, "deleting a deleted edge errors")

	// delete-node removes the node; a second delete errors
	_, err = evalGraphExpr(`(delete-node a)`, eval, env)
	require.NoError(t, err)
	_, err = evalGraphExpr(`(delete-node a)`, eval, env)
	require.Error(t, err)
	v, err = evalGraphExpr(`(nodes {:graph "kb"})`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)

	// errors
	_, err = evalGraphExpr(`(delete-node)`, eval, env)
	require.Error(t, err)
	_, err = evalGraphExpr(`(delete-node "x")`, eval, env)
	require.Error(t, err)
}

func TestTopIncomingRows(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	gs := core.NewGraphStore(db)
	eval := NewEvaluatorWithEnvironment(db, gs)
	env := eval.globalEnv

	_, err := evalGraphExpr(`(begin
		(create-graph "kb")
		(define hub (add-node "kb" {:type "topic" :title "hub"}))
		(define x (add-node "kb" {:type "note" :title "x"}))
		(define y (add-node "kb" {:type "note" :title "y"}))
		(add-edge "kb" {:source x :target hub :type "about"})
		(add-edge "kb" {:source y :target hub :type "about"}))`, eval, env)
	require.NoError(t, err)

	v, err := evalGraphExpr(`(with-graph "kb" (top-incoming {:limit 1}))`, eval, env)
	require.NoError(t, err)
	list := v.([]Value)
	require.Len(t, list, 1)
	row, ok := list[0].(Dictionary)
	require.True(t, ok, "expected dictionary rows")
	require.Equal(t, String("hub"), row["properties"].(Dictionary)["title"])
	require.Equal(t, Integer(2), row["count"])

	v, err = evalGraphExpr(`(top-outgoing {:graph "kb" :limit 2})`, eval, env)
	require.NoError(t, err)
	for _, item := range v.([]Value) {
		row, ok := item.(Dictionary)
		require.True(t, ok, "expected dictionary rows")
		require.Equal(t, Integer(1), row["count"])
	}

	// The old positional overloads are gone
	_, err = evalGraphExpr(`(with-graph "kb" (top-incoming 1))`, eval, env)
	require.Error(t, err)
	_, err = evalGraphExpr(`(top-outgoing "kb")`, eval, env)
	require.Error(t, err)
}

func TestEdgesNeighborsNode(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	gs := core.NewGraphStore(db)
	eval := NewEvaluatorWithEnvironment(db, gs)
	env := eval.globalEnv

	_, err := evalGraphExpr(`(begin
		(create-graph "kb")
		(define hub (add-node "kb" {:type "topic" :title "hub"}))
		(define x (add-node "kb" {:type "note" :title "x"}))
		(define y (add-node "kb" {:type "note" :title "y"}))
		(add-edge "kb" {:source x :target hub :type "about"})
		(add-edge "kb" {:source hub :target y :type "refines"}))`, eval, env)
	require.NoError(t, err)

	// edges lists flat id/source/target/type rows; :type filters, :limit caps
	v, err := evalGraphExpr(`(edges {:graph "kb"})`, eval, env)
	require.NoError(t, err)
	list := v.([]Value)
	require.Len(t, list, 2)
	row := list[0].(Dictionary)
	require.Contains(t, row, "id")
	require.Contains(t, row, "source")
	require.Contains(t, row, "target")
	require.Equal(t, String("about"), row["type"])
	v, err = evalGraphExpr(`(edges {:graph "kb" :type "refines"})`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)
	v, err = evalGraphExpr(`(with-graph "kb" (edges {:limit 1}))`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)
	_, err = evalGraphExpr(`(edges "kb")`, eval, env)
	require.Error(t, err, "edges takes no positional arguments")

	// node returns the structured row with its edges (both directions) attached
	v, err = evalGraphExpr(`(with-graph "kb" (node hub))`, eval, env)
	require.NoError(t, err)
	nodeDict := v.(Dictionary)
	require.Equal(t, String("hub"), nodeDict["properties"].(Dictionary)["title"])
	nodeEdges := nodeDict["edges"].([]Value)
	require.Len(t, nodeEdges, 2)
	require.Contains(t, nodeEdges[0].(Dictionary), "source")

	// neighbors: node rows one edge away, tagged by direction
	v, err = evalGraphExpr(`(neighbors hub {:graph "kb"})`, eval, env)
	require.NoError(t, err)
	rows := v.([]Value)
	require.Len(t, rows, 2)
	byDir := map[string]string{}
	for _, item := range rows {
		r := item.(Dictionary)
		byDir[string(r["direction"].(String))] = string(r["properties"].(Dictionary)["title"].(String))
	}
	require.Equal(t, map[string]string{"out": "y", "in": "x"}, byDir)

	_, err = evalGraphExpr(`(neighbors 99999 {:graph "kb"})`, eval, env)
	require.Error(t, err, "neighbors of a missing node errors")
}

func TestRememberRecall(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	gs := core.NewGraphStore(db)
	eval := NewEvaluatorWithEnvironment(db, gs)
	env := eval.globalEnv

	// remember creates the default graph lazily and returns the node id
	v, err := evalGraphExpr(`(remember "lunch" "with" "bob")`, eval, env)
	require.NoError(t, err)
	require.IsType(t, Integer(0), v)
	_, err = gs.GetGraph("knowledge-base")
	require.NoError(t, err, "default graph should be auto-created")

	_, err = evalGraphExpr(`(remember "quarterly planning is due friday")`, eval, env)
	require.NoError(t, err)

	// A non-memory node in the same graph must not surface in recall
	_, err = evalGraphExpr(`(add-node {:type "note" :title "bob's number"})`, eval, env)
	require.NoError(t, err)

	// recall with no args lists all memories, oldest first
	v, err = evalGraphExpr(`(recall)`, eval, env)
	require.NoError(t, err)
	list := v.([]Value)
	require.Len(t, list, 2)
	first := list[0].(Dictionary)
	require.Equal(t, String("lunch with bob"), first["text"])
	require.Contains(t, first, "created_at")

	v, err = evalGraphExpr(`(recall "bob")`, eval, env)
	require.NoError(t, err)
	list = v.([]Value)
	require.Len(t, list, 1)
	require.Equal(t, String("lunch with bob"), list[0].(Dictionary)["text"])

	// limit applies (FTS5 OR matches both memories; limit keeps one)
	v, err = evalGraphExpr(`(recall "bob OR quarterly" {:limit 1})`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)

	// with-graph overrides the default target
	_, err = evalGraphExpr(`(begin
		(create-graph "work")
		(with-graph "work" (remember "standup at nine")))`, eval, env)
	require.NoError(t, err)
	v, err = evalGraphExpr(`(with-graph "work" (recall))`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)
	v, err = evalGraphExpr(`(recall "standup")`, eval, env)
	require.NoError(t, err)
	require.Empty(t, v, "default-graph recall should not see the work graph")

	// errors
	_, err = evalGraphExpr(`(remember)`, eval, env)
	require.Error(t, err)
	_, err = evalGraphExpr(`(recall 1 2 3)`, eval, env)
	require.Error(t, err)
}

func TestDefaultGraphBinding(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	gs := core.NewGraphStore(db)
	eval := NewEvaluatorWithEnvironment(db, gs)
	env := eval.globalEnv

	// Redefining default-graph (as a prelude would) redirects the fallback
	_, err := evalGraphExpr(`(define default-graph "personal")`, eval, env)
	require.NoError(t, err)
	_, err = evalGraphExpr(`(remember "feed the cat")`, eval, env)
	require.NoError(t, err)
	_, err = gs.GetGraph("personal")
	require.NoError(t, err, "redefined default graph should be auto-created")
	_, err = gs.GetGraph("knowledge-base")
	require.Error(t, err, "the stock default graph should not exist")

	v, err := evalGraphExpr(`(recall "cat")`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)

	_, err = evalGraphExpr(`(nodes "cat" {:graph "nope"})`, eval, env)
	require.Error(t, err)
	_, err = evalGraphExpr(`(with-graph "nope" (recall))`, eval, env)
	require.Error(t, err)

	// a bare nodes query hits the default graph too
	v, err = evalGraphExpr(`(nodes "cat")`, eval, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1)
}

func TestDeleteGraphGuards(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	gs := core.NewGraphStore(db)
	ev := NewEvaluatorWithEnvironment(db, gs)
	env := ev.globalEnv

	_, err := evalGraphExpr(`(create-graph "g")`, ev, env)
	require.NoError(t, err)

	// Assistant callers never reach the prompt, wrappers included.
	ev.SetCaller(CallerAssistant)
	_, err = evalGraphExpr(`(delete-graph "g")`, ev, env)
	require.ErrorContains(t, err, "only be run by the user")
	_, err = evalGraphExpr(`(begin (define (nuke) (delete-graph "g")) (nuke))`, ev, env)
	require.ErrorContains(t, err, "only be run by the user")
	ev.SetCaller(CallerUser)

	// No confirmer installed (scripts, non-terminal rf -e): fail closed.
	_, err = evalGraphExpr(`(delete-graph "g")`, ev, env)
	require.ErrorContains(t, err, "needs a y/N confirmation")

	// A declined prompt aborts; the graph survives both refusals.
	var asked string
	ev.SetConfirmer(func(action string) bool { asked = action; return false })
	_, err = evalGraphExpr(`(delete-graph "g")`, ev, env)
	require.ErrorContains(t, err, "canceled")
	require.Contains(t, asked, `delete graph "g"`)
	_, err = gs.GetGraph("g")
	require.NoError(t, err, "graph should survive refused deletions")

	// A missing graph errors before anyone is asked.
	asked = ""
	_, err = evalGraphExpr(`(delete-graph "nope")`, ev, env)
	require.ErrorContains(t, err, "graph not found")
	require.Empty(t, asked)

	// The prompt shows the blast radius, and yes deletes.
	_, err = evalGraphExpr(`(add-node "g" {:type "person" :name "Alice" :id 1})`, ev, env)
	require.NoError(t, err)
	ev.SetConfirmer(func(action string) bool { asked = action; return true })
	_, err = evalGraphExpr(`(delete-graph "g")`, ev, env)
	require.NoError(t, err)
	require.Contains(t, asked, "1 nodes")
	_, err = gs.GetGraph("g")
	require.Error(t, err)
}
