package eval

import (
	"strings"
	"testing"
)

func TestHelpCommand(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(help "dir")`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	text, ok, err := AsString(got)
	if err != nil || !ok {
		t.Fatalf("help should return a line stream: %v %v", got, err)
	}
	for _, want := range []string{
		"dir — list a directory as rows",
		"usage:  dir [path] [flags]",
		"scheme: (dir [path] [{:all #t}])",
		"-a, --all",
		"include dotfiles",
		"-h, --help",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help dir missing %q:\n%s", want, text)
		}
	}

	got, err = evalExpr(`(help "where")`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	text, _, _ = AsString(got)
	if !strings.Contains(text, "where — filter rows") || strings.Contains(text, "--help") {
		t.Errorf("flagless help wrong:\n%s", text)
	}

	// Unknown names error toward the listings.
	if _, err = evalExpr(`(help "no-such-thing")`, ev, ev.globalEnv); err == nil || !strings.Contains(err.Error(), "no builtin named") {
		t.Errorf("unknown name: %v", err)
	}
}

func TestHelpListing(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr(`(help)`, ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := got.([]Value)
	if !ok || len(rows) == 0 {
		t.Fatalf("help listing: %#v", got)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		d := r.(Dictionary)
		seen[string(d["command"].(String))] = true
		if d["doc"] == String("") {
			t.Errorf("command %v has no doc line", d["command"])
		}
	}
	for _, want := range []string{"dir", "where", "help", "similar"} {
		if !seen[want] {
			t.Errorf("help listing missing %q", want)
		}
	}
}
