package eval

import (
	"strings"
	"testing"
	"time"
)

func rowStrings(t *testing.T, v Value, key string) []string {
	t.Helper()
	lst, ok := v.([]Value)
	if !ok {
		t.Fatalf("expected a list of rows, got %#v", v)
	}
	out := make([]string, len(lst))
	for i, item := range lst {
		dict, ok := item.(Dictionary)
		if !ok {
			t.Fatalf("expected a row, got %#v", item)
		}
		out[i] = textCell(dict[key])
	}
	return out
}

const whereFixture = `(list {:name "a" :size 10} {:name "b" :size 200} {:name "c" :size 3000} {:name "d"})`

func TestWhereComparisons(t *testing.T) {
	ev := NewEvaluator()
	for expr, want := range map[string]string{
		`(where "size" ">" "100" ` + whereFixture + `)`:  "b c",
		`(where "size" "<=" 200 ` + whereFixture + `)`:   "a b",
		`(where "size" "=" "200" ` + whereFixture + `)`:  "b",
		`(where "size" "!=" "200" ` + whereFixture + `)`: "a c", // d has no size — a missing field matches no comparison, != included
		`(where "name" "=" "b" ` + whereFixture + `)`:    "b",
		`(where "name" ">" "b" ` + whereFixture + `)`:    "c d",
	} {
		got, err := evalExpr(expr, ev, ev.globalEnv)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		names := strings.Join(rowStrings(t, got, "name"), " ")
		if names != want {
			t.Fatalf("%s: got %q, want %q", expr, names, want)
		}
	}
}

func TestWhereAndOr(t *testing.T) {
	ev := NewEvaluator()
	for expr, want := range map[string]string{
		`(where "size" ">" "100" "and" "size" "<" "1000" ` + whereFixture + `)`:                     "b",
		`(where "name" "=" "a" "or" "name" "=" "c" ` + whereFixture + `)`:                           "a c",
		`(where "size" ">" "100" "and" "size" "<" "1000" "or" "name" "=" "a" ` + whereFixture + `)`: "a b", // (∧) then ∨
		`(where "name" "=" "and" "or" "name" "=" "d" ` + whereFixture + `)`:                         "d",   // "and" as a value, not a connective
	} {
		got, err := evalExpr(expr, ev, ev.globalEnv)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		names := strings.Join(rowStrings(t, got, "name"), " ")
		if names != want {
			t.Fatalf("%s: got %q, want %q", expr, names, want)
		}
	}

	// A stray word where a connective belongs names the expectation.
	if _, err := evalExpr(`(where "size" ">" "1" "nor" "size" "<" "2" `+whereFixture+`)`, ev, ev.globalEnv); err == nil || !strings.Contains(err.Error(), "and/or") {
		t.Fatalf("bad connective: expected an and/or error, got %v", err)
	}
	// A truncated trailing clause errors instead of silently filtering.
	if _, err := evalExpr(`(where "size" ">" "1" "and" "size" `+whereFixture+`)`, ev, ev.globalEnv); err == nil || !strings.Contains(err.Error(), "where expects") {
		t.Fatalf("truncated clause: expected a usage error, got %v", err)
	}
}

func TestWhereSizeSuffixes(t *testing.T) {
	ev := NewEvaluator()
	fixture := `(list {:name "small" :size 512} {:name "mid" :size 2097152} {:name "big" :size 209715200})`
	for expr, want := range map[string]string{
		`(where "size" ">" "100MB" ` + fixture + `)`: "big",
		`(where "size" "<" "1k" ` + fixture + `)`:    "small",
		`(where "size" "=" "2mb" ` + fixture + `)`:   "mid", // suffixes are case-insensitive
		`(where "size" ">=" "2MiB" ` + fixture + `)`: "mid big",
	} {
		got, err := evalExpr(expr, ev, ev.globalEnv)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		names := strings.Join(rowStrings(t, got, "name"), " ")
		if names != want {
			t.Fatalf("%s: got %q, want %q", expr, names, want)
		}
	}
}

func TestWherePredicate(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(where (lambda (r) (> (get "size" r) 100)) (list {:name "a" :size 10} {:name "b" :size 200}))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if names := rowStrings(t, got, "name"); len(names) != 1 || names[0] != "b" {
		t.Fatalf("predicate where: %v", names)
	}
}

func TestWhereOpFunction(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(where "size" > 100 (list {:name "a" :size 10} {:name "b" :size 200}))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if names := rowStrings(t, got, "name"); len(names) != 1 || names[0] != "b" {
		t.Fatalf("op-function where: %v", names)
	}
}

func TestWhereStreamLazy(t *testing.T) {
	done := make(chan struct{})
	var got Value
	var err error
	go func() {
		defer close(done)
		ev := NewEvaluator()
		got, err = evalExpr(`(pipe (sh "yes") (where (lambda (l) true)) (take 2))`, ev, ev.globalEnv)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("(pipe (sh \"yes\") (where ...) (take 2)) did not terminate")
	}
	if err != nil || got != String("y\ny\n") {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestWhereNonRowErrors(t *testing.T) {
	ev := NewEvaluator()
	if _, err := evalExpr(`(where "size" ">" 1 (list "just" "lines"))`, ev, ev.globalEnv); err == nil || !strings.Contains(err.Error(), "no columns") {
		t.Fatalf("expected a no-columns error, got %v", err)
	}
}

func TestSortBy(t *testing.T) {
	ev := NewEvaluator()
	fixture := `(list {:name "a" :size 200} {:name "b"} {:name "c" :size "9"} {:name "d" :size 3000})`
	got, err := evalExpr(`(sort-by "size" `+fixture+`)`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(rowStrings(t, got, "name"), " "); names != "c a d b" {
		t.Fatalf("sort-by: %q", names)
	}
	got, err = evalExpr(`(sort-by "size" :desc `+fixture+`)`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(rowStrings(t, got, "name"), " "); names != "b d a c" {
		t.Fatalf("sort-by :desc: %q", names)
	}
}

func TestSortByScalars(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(sort-by (list "pear" "apple" "mango"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := []Value{String("apple"), String("mango"), String("pear")}
	lst, _ := got.([]Value)
	if len(lst) != 3 || lst[0] != want[0] || lst[1] != want[1] || lst[2] != want[2] {
		t.Fatalf("sort-by scalars: %#v", got)
	}
}

func TestGroupBy(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(get "dir" (group-by "type" (list {:name "a" :type "file"} {:name "b" :type "dir"} {:name "c" :type "dir"})))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(rowStrings(t, got, "name"), " "); names != "b c" {
		t.Fatalf("group-by: %q", names)
	}
}

func TestCountBy(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(count-by "type" (list {:type "file"} {:type "dir"} {:type "file"}))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if types := strings.Join(rowStrings(t, got, "type"), " "); types != "file dir" {
		t.Fatalf("count-by groups: %q", types)
	}
	if counts := strings.Join(rowStrings(t, got, "count"), " "); counts != "2 1" {
		t.Fatalf("count-by counts: %q", counts)
	}

	got, err = evalExpr(`(count-by (list "x" "y" "x"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if vals := strings.Join(rowStrings(t, got, "value"), " "); vals != "x y" {
		t.Fatalf("keyless count-by: %q", vals)
	}
}

func TestPick(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(pick "name" (list {:name "a" :size 1 :_match "pat"} {:name "b" :size 2}))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	lst, _ := got.([]Value)
	if len(lst) != 2 {
		t.Fatalf("pick: %#v", got)
	}
	first, _ := lst[0].(Dictionary)
	if len(first) != 2 || first["name"] != String("a") || first["_match"] != String("pat") {
		t.Fatalf("pick row: %#v", first)
	}
	if _, hasSize := first["size"]; hasSize {
		t.Fatalf("pick kept an unpicked column: %#v", first)
	}

	got, err = evalExpr(`(pick "size" {:name "a" :size 7})`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	dict, _ := got.(Dictionary)
	if len(dict) != 1 || dict["size"] != Integer(7) {
		t.Fatalf("pick single dict: %#v", got)
	}
}

func TestDottedPaths(t *testing.T) {
	ev := NewEvaluator()
	fixture := `(list {:id 1 :properties {:title "beta"}} {:id 2 :properties {:title "alpha"}} {:id 3 :properties {:kind "x"}})`

	got, err := evalExpr(`(sort-by "properties.title" `+fixture+`)`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(rowStrings(t, got, "id"), " "); ids != "2 1 3" {
		t.Fatalf("sort-by dotted: %q (missing path sorts last)", ids)
	}

	got, err = evalExpr(`(get "alpha" (group-by "properties.title" `+fixture+`))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(rowStrings(t, got, "id"), " "); ids != "2" {
		t.Fatalf("group-by dotted: %q", ids)
	}

	got, err = evalExpr(`(pick "id" "properties.title" `+fixture+`)`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	lst, _ := got.([]Value)
	first, _ := lst[0].(Dictionary)
	if first["properties.title"] != String("beta") {
		t.Fatalf("pick dotted lifts the value under the full path: %#v", first)
	}
	third, _ := lst[2].(Dictionary)
	if _, has := third["properties.title"]; has {
		t.Fatalf("pick dotted on a missing path adds no column: %#v", third)
	}

	got, err = evalExpr(`(where "properties.title" "=" "alpha" `+fixture+`)`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(rowStrings(t, got, "id"), " "); ids != "2" {
		t.Fatalf("where dotted: %q", ids)
	}

	// A literal dotted key beats the path reading
	got, err = evalExpr(`(pick "a.b" (list {:a.b 1 :a {:b 2}}))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if row := got.([]Value)[0].(Dictionary); row["a.b"] != Integer(1) {
		t.Fatalf("literal dotted key should win: %#v", row)
	}
}

func TestVerbsGuideToPipe(t *testing.T) {
	ev := NewEvaluator()
	for _, expr := range []string{
		`(where "size" ">" "100000")`,
		`(pick "name")`,
		`(group-by "project")`,
	} {
		if _, err := evalExpr(expr, ev, ev.globalEnv); err == nil || !strings.Contains(err.Error(), "pipe") {
			t.Fatalf("%s: expected a pipe hint, got %v", expr, err)
		}
	}
}
