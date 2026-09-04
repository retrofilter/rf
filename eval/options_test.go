package eval

import (
	"strings"
	"testing"
)

func TestParseOptions(t *testing.T) {
	NewEvaluator() // registers the builtins' option tables

	pos, opts, err := ParseOptions("chunk", []Value{String("notes.md"), Dictionary{"format": String("markdown"), "size": String("10")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 1 || pos[0] != String("notes.md") {
		t.Fatalf("positionals: %#v", pos)
	}
	if OptString(opts, "format", "") != "markdown" || OptInt(opts, "size", 0) != 10 {
		t.Fatalf("options: %#v", opts)
	}

	pos, opts, err = ParseOptions("sort-by", []Value{Dictionary{"desc": true}, String("size"), []Value{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 2 || !OptBool(opts, "desc") {
		t.Fatalf("dict-first: pos %#v opts %#v", pos, opts)
	}

	// Bare keyword = boolean sugar; #f in the dict = unset.
	_, opts, err = ParseOptions("sort-by", []Value{Keyword("desc")})
	if err != nil || !OptBool(opts, "desc") {
		t.Fatalf("keyword sugar: %v %#v", err, opts)
	}
	_, opts, err = ParseOptions("sort-by", []Value{Dictionary{"desc": false}})
	if err != nil || OptBool(opts, "desc") {
		t.Fatalf("#f should unset: %v %#v", err, opts)
	}

	for expr, want := range map[string]string{
		"unknown key":         ":folder",
		"valued as keyword":   "takes a value",
		"kind mismatch":       ":size expects a number",
		"bool needs #t":       "use #t or #f",
		"no options declared": "takes no options",
	} {
		var err error
		switch expr {
		case "unknown key":
			_, _, err = ParseOptions("chunk", []Value{Dictionary{"flavor": String("x")}})
			want = ":size" // supported set named
		case "valued as keyword":
			_, _, err = ParseOptions("chunk", []Value{Keyword("size")})
		case "kind mismatch":
			_, _, err = ParseOptions("chunk", []Value{Dictionary{"size": String("ten")}})
		case "bool needs #t":
			_, _, err = ParseOptions("sort-by", []Value{Dictionary{"desc": String("yes")}})
		case "no options declared":
			_, _, err = ParseOptions("where", []Value{Keyword("bogus")})
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: expected error containing %q, got %v", expr, want, err)
		}
	}
}

func TestOptionCoherence(t *testing.T) {
	NewEvaluator()
	registryMu.RLock()
	defer registryMu.RUnlock()
	for name, info := range registry {
		longs := map[string]bool{}
		shorts := map[string]bool{}
		for _, o := range info.meta.Options {
			if o.Long == "" || strings.HasPrefix(o.Long, "-") {
				t.Errorf("%s: option long name %q must be bare", name, o.Long)
			}
			if longs[o.Long] {
				t.Errorf("%s: duplicate option --%s", name, o.Long)
			}
			longs[o.Long] = true
			if o.Short != "" {
				if len(o.Short) != 1 {
					t.Errorf("%s: short flag -%s must be one letter", name, o.Short)
				}
				if o.Short == "h" {
					t.Errorf("%s: -h is reserved for help", name)
				}
				if shorts[o.Short] {
					t.Errorf("%s: duplicate short -%s", name, o.Short)
				}
				shorts[o.Short] = true
			}
			if o.Doc == "" {
				t.Errorf("%s: option --%s has no doc line", name, o.Long)
			}
			if o.Kind != OptionBool && o.Placeholder == "" {
				t.Errorf("%s: valued option --%s needs a placeholder for help", name, o.Long)
			}
		}
	}
}
