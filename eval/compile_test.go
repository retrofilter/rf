package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func compileTop(t *testing.T, ev *Evaluator, src string) node {
	t.Helper()
	x, err := Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	c := &compiler{e: ev, root: ev.globalEnv}
	n, err := c.compile(x, scopeFromEnv(ev.globalEnv), true)
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	return n
}

func mustEval(t *testing.T, ev *Evaluator, src string) Value {
	t.Helper()
	return evalAllString(t, ev, src)
}

func evalErr(t *testing.T, ev *Evaluator, src string) string {
	t.Helper()
	exprs, err := ParseAll(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	_, err = ev.EvalAll(exprs, ev.globalEnv)
	if err == nil {
		t.Fatalf("eval %q: expected an error", src)
	}
	return err.Error()
}

func lambdaBody(t *testing.T, n node) node {
	t.Helper()
	ln, ok := n.(*lambdaNode)
	if !ok {
		t.Fatalf("expected *lambdaNode, got %T", n)
	}
	return ln.body
}

func TestCompileNodeShapes(t *testing.T) {
	ev := NewEvaluator()
	mustEval(t, ev, `(define bound 1)`)
	tests := []struct {
		src  string
		want string
	}{
		{`42`, "*eval.constNode"},
		{`"s"`, "*eval.constNode"},
		{`'x`, "*eval.constNode"},
		{`(quote (1 2))`, "*eval.constNode"},
		{`bound`, "*eval.globalRef"},
		{`unbound-yet`, "*eval.globalRef"}, // resolved lazily, still a cell ref
		{`(car '(1))`, "*eval.globalCallNode"},
		{`((lambda (x) x) 1)`, "*eval.letNode"},
		{`(let ((x 1)) x)`, "*eval.letNode"},           // the let macro expands to the same shape
		{`((lambda (x . r) x) 1 2)`, "*eval.callNode"}, // variadic: not a let frame
		{`((lambda (x) x) 1 2)`, "*eval.callNode"},     // arity mismatch: left to run time
		{`((car (list car)) '(1))`, "*eval.callNode"},
		{`(lambda (x) x)`, "*eval.lambdaNode"},
		{`(if 1 2)`, "*eval.ifNode"},
		{`(cond (#t 1))`, "*eval.ifNode"}, // macro expanded at the site
		{`(and)`, "*eval.constNode"},
		{`(or)`, "*eval.constNode"},
		{`(and 1 2)`, "*eval.andNode"},
		{`(or 1 2)`, "*eval.orNode"},
		{`(begin)`, "*eval.constNode"},
		{`(begin 1)`, "*eval.constNode"}, // a one-form body is the form
		{`(begin 1 2)`, "*eval.seqNode"},
		{`{:a 1}`, "*eval.dictNode"},
		{"`(1 2)", "*eval.constNode"}, // hole-free template folds to its datum
		{"`(1 ,bound)", "*eval.qqListNode"},
		{"`#(1 ,bound)", "*eval.qqVecNode"},
		{"`(1 ,@(list 2))", "*eval.qqListNode"},
		{`(pipe 1 (+ 1))`, "*eval.pipeNode"},
		{`(define d 1)`, "*eval.defineGlobal"},
		{`(set! bound 2)`, "*eval.setGlobal"},
		{`(set! never-defined 2)`, "*eval.setGlobal"},
		{`(define agent-allow-commands '())`, "*eval.grantGuard"},
		{`(set! agent-allow-working-dir :read)`, "*eval.grantGuard"},
		{`(define-syntax m (syntax-rules () ((_) 1)))`, "*eval.constNode"},
		{`(syntax-error "x")`, "*eval.sfNode"}, // still a fallback form
		{`()`, "*eval.errNode"},
	}
	for _, tt := range tests {
		got := fmt.Sprintf("%T", compileTop(t, ev, tt.src))
		if got != tt.want {
			t.Errorf("%s: compiled to %s, want %s", tt.src, got, tt.want)
		}
	}

	// A body expander placeholder compiles to the node it carries.
	c := &compiler{e: ev, root: ev.globalEnv}
	inner := &constNode{v: Integer(7)}
	n, err := c.compile(&compiledForm{n: inner}, scopeFromEnv(ev.globalEnv), false)
	if err != nil || n != inner {
		t.Fatalf("compiledForm: got %v, %v; want the wrapped node", n, err)
	}

	// A dotted template with a hole in the cdr is a pair builder.
	pair := &Pair{Car: Integer(1), Cdr: []Value{Symbol("unquote"), Symbol("bound")}}
	n, err = c.compile([]Value{Symbol("quasiquote"), pair}, scopeFromEnv(ev.globalEnv), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.(*qqPairNode); !ok {
		t.Fatalf("dotted quasiquote: got %T, want *qqPairNode", n)
	}
	v, err := n.eval(ev, ev.globalEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got := PrintValue(v); got != "(1 . 1)" {
		t.Fatalf("dotted quasiquote: got %s, want (1 . 1)", got)
	}
}

func TestCompileLexicalAddressing(t *testing.T) {
	ev := NewEvaluator()
	n := compileTop(t, ev, `(lambda (a) (lambda (b) (lambda (c d) (list c a d b))))`)
	body := lambdaBody(t, lambdaBody(t, lambdaBody(t, n)))
	call, ok := body.(*globalCallNode)
	if !ok {
		t.Fatalf("innermost body: got %T, want *globalCallNode", body)
	}
	if call.g.name != "list" {
		t.Fatalf("head: got %q, want list", call.g.name)
	}
	want := []node{&localRef0{idx: 0}, &localRef{depth: 2, idx: 0}, &localRef0{idx: 1}, &localRef1{idx: 0}}
	if len(call.args) != len(want) {
		t.Fatalf("args: got %d, want %d", len(call.args), len(want))
	}
	for i, a := range call.args {
		if fmt.Sprintf("%#v", a) != fmt.Sprintf("%#v", want[i]) {
			t.Errorf("arg %d: got %#v, want %#v", i, a, want[i])
		}
	}

	// A let frame is one level like any other.
	n = compileTop(t, ev, `(lambda (a) ((lambda (b) (+ a b)) 1))`)
	let, ok := lambdaBody(t, n).(*letNode)
	if !ok {
		t.Fatalf("let body: got %T", lambdaBody(t, n))
	}
	add := let.body.(*globalCallNode)
	if fmt.Sprintf("%#v %#v", add.args[0], add.args[1]) != fmt.Sprintf("%#v %#v", &localRef1{idx: 0}, &localRef0{idx: 0}) {
		t.Errorf("let body refs: got %#v %#v", add.args[0], add.args[1])
	}

	// set! addresses the same way.
	n = compileTop(t, ev, `(lambda (a) (lambda (b) (set! a b)))`)
	set, ok := lambdaBody(t, lambdaBody(t, n)).(*setLocal)
	if !ok || set.depth != 1 || set.idx != 0 {
		t.Fatalf("set! of outer local: got %#v", lambdaBody(t, lambdaBody(t, n)))
	}

	// And it all runs.
	got := mustEval(t, ev, `
		(define (f a) (lambda (b) (lambda (c d) (set! a (+ a 100)) (list c a d b))))
		(((f 1) 2) 3 4)`)
	if PrintValue(got) != "(3 101 4 2)" {
		t.Fatalf("got %s, want (3 101 4 2)", PrintValue(got))
	}
}

func TestCompileInternalDefineSlots(t *testing.T) {
	ev := NewEvaluator()
	n := compileTop(t, ev, `(lambda (p) (define y 1) (begin (define (z) y) (define w 2)) (z))`)
	ln := n.(*lambdaNode)
	if got := fmt.Sprint(ln.frame); got != "[p y z w]" {
		t.Fatalf("frame slots: got %s, want [p y z w] (params first, defines in order, begin spliced)", got)
	}
	seq, ok := ln.body.(*seqNode)
	if !ok {
		t.Fatalf("body: got %T, want *seqNode", ln.body)
	}
	dl, ok := seq.nodes[0].(*defineLocal)
	if !ok || dl.idx != 1 || dl.name != "y" {
		t.Fatalf("first define: got %#v, want defineLocal{idx:1 name:y}", seq.nodes[0])
	}
	if _, ok := seq.nodes[len(seq.nodes)-1].(*globalCallNode); ok {
		t.Fatalf("(z) must call a local slot, compiled as a global call")
	}

	got := mustEval(t, ev, `
		(define (parity n)
		  (define (ev? k) (if (= k 0) #t (od? (- k 1))))
		  (define (od? k) (if (= k 0) #f (ev? (- k 1))))
		  (list (ev? n) (od? n)))
		(parity 11)`)
	if PrintValue(got) != "(false true)" {
		t.Fatalf("mutual recursion through internal defines: got %s", PrintValue(got))
	}
	// A define reached through a chain of macros is still pre-declared.
	got = mustEval(t, ev, `
		(define-syntax def2 (syntax-rules () ((_ n v) (define n v))))
		(define-syntax def3 (syntax-rules () ((_ n v) (def2 n v))))
		(define (f) (define (g) k) (def3 k 5) (g))
		(f)`)
	if PrintValue(got) != "5" {
		t.Fatalf("macro-introduced internal define: got %s, want 5", PrintValue(got))
	}
	// An internal define shadows a global of the same name only inside.
	got = mustEval(t, ev, `
		(define shadowed 'global)
		(define (f) (define shadowed 'local) shadowed)
		(list (f) shadowed)`)
	if PrintValue(got) != "(local global)" {
		t.Fatalf("internal define shadowing: got %s", PrintValue(got))
	}
}

func TestCompileGlobalCells(t *testing.T) {
	ev := NewEvaluator()
	mustEval(t, ev, `(define g 1)`)
	ref := compileTop(t, ev, `g`).(*globalRef)
	cell := ev.globalEnv.bindings["g"]
	if ref.c == nil || ref.c != cell {
		t.Fatalf("bound global compiled without its cell (%p vs %p)", ref.c, cell)
	}
	mustEval(t, ev, `(define g 2)`)
	if ev.globalEnv.bindings["g"] != cell {
		t.Fatalf("redefinition replaced the cell instead of updating it")
	}
	if v, _ := ref.eval(ev, ev.globalEnv); v != Integer(2) {
		t.Fatalf("stale read through cell: got %v, want 2", v)
	}

	mustEval(t, ev, `(define (use-later) later)`)
	fn := ev.globalEnv.bindings["use-later"].val.(*Lambda)
	fwd, ok := fn.body.(*globalRef)
	if !ok || fwd.c != nil {
		t.Fatalf("forward reference: got %#v, want an unresolved *globalRef", fn.body)
	}
	if got := evalErr(t, ev, `(use-later)`); got != "unbound symbol: later" {
		t.Fatalf("unbound error: got %q", got)
	}
	mustEval(t, ev, `(define later 'here)`)
	if got := mustEval(t, ev, `(use-later)`); got != Symbol("here") {
		t.Fatalf("after define: got %v", got)
	}
	if fwd.c == nil || fwd.c != ev.globalEnv.bindings["later"] {
		t.Fatalf("forward reference did not cache the cell it resolved")
	}
	if got := evalErr(t, ev, `(set! never-bound 1)`); got != "unbound variable: never-bound" {
		t.Fatalf("set! unbound: got %q", got)
	}
	// set! through a cell resolved after compilation.
	mustEval(t, ev, `(define (bump) (set! late-counter (+ late-counter 1)))`)
	mustEval(t, ev, `(define late-counter 10)`)
	if got := mustEval(t, ev, `(bump) (bump) late-counter`); got != Integer(12) {
		t.Fatalf("set! through late cell: got %v, want 12", got)
	}
}

func TestCompileShadowing(t *testing.T) {
	ev := NewEvaluator()
	tests := []struct {
		src, want string
	}{
		{`((lambda (if) (if 1 2)) list)`, "(1 2)"},
		{`((lambda (when) (when 1 2)) list)`, "(1 2)"},
		{`((lambda (quote) (quote 3)) -)`, "-3"},
		{`((lambda (lambda) ((lambda 1 2) 3)) (lambda (a b) (lambda (c) (list a b c))))`, "(1 2 3)"},
		// let-bound shadow of a global function.
		{`(let ((list vector)) (list 1 2))`, "#(1 2)"},
	}
	for _, tt := range tests {
		if got := PrintValue(mustEval(t, ev, tt.src)); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.src, got, tt.want)
		}
	}

	if ev.globalEnv.formShadows != 0 {
		t.Fatalf("fresh evaluator has formShadows=%d", ev.globalEnv.formShadows)
	}
	got := mustEval(t, ev, `(define when 5) (list when (when #t 7))`)
	if PrintValue(got) != "(5 7)" {
		t.Fatalf("binding beside form: got %s, want (5 7)", PrintValue(got))
	}
	if ev.globalEnv.formShadows != 1 {
		t.Fatalf("formShadows after (define when 5): got %d, want 1", ev.globalEnv.formShadows)
	}
	if got := mustEval(t, ev, `(define (twice x) (* 2 x)) (twice 21)`); got != Integer(42) {
		t.Fatalf("global head with formShadows>0: got %v", got)
	}
}

func TestCompileErrorsAtDefinition(t *testing.T) {
	ev := NewEvaluator()
	tests := []struct {
		src, want string
	}{
		{`(define (f) (if))`, "if expects 2 or 3 arguments"},
		{`(lambda () (quote))`, "quote expects 1 argument"},
		{`(lambda () (set! 1 2))`, "set!: first argument must be a symbol"},
		{`(lambda () (set! x))`, "set!: invalid syntax"},
		{`(define)`, "define: invalid syntax"},
		{`(define x)`, "define: invalid syntax"},
		{`(define x 1 2)`, "define: invalid syntax for variable"},
		{`(define (1) 1)`, "define: expected function name in signature"},
		{`(define (f))`, "define: invalid syntax"},
		{`(define 1 2)`, "define: first argument must be a symbol"},
		{`(lambda (1) 1)`, "lambda: all parameters must be symbols"},
		{`(lambda)`, "lambda expects at least 2 arguments"},
		{`(pipe)`, "pipe expects at least 1 argument"},
		{`(pipe 1 ())`, "pipe: empty stage"},
		{`(pipe 1 5)`, "pipe: each stage must be a call form or a function name"},
		{"(lambda () `,@x)", "unquote-splicing: not in list context"},
		{`(quasiquote)`, "quasiquote expects 1 argument"},
		{`(define-syntax 5 (syntax-rules () ((_) 1)))`, "define-syntax: name must be a symbol"},
		{`(define-syntax m 5)`, "define-syntax expects a syntax-rules transformer"},
		{`(define-syntax m)`, "define-syntax expects 2 arguments"},
		{`(if #f (if) 1)`, "if expects 2 or 3 arguments"},
	}
	for _, tt := range tests {
		got := evalErr(t, ev, tt.src)
		if !strings.Contains(got, tt.want) {
			t.Errorf("%s: got %q, want it to contain %q", tt.src, got, tt.want)
		}
	}
	// The definition never took effect.
	if _, ok := ev.globalEnv.bindings["f"]; ok {
		t.Fatalf("a definition with a syntax error was bound")
	}
	if got := mustEval(t, ev, `(if #t 'live ())`); got != Symbol("live") {
		t.Fatalf("dead empty list: got %v", got)
	}
	if got := evalErr(t, ev, `()`); got != "empty list" {
		t.Fatalf("empty application: got %q", got)
	}
}

func TestCompileDynamicLevel(t *testing.T) {
	ev := NewEvaluator()
	child := NewEnvironment(ev.globalEnv)
	child.Set("q", Integer(7))
	c := &compiler{e: ev, root: ev.globalEnv}
	x, _ := Parse(`(+ q 1)`)
	n, err := c.compile(x, scopeFromEnv(child), true)
	if err != nil {
		t.Fatal(err)
	}
	call := n.(*globalCallNode)
	if _, ok := call.args[0].(*dynRef); !ok {
		t.Fatalf("reference through a dynamic level: got %T, want *dynRef", call.args[0])
	}
	if v, err := ev.Eval(x, child); err != nil || v != Integer(8) {
		t.Fatalf("(+ q 1) in child: got %v, %v", v, err)
	}

	for _, src := range []string{`(define r 2)`, `(set! q 9)`} {
		x, _ := Parse(src)
		if _, err := ev.Eval(x, child); err != nil {
			t.Fatalf("%s: %v", src, err)
		}
	}
	if _, leaked := ev.globalEnv.bindings["r"]; leaked {
		t.Fatalf("define in a child level landed in the global environment")
	}
	if v, _ := child.Lookup("r"); v != Integer(2) {
		t.Fatalf("define in child: got %v, want 2", v)
	}
	if v, _ := child.Lookup("q"); v != Integer(9) {
		t.Fatalf("set! in child: got %v, want 9", v)
	}
	// A reference the level doesn't hold still reaches the global.
	x, _ = Parse(`(car '(3))`)
	if v, err := ev.Eval(x, child); err != nil || v != Integer(3) {
		t.Fatalf("global through child: got %v, %v", v, err)
	}
	x, _ = Parse(`later-q`)
	n, err = c.compile(x, scopeFromEnv(child), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.(*dynRef); !ok {
		t.Fatalf("unbound name at a dynamic level: got %T, want *dynRef", n)
	}
	child.Set("later-q", Integer(5))
	if v, _ := n.eval(ev, child); v != Integer(5) {
		t.Fatalf("dynRef after late define: got %v", v)
	}

	frame := &Environment{parent: ev.globalEnv, paramNames: []Symbol{"p"}, vals: []Value{Integer(1)}}
	x, _ = Parse(`(list p car)`)
	n, err = c.compile(x, scopeFromEnv(frame), true)
	if err != nil {
		t.Fatal(err)
	}
	call = n.(*globalCallNode)
	if _, ok := call.args[0].(*localRef0); !ok {
		t.Errorf("param through a live frame: got %T, want *localRef0", call.args[0])
	}
	if g, ok := call.args[1].(*globalRef); !ok || g.c == nil {
		t.Errorf("global through a live frame: got %#v, want a *globalRef with its cell", call.args[1])
	}
}

func TestCompileCache(t *testing.T) {
	ev := NewEvaluator()
	frame := &Environment{parent: ev.globalEnv, paramNames: []Symbol{"x"}, vals: []Value{Integer(1)}}
	x, _ := Parse(`(+ x 1)`)
	n1, err := ev.compileIn(x, frame, true)
	if err != nil {
		t.Fatal(err)
	}
	n2, _ := ev.compileIn(x, frame, true)
	if n1 != n2 {
		t.Fatalf("same site, same scope: compiled twice")
	}
	if n3, _ := ev.compileIn(x, frame, false); n3 == n1 {
		t.Fatalf("tail flag is part of the key")
	}
	// A different AST slice with equal content is a different site.
	y, _ := Parse(`(+ x 1)`)
	if n4, _ := ev.compileIn(y, frame, true); n4 == n1 {
		t.Fatalf("distinct AST slices share a cache entry")
	}
	// The scope signature: a map binding added to the frame invalidates.
	frame.Set("y", Integer(2))
	n5, _ := ev.compileIn(x, frame, true)
	if n5 == n1 {
		t.Fatalf("binding added to the frame did not miss the cache")
	}
	if n6, _ := ev.compileIn(x, frame, true); n6 != n5 {
		t.Fatalf("post-invalidation entry not reused")
	}
	// The hit is still correct: the cached node runs against the frame.
	if v, err := n5.eval(ev, frame); err != nil || v != Integer(2) {
		t.Fatalf("cached node: got %v, %v", v, err)
	}
	// Top level: fresh each time.
	g1, _ := ev.compileIn(x, ev.globalEnv, true)
	g2, _ := ev.compileIn(x, ev.globalEnv, true)
	if g1 == g2 {
		t.Fatalf("top-level forms must not be cached")
	}
	// Non-list forms bypass the cache.
	sym := Symbol("x")
	s1, _ := ev.compileIn(sym, frame, true)
	s2, _ := ev.compileIn(sym, frame, true)
	if s1 == s2 {
		t.Fatalf("symbol form was cached")
	}
	// A compile error is not cached.
	bad, _ := Parse(`(if)`)
	if _, err := ev.compileIn(bad, frame, true); err == nil {
		t.Fatalf("expected a compile error")
	}
	if _, ok := ev.ccache.m[astKey{ptr: &bad.([]Value)[0], n: 1, tail: true}]; ok {
		t.Fatalf("failed compile left a cache entry")
	}
}

func TestCompileCacheCap(t *testing.T) {
	cc := &compileCache{m: make(map[astKey][]compiledEntry)}
	asts := make([][]Value, compileCacheCap+1)
	for i := range asts {
		asts[i] = []Value{Symbol("x")}
		cc.store(astKey{ptr: &asts[i][0], n: 1}, compiledEntry{ast: asts[i]})
		if i < compileCacheCap && cc.n != i+1 {
			t.Fatalf("after %d stores n=%d", i+1, cc.n)
		}
	}
	if cc.n != 1 || len(cc.m) != 1 {
		t.Fatalf("cache past cap: n=%d entries=%d, want the map dropped and restarted", cc.n, len(cc.m))
	}
	if _, ok := cc.m[astKey{ptr: &asts[compileCacheCap][0], n: 1}]; !ok {
		t.Fatalf("the store that overflowed the cap was lost")
	}
}

func TestCompileScopeSig(t *testing.T) {
	ev := NewEvaluator()
	frame := &Environment{parent: ev.globalEnv, paramNames: []Symbol{"a", "b"}, vals: make([]Value, 2)}
	inner := &Environment{parent: frame}
	sig := scopeSig(inner, nil)
	if len(sig) != 3 {
		t.Fatalf("chain of 3 gives %d levels", len(sig))
	}
	if sig[0].nnames != 0 || sig[1].nnames != 2 || sig[1].names != &frame.paramNames[0] {
		t.Fatalf("level signatures: %+v", sig[:2])
	}
	if !sigEqual(sig, scopeSig(inner, nil)) {
		t.Fatalf("same chain, unequal signatures")
	}
	if sigEqual(sig, scopeSig(frame, nil)) {
		t.Fatalf("different lengths compare equal")
	}
	inner.Set("z", Integer(1))
	if sigEqual(sig, scopeSig(inner, nil)) {
		t.Fatalf("a new map binding did not change the signature")
	}
	before := scopeSig(inner, nil)
	inner.SetMacro("m", func(args []Value) (Value, error) { return nil, nil })
	if sigEqual(before, scopeSig(inner, nil)) {
		t.Fatalf("a new form did not change the signature")
	}
	// A stack buffer is appended to in place when it has room.
	var buf [8]levelSig
	out := scopeSig(inner, buf[:0])
	if &out[0] != &buf[0] {
		t.Fatalf("scopeSig reallocated a buffer with room")
	}
}

func TestCompileTailPosition(t *testing.T) {
	ev := NewEvaluator()
	// Frames at the base case stay constant iff every hop is a tail call.
	ev.globalEnv.Set("frames", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		return Integer(runtime.Callers(0, make([]uintptr, 1<<16))), nil
	}))
	growth := func(def string) Integer {
		t.Helper()
		got := mustEval(t, ev, def+` (- (l 400) (l 4))`)
		n, ok := got.(Integer)
		if !ok {
			t.Fatalf("%s: got %v", def, got)
		}
		return n
	}
	if growth(`(define (l n) (if (= n 0) (frames) (+ 0 (l (- n 1)))))`) <= 0 {
		t.Fatal("probe cannot see a non-tail call's stack growth")
	}
	loops := []string{
		`(define (l n) (or (and (= n 0) (frames)) (l (- n 1))))`,
		`(define (l n) (and (>= n 0) (if (= n 0) (frames) (l (- n 1)))))`,
		`(define (l n) (begin 'side (if (= n 0) (frames) (l (- n 1)))))`,
		`(define (l n) (let ((m (- n 1))) (if (< m 0) (frames) (l m))))`,
		`(define (l n) (let* ((m (- n 1)) (k m)) (if (< k 0) (frames) (l k))))`,
		`(define (l n) (cond ((= n 0) (frames)) (else (l (- n 1)))))`,
		`(define (l n) (case n ((0) (frames)) (else (l (- n 1)))))`,
		`(define (l n) (when (>= n 0) (if (= n 0) (frames) (l (- n 1)))))`,
		`(define (l n) (if (= n 0) (frames) (apply l (list (- n 1)))))`,
		`(define (l n) (define (step) (l (- n 1))) (if (= n 0) (frames) (step)))`,
		`(define (l n) (cond-expand (else (if (= n 0) (frames) (l (- n 1))))))`,
	}
	for _, def := range loops {
		if n := growth(def); n != 0 {
			t.Errorf("%s: stack grew by %d frames", def, n)
		}
	}
	got := mustEval(t, ev, `(list (cond-expand (else 41)))`)
	if PrintValue(got) != "(41)" {
		t.Fatalf("fallback form in argument position: got %s", PrintValue(got))
	}
}

func TestCompilePipe(t *testing.T) {
	ev := NewEvaluator()
	tests := []struct {
		src, want string
	}{
		{`(pipe 3)`, "3"},
		{`(pipe 3 -)`, "-3"},                         // symbol stage: (- _)
		{`(pipe 3 (- 10))`, "7"},                     // no _: appended last
		{`(pipe 3 (- _ 10))`, "-7"},                  // explicit _
		{`(pipe 3 (list _ (+ _ 1)))`, "(3 4)"},       // _ anywhere in the stage
		{`(pipe 3 (+ 1) (* 2) (list _))`, "(8)"},     // chained
		{`((lambda (_) (pipe 1 (+ _ _))) 100)`, "2"}, // stage _ shadows an outer _
		{`(pipe '(1 2 3) (map (lambda (x) (* x x))))`, "(1 4 9)"},
		{`(let ((y 10)) (pipe 1 (+ y)))`, "11"}, // stage sees the enclosing scope
	}
	for _, tt := range tests {
		if got := PrintValue(mustEval(t, ev, tt.src)); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.src, got, tt.want)
		}
	}
	if got := evalErr(t, ev, `(pipe 1 (car))`); !strings.Contains(got, "car") {
		t.Fatalf("stage error not propagated: %q", got)
	}
	// The stage frame is one slot named _, so _ isn't visible after.
	if got := evalErr(t, ev, `(begin (pipe 1 (+ 1)) _)`); got != "unbound symbol: _" {
		t.Fatalf("_ leaked out of the pipe: %q", got)
	}
	// A failed stage closes the stream it was handed.
	path := filepath.Join(t.TempDir(), "lines.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustEval(t, ev, fmt.Sprintf(`(define s (cat %q))`, path))
	if got := evalErr(t, ev, `(pipe s (error "boom"))`); !strings.Contains(got, "boom") {
		t.Fatalf("stream stage error: %q", got)
	}
	st := ev.globalEnv.bindings["s"].val.(*Stream)
	if _, _, err := st.Next(); err != ErrStreamConsumed {
		t.Fatalf("stream abandoned by a failed stage not closed: %v", err)
	}
}

func TestCompileQuasiquote(t *testing.T) {
	ev := NewEvaluator()
	mustEval(t, ev, `(define x 4) (define xs '(2 3))`)
	tests := []struct {
		src, want string
	}{
		{"`(1 ,x)", "(1 4)"},
		{"`(1 ,@xs 5)", "(1 2 3 5)"},
		{"`(1 ,@'())", "(1)"},
		{"`#(1 ,x ,@xs)", "#(1 4 2 3)"},
		{"`(a (b ,x) c)", "(a (b 4) c)"},
		{"`(1 `(2 ,(3 ,x)))", "(1 (quasiquote (2 (unquote (3 4)))))"},
		{"`(1 `(2 ,,x))", "(1 (quasiquote (2 (unquote 4))))"},
		{"`(1 `(2 ,@,xs))", "(1 (quasiquote (2 (unquote-splicing (2 3)))))"},
		{"`(1 `(2 ,x))", "(1 (quasiquote (2 (unquote x))))"}, // depth 2: not a hole
		{"`,x", "4"},
		{"`(1 . 2)", "(1 . 2)"},
		{"`(1 . ,x)", "(1 . 4)"},
		{"`(1 2 . ,x)", "(1 2 . 4)"},
		{"`(1 . ,xs)", "(1 2 3)"}, // proper-list tail: proper result
		{"`(1 ,@xs . ,x)", "(1 2 3 . 4)"},
		{"``(1 . ,,x)", "(quasiquote (1 unquote 4))"}, // hole two deep
		{"``(1 . ,x)", "(quasiquote (1 unquote x))"},  // depth 2: not a hole
		{"(quasiquote (unquote x))", "4"},             // long spelling
	}
	for _, tt := range tests {
		got := mustEval(t, ev, `(equal? `+tt.src+` '`+tt.want+`)`)
		if got != true {
			t.Errorf("%s: got %s, want %s", tt.src, PrintValue(mustEval(t, ev, tt.src)), tt.want)
		}
	}
	if got := evalErr(t, ev, "`(1 ,@x)"); got != "unquote-splicing expects a list" {
		t.Fatalf("splice non-list: %q", got)
	}
	// A hole-free subtemplate is folded to a constant inside a builder.
	n := compileTop(t, ev, "`((1 2) ,x #(3))").(*qqListNode)
	if !isConst(n.items[0].n) || isConst(n.items[1].n) || !isConst(n.items[2].n) {
		t.Fatalf("constant folding inside a builder: %#v", n.items)
	}
	// A folded template is the datum itself, shared across evaluations.
	mustEval(t, ev, "(define (tpl) `(1 (2 3)))")
	if got := mustEval(t, ev, `(eq? (tpl) (tpl))`); got != true {
		t.Fatalf("hole-free template rebuilt per evaluation")
	}
	// A template with a hole is fresh each time.
	mustEval(t, ev, "(define (tpl2) `(1 ,x))")
	if got := mustEval(t, ev, `(eq? (tpl2) (tpl2))`); got != false {
		t.Fatalf("template with a hole shared its list across evaluations")
	}
}

func TestCompileApplyErrors(t *testing.T) {
	ev := NewEvaluator()
	tests := []struct {
		src, want string
	}{
		{`((lambda (x) x))`, "lambda: expected 1 arguments, got 0"},
		{`((lambda (x y) x) 1 2 3)`, "lambda: expected 2 arguments, got 3"},
		{`((lambda (x . r) x))`, "lambda: expected at least 1 arguments, got 0"},
		{`(define (f a b) a) (f 1)`, "lambda: expected 2 arguments, got 1"},
		{`(5 1)`, "not a function: 5"},
		{`("str")`, `not a function: "str"`},
		{`({:a 1} :a)`, "dictionaries aren't callable; index with (get :key dict)"},
		{`((make-parameter 1) 2)`, "a parameter object takes no arguments"},
		{`((case-lambda ((a) a) ((a b) b)) 1 2 3)`, "case-lambda: no clause matches 3 arguments"},
		{`((lambda (x) x) (car '()) 2)`, "lambda: expected 1 arguments, got 2"},
	}
	for _, tt := range tests {
		got := evalErr(t, ev, tt.src)
		if !strings.Contains(got, tt.want) {
			t.Errorf("%s: got %q, want it to contain %q", tt.src, got, tt.want)
		}
	}
	// The applyNodes dispatch for the non-lambda callables.
	good := []struct {
		src, want string
	}{
		{`((make-parameter 1))`, "1"},
		{`((lambda args args) 1 2 3)`, "(1 2 3)"},
		{`((lambda (a . r) (list a r)) 1)`, "(1 ())"},
		{`((case-lambda ((a) a) ((a b) b)) 1 2)`, "2"},
		{`(call/cc (lambda (k) (+ 1 (k 41))))`, "41"},
		{`(apply (lambda (a b . r) (list a b r)) 1 '(2 3 4))`, "(1 2 (3 4))"},
	}
	for _, tt := range good {
		if got := PrintValue(mustEval(t, ev, tt.src)); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.src, got, tt.want)
		}
	}
}

func TestCompileGrantGuard(t *testing.T) {
	ev := NewEvaluator()
	mustEval(t, ev, `
		(define agent-allow-commands '("go test"))
		(define (widen) (set! agent-allow-commands '("rm")))
		(define (grant-dir) (define agent-allow-working-dir :write) 'ok)`)
	ev.SetCaller(CallerAssistant)
	defer ev.SetCaller(CallerUser)
	for _, src := range []string{
		`(define agent-allow-commands '())`,
		`(set! agent-allow-commands '())`,
		`(widen)`,
		`(grant-dir)`,
		`(define agent-allow-working-dir :read)`,
	} {
		got := evalErr(t, ev, src)
		if !strings.Contains(got, "can only be bound by the user") {
			t.Errorf("%s as assistant: got %q", src, got)
		}
	}
	if got := PrintValue(mustEval(t, ev, `agent-allow-commands`)); got != `("go test")` {
		t.Fatalf("grant changed by a refused write: %s", got)
	}
	// Reading is fine; only binding is guarded.
	if got := mustEval(t, ev, `(length agent-allow-commands)`); got != Integer(1) {
		t.Fatalf("assistant read of the grant: got %v", got)
	}
	ev.SetCaller(CallerUser)
	if got := mustEval(t, ev, `(widen) agent-allow-commands`); PrintValue(got) != `("rm")` {
		t.Fatalf("user through the wrapper: got %s", PrintValue(got))
	}
}

func TestCompileBodyDefineSyntax(t *testing.T) {
	ev := NewEvaluator()
	got := mustEval(t, ev, `
		(define (f y)
		  (define-syntax twice (syntax-rules () ((_ e) (* 2 e))))
		  (define z (twice y))
		  (twice z))
		(f 5)`)
	if got != Integer(20) {
		t.Fatalf("body-level macro: got %v, want 20", got)
	}
	if _, leaked := ev.globalEnv.forms["twice"]; leaked {
		t.Fatalf("body-level define-syntax registered globally")
	}
	if got := evalErr(t, ev, `(twice 1)`); got != "unbound symbol: twice" {
		t.Fatalf("body macro visible outside: %q", got)
	}
	got = mustEval(t, ev, `
		(define (g y)
		  (define-syntax inc (syntax-rules () ((_ e) (+ e 1))))
		  (with-approval (inc y)))
		(g 41)`)
	if got != Integer(42) {
		t.Fatalf("local macro through a fallback form: got %v, want 42", got)
	}
	// A transformer bound at top level, referenced by name from a body.
	got = mustEval(t, ev, `
		(define sr (syntax-rules () ((_ e) (list e e))))
		(define (h) (define-syntax dup sr) (dup 1))
		(h)`)
	if PrintValue(got) != "(1 1)" {
		t.Fatalf("named transformer in a body: got %s", PrintValue(got))
	}
}

func TestCompileMarkedGlobalNames(t *testing.T) {
	ev := NewEvaluator()
	got := mustEval(t, ev, `
		(define-syntax defcounter
		  (syntax-rules ()
		    ((_ name) (begin (define count 0)
		                     (define (name) (set! count (+ count 1)) count)))))
		(defcounter tick)
		(tick) (tick)`)
	if got != Integer(2) {
		t.Fatalf("renamed global through the expansion: got %v, want 2", got)
	}
	if got := evalErr(t, ev, `count`); got != "unbound symbol: count" {
		t.Fatalf("hygienic global leaked under its base name: %q", got)
	}
	got = mustEval(t, ev, `
		(define-syntax call-helper (syntax-rules () ((_) (helper))))
		(define (use) (call-helper))
		(define (helper) 'ok)
		(use)`)
	if got != Symbol("ok") {
		t.Fatalf("macro reference to a later global: got %v", got)
	}
}

func TestDefineParts(t *testing.T) {
	parse := func(src string) []Value {
		x, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		return x.([]Value)[1:]
	}
	tests := []struct {
		src     string
		name    string
		formals string
		nbody   int
		ok      bool
	}{
		{`(define x 1)`, "x", "", 0, true},
		{`(define (f a b) a b)`, "f", "(a b)", 2, true},
		{`(define (f . args) args)`, "f", "args", 1, true},
		{`(define (f a . rest) a)`, "f", "(a . rest)", 1, true},
		{`(define)`, "", "", 0, false},
		{`(define () 1)`, "", "", 0, false},
		{`(define (1 a) 1)`, "", "", 0, false},
		{`(define ((f)) 1)`, "", "", 0, false},
		{`(define "s" 1)`, "", "", 0, false},
	}
	for _, tt := range tests {
		name, formals, body, ok := defineParts(parse(tt.src))
		if ok != tt.ok || name != tt.name || len(body) != tt.nbody {
			t.Errorf("%s: got (%q, %d body forms, ok=%v), want (%q, %d, %v)", tt.src, name, len(body), ok, tt.name, tt.nbody, tt.ok)
			continue
		}
		if tt.ok && tt.formals != "" && PrintValue(formals) != tt.formals {
			t.Errorf("%s: formals %s, want %s", tt.src, PrintValue(formals), tt.formals)
		}
		if tt.ok && tt.formals == "" && formals != nil {
			t.Errorf("%s: variable define has formals %v", tt.src, formals)
		}
	}
}

func TestCompileLetFrameNotCaptured(t *testing.T) {
	ev := NewEvaluator()
	n := compileTop(t, ev, `(lambda (a) (let ((b (+ a 1))) (* a b)))`)
	let := lambdaBody(t, n).(*letNode)
	if !let.tail || len(let.inits) != 1 || fmt.Sprint(let.names) != "[b]" {
		t.Fatalf("let node: %+v", let)
	}
	frame := ev.acquireFrame(ev.globalEnv, []Symbol{"a"}, 1)
	frame.vals[0] = Integer(2)
	if v, err := ev.run(let, frame); err != nil || v != Integer(6) {
		t.Fatalf("let over a frame: got %v, %v", v, err)
	}
	if frame.captured {
		t.Fatalf("a let body without closures capture-marked the enclosing frame")
	}
	// A closure created in the let body marks both frames.
	n = compileTop(t, ev, `(lambda (a) (let ((b 1)) (lambda () (+ a b))))`)
	let = lambdaBody(t, n).(*letNode)
	frame = ev.acquireFrame(ev.globalEnv, []Symbol{"a"}, 1)
	frame.vals[0] = Integer(2)
	v, err := ev.run(let, frame)
	if err != nil {
		t.Fatal(err)
	}
	l := v.(*Lambda)
	if !l.env.captured || !frame.captured {
		t.Fatalf("closure escaping a let left frames unmarked (let=%v outer=%v)", l.env.captured, frame.captured)
	}
	if got, _ := ev.applyLambda(l, nil); got != Integer(3) {
		t.Fatalf("escaped closure: got %v, want 3", got)
	}
}
