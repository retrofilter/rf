package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustEvalSeq(t *testing.T, ev *Evaluator, exprs ...string) Value {
	t.Helper()
	var last Value
	for _, expr := range exprs {
		v, err := evalExpr(expr, ev, ev.globalEnv)
		if err != nil {
			t.Fatalf("eval %q: %v", expr, err)
		}
		last = v
	}
	return last
}

func TestDefineLibraryAndImport(t *testing.T) {
	ev := NewEvaluator()
	mustEvalSeq(t, ev, `(define-library (my utils)
	   (export triple (rename helper util-helper))
	   (begin
	     (define (triple x) (* 3 x))
	     (define (helper) 'h)))`)

	// D5: the body evaluated straight into the global env.
	if v := mustEvalSeq(t, ev, `(triple 2)`); !deepEqual(v, Integer(6)) {
		t.Errorf("triple bound globally: got %v", PrintValue(v))
	}

	// Plain import binds renamed exports; prefix/rename/only compose.
	mustEvalSeq(t, ev, `(import (my utils))`)
	if v := mustEvalSeq(t, ev, `(util-helper)`); !deepEqual(v, Symbol("h")) {
		t.Errorf("renamed export: got %v", PrintValue(v))
	}
	mustEvalSeq(t, ev, `(import (prefix (my utils) my-))`)
	if v := mustEvalSeq(t, ev, `(my-triple 3)`); !deepEqual(v, Integer(9)) {
		t.Errorf("prefix import: got %v", PrintValue(v))
	}
	mustEvalSeq(t, ev, `(import (rename (my utils) (triple thrice)))`)
	if v := mustEvalSeq(t, ev, `(thrice 2)`); !deepEqual(v, Integer(6)) {
		t.Errorf("rename import: got %v", PrintValue(v))
	}
	mustEvalSeq(t, ev, `(import (only (my utils) triple))`)
	mustEvalSeq(t, ev, `(import (except (my utils) triple))`)
	// Standard libraries are preloaded no-ops.
	mustEvalSeq(t, ev, `(import (scheme base) (scheme char) (scheme complex))`)
	mustEvalSeq(t, ev, `(import (only (scheme base) car))`)

	for _, bad := range []string{
		`(import (no such library))`,
		`(import (only (my utils) missing-export))`,
		`(import (rename (my utils) (missing-export x)))`,
		`(import (except (my utils) missing-export))`,
		`(import (prefix (scheme base) b-))`, // thin design: no export enumeration
		`(import (rename (scheme base) (car head)))`,
		`(import)`,
		`(import 42)`,
	} {
		if _, err := evalExpr(bad, ev, ev.globalEnv); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestLibraryExportMustExist(t *testing.T) {
	ev := NewEvaluator()
	// Exports are checked lazily at import time (thin design).
	mustEvalSeq(t, ev, `(define-library (ghost) (export phantom))`)
	if _, err := evalExpr(`(import (ghost))`, ev, ev.globalEnv); err == nil ||
		!strings.Contains(err.Error(), "phantom") {
		t.Errorf("importing a phantom export should error naming it, got %v", err)
	}
}

func TestCondExpand(t *testing.T) {
	runStdlibTests(t, []struct {
		expr      string
		expect    Value
		shouldErr bool
	}{
		{`(cond-expand (r7rs 'yes) (else 'no))`, Symbol("yes"), false},
		{`(cond-expand (this-feature-does-not-exist 1) (else 2))`, Integer(2), false},
		{`(cond-expand ((and r7rs retrofilter) 'both) (else 'no))`, Symbol("both"), false},
		{`(cond-expand ((or this-feature-does-not-exist r7rs) 'or) (else 'no))`, Symbol("or"), false},
		{`(cond-expand ((not this-feature-does-not-exist) 'not) (else 'no))`, Symbol("not"), false},
		{`(cond-expand ((library (scheme base)) 'lib) (else 'no))`, Symbol("lib"), false},
		{`(cond-expand ((library (no such library)) 'lib) (else 'no))`, Symbol("no"), false},
		// Multi-form bodies run with begin semantics.
		{`(cond-expand (r7rs (define ce-x 1) (+ ce-x 1)))`, Integer(2), false},
		// No matching clause is an error per §4.2.1.
		{`(cond-expand (this-feature-does-not-exist 'x))`, nil, true},
		{`(cond-expand)`, nil, true},
		{`(cond-expand 42)`, nil, true},
	})
}

func TestIncludeAndIncludeCI(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.scm")
	if err := os.WriteFile(plain, []byte("(define included-x 40)\n(+ included-x 2)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(dir, "upper.scm")
	// include-ci folds symbols; the string literal keeps its case.
	if err := os.WriteFile(upper, []byte("(DEFINE FOLDED-Y \"MiXeD\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ev := NewEvaluator()
	if v := mustEvalSeq(t, ev, `(include "`+plain+`")`); !deepEqual(v, Integer(42)) {
		t.Errorf("include returns the last form's value: got %v", PrintValue(v))
	}
	if v := mustEvalSeq(t, ev, `included-x`); !deepEqual(v, Integer(40)) {
		t.Errorf("include defines in the current env: got %v", PrintValue(v))
	}
	mustEvalSeq(t, ev, `(include-ci "`+upper+`")`)
	if v := mustEvalSeq(t, ev, `folded-y`); !deepEqual(v, String("MiXeD")) {
		t.Errorf("include-ci folds symbols but not strings: got %v", PrintValue(v))
	}
	// A folded-case include leaves the un-folded spelling unbound.
	if _, err := evalExpr(`FOLDED-Y`, ev, ev.globalEnv); err == nil {
		t.Error("include-ci should not bind the original spelling")
	}
	if _, err := evalExpr(`(include "`+filepath.Join(dir, "missing.scm")+`")`, ev, ev.globalEnv); err == nil {
		t.Error("include of a missing file should error")
	}
	if _, err := evalExpr(`(include)`, ev, ev.globalEnv); err == nil {
		t.Error("include with no filenames should error")
	}
}

func TestDefineLibraryDeclarations(t *testing.T) {
	dir := t.TempDir()
	body := filepath.Join(dir, "body.scm")
	if err := os.WriteFile(body, []byte("(define (lib-double x) (* 2 x))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	decls := filepath.Join(dir, "decls.scm")
	if err := os.WriteFile(decls, []byte("(export lib-double)\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ev := NewEvaluator()
	mustEvalSeq(t, ev, `(define-library (inc lib)
	   (include-library-declarations "`+decls+`")
	   (cond-expand (r7rs (include "`+body+`")) (else (begin (define lib-double 'nope))))
	   (import (scheme base)))`)
	if v := mustEvalSeq(t, ev, `(lib-double 21)`); !deepEqual(v, Integer(42)) {
		t.Errorf("library include + cond-expand declarations: got %v", PrintValue(v))
	}
	mustEvalSeq(t, ev, `(import (prefix (inc lib) p:))`)
	if v := mustEvalSeq(t, ev, `(p:lib-double 4)`); !deepEqual(v, Integer(8)) {
		t.Errorf("import of included export: got %v", PrintValue(v))
	}

	// A library requirement in cond-expand sees session-defined libraries.
	if v := mustEvalSeq(t, ev, `(cond-expand ((library (inc lib)) 'known) (else 'unknown))`); !deepEqual(v, Symbol("known")) {
		t.Errorf("cond-expand library requirement: got %v", PrintValue(v))
	}

	for _, bad := range []string{
		`(define-library)`,
		`(define-library (bad lib) (unknown-declaration 1))`,
		`(define-library (bad lib) (export 42))`,
		`(define-library "not-a-name")`,
	} {
		if _, err := evalExpr(bad, ev, ev.globalEnv); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}
