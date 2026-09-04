package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseJSONSingleDoc(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(parse-json (lines "{\"name\": \"a\", \"stars\": 3}"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	dict, ok := got.(Dictionary)
	if !ok || dict["name"] != String("a") || dict["stars"] != Integer(3) {
		t.Fatalf("object: %#v", got)
	}

	got, err = evalExpr(`(parse-json (lines "[{\"n\": 1}, {\"n\": 2}]"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	lst, ok := got.([]Value)
	if !ok || len(lst) != 2 {
		t.Fatalf("array: %#v", got)
	}
	if row, _ := lst[1].(Dictionary); row["n"] != Integer(2) {
		t.Fatalf("array row: %#v", lst[1])
	}
}

func TestParseJSONLines(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(parse-json (json (list {:n 1} {:n 2})))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	lst, ok := got.([]Value)
	if !ok || len(lst) != 2 {
		t.Fatalf("jsonl: %#v", got)
	}
	if row, _ := lst[0].(Dictionary); row["n"] != Integer(1) {
		t.Fatalf("jsonl row: %#v", lst[0])
	}
}

func TestParseJSONFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(path, []byte(`{"ok": true, "items": [1, 2]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator()
	got, err := evalExpr(`(parse-json "`+path+`")`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	dict, ok := got.(Dictionary)
	if !ok || dict["ok"] != true {
		t.Fatalf("file: %#v", got)
	}
	if items, _ := dict["items"].([]Value); len(items) != 2 || items[0] != Integer(1) {
		t.Fatalf("nested array: %#v", dict["items"])
	}
}

func TestParseJSONErrors(t *testing.T) {
	ev := NewEvaluator()
	if _, err := evalExpr(`(parse-json (lines "not json"))`, ev, ev.globalEnv); err == nil || !strings.Contains(err.Error(), "parse-json") {
		t.Fatalf("invalid: %v", err)
	}
	if _, err := evalExpr(`(parse-json "{\"a\": 1}")`, ev, ev.globalEnv); err == nil || !strings.Contains(err.Error(), "lines") {
		t.Fatalf("string-is-a-file hint: %v", err)
	}
}

func TestFromCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.csv")
	csv := "name,price,code\nwidget,3.5,007\ngadget,10,1.50\n"
	if err := os.WriteFile(path, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator()
	got, err := evalExpr(`(from-csv "`+path+`")`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	lst, ok := got.([]Value)
	if !ok || len(lst) != 2 {
		t.Fatalf("rows: %#v", got)
	}
	first, _ := lst[0].(Dictionary)
	if first["name"] != String("widget") || first["price"] != Number(3.5) || first["code"] != String("007") {
		t.Fatalf("first row: %#v", first)
	}
	second, _ := lst[1].(Dictionary)
	if second["price"] != Integer(10) || second["code"] != String("1.50") {
		t.Fatalf("second row: %#v", second)
	}
}

func TestFromCSVOptions(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(from-csv (lines "a;b\n1;2") {:sep ";"})`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	lst, _ := got.([]Value)
	if len(lst) != 1 {
		t.Fatalf(":sep rows: %#v", got)
	}
	if row, _ := lst[0].(Dictionary); row["a"] != Integer(1) || row["b"] != Integer(2) {
		t.Fatalf(":sep row: %#v", lst[0])
	}

	got, err = evalExpr(`(from-csv (lines "x\ty") {:sep "tab"} :noheader)`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	lst, _ = got.([]Value)
	if len(lst) != 1 {
		t.Fatalf(":noheader rows: %#v", got)
	}
	if row, _ := lst[0].(Dictionary); row["c1"] != String("x") || row["c2"] != String("y") {
		t.Fatalf(":noheader row: %#v", lst[0])
	}
}

func TestFromCSVHeaderRepairs(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(from-csv (lines "a,,a\n1,2,3"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	lst, _ := got.([]Value)
	row, _ := lst[0].(Dictionary)
	if row["a"] != Integer(1) || row["c2"] != Integer(2) || row["a_2"] != Integer(3) {
		t.Fatalf("header repairs: %#v", row)
	}
}

func TestFromCSVComposes(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(pipe (lines "name,price\na,5\nb,50\nc,500") (from-csv) (where "price" ">" "10") (pick "name"))`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	lst, _ := got.([]Value)
	if len(lst) != 2 {
		t.Fatalf("compose: %#v", got)
	}
	if row, _ := lst[0].(Dictionary); row["name"] != String("b") {
		t.Fatalf("compose row: %#v", lst[0])
	}
}
