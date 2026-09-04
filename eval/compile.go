package eval

import (
	"errors"
	"fmt"
)

type cell struct {
	val Value
}

type node interface {
	eval(e *Evaluator, fr *Environment) (Value, error)
}

type compileFunc func(c *compiler, form []Value, sc *scope, tail bool) (node, error)

type compiledForm struct{ n node }

type scope struct {
	parent *scope
	names  []Symbol
	frame  bool
	static bool
	env    *Environment
	dyn    map[string]bool
	shadow *Environment
}

func (s *scope) isGlobal() bool { return s.env != nil && s.env.parent == nil }

func (s *scope) dynamic() bool {
	if s.env == nil || s.env.parent == nil {
		return false
	}
	return !s.frame || len(s.env.bindings) > 0
}

func (s *scope) slotFor(name string) int {
	for i, p := range s.names {
		if string(p) == name {
			return i
		}
	}
	s.names = append(s.names, Symbol(name))
	return len(s.names) - 1
}

func (s *scope) declare(name string) {
	switch {
	case s.static:
		s.slotFor(name)
	case s.env != nil && s.env.parent != nil:
		if s.dyn == nil {
			s.dyn = make(map[string]bool)
		}
		s.dyn[name] = true
	}
}

func newStaticScope(parent *scope, names []Symbol) *scope {
	own := make([]Symbol, len(names), len(names)+2)
	copy(own, names)
	return &scope{parent: parent, names: own, frame: true, static: true}
}

func scopeFromEnv(env *Environment) *scope {
	if env == nil {
		return nil
	}
	s := &scope{parent: scopeFromEnv(env.parent), env: env, shadow: env.shadow}
	if env.parent != nil && env.paramNames != nil {
		s.frame = true
		s.names = env.paramNames
	}
	return s
}

func (s *scope) globalLevel() *scope {
	for ; s != nil; s = s.parent {
		if s.parent == nil {
			return s
		}
	}
	return nil
}

func (c *compiler) envFor(s *scope) *Environment {
	if s.env != nil {
		return s.env
	}
	if s.shadow == nil {
		s.shadow = &Environment{parent: c.root, captured: true}
	}
	return s.shadow
}

type bindKind int

const (
	bindNone   bindKind = iota // unbound at compile time: a global, resolved lazily
	bindLocal                  // frame slot at (depth, idx)
	bindGlobal                 // global cell
	bindDyn                    // runtime lookup by name from the current frame
	bindDynFB                  // runtime lookup by name in a captured fallback env
	bindForm                   // special form / macro (head position only)
)

type binding struct {
	kind  bindKind
	depth int
	idx   int
	name  string
	cell  *cell
	entry formEntry
	fb    *Environment
	names []string
}

func (c *compiler) lookup(sc *scope, name string, head bool) binding {
	return c.lookupFrom(sc, sc, 0, name, head)
}

func (c *compiler) lookupFrom(sc, start *scope, startDepth int, name string, head bool) binding {
	depth := startDepth
	anyDyn := false
	for s := start; s != nil; s = s.parent {
		if s.frame {
			for i, p := range s.names {
				if string(p) == name {
					return binding{kind: bindLocal, depth: depth, idx: i, name: name}
				}
			}
		}
		if head && s.shadow != nil {
			if entry, ok := s.shadow.forms[name]; ok {
				return binding{kind: bindForm, entry: entry, name: name}
			}
		}
		if s.dyn != nil && s.dyn[name] {
			return binding{kind: bindDyn, name: name}
		}
		if env := s.env; env != nil {
			global := env.parent == nil
			probeBinding := func() (binding, bool) {
				if cl, ok := env.bindings[name]; ok {
					if global {
						return binding{kind: bindGlobal, cell: cl, name: name}, true
					}
					return binding{kind: bindDyn, name: name}, true
				}
				return binding{}, false
			}
			probeForm := func() (binding, bool) {
				if !head {
					return binding{}, false
				}
				if entry, ok := env.forms[name]; ok {
					return binding{kind: bindForm, entry: entry, name: name}, true
				}
				return binding{}, false
			}
			if env.formShadows == 0 {
				if b, ok := probeBinding(); ok {
					return b
				}
				if b, ok := probeForm(); ok {
					return b
				}
			} else {
				if b, ok := probeForm(); ok {
					return b
				}
				if b, ok := probeBinding(); ok {
					return b
				}
			}
		}
		if s.dynamic() {
			anyDyn = true
		}
		depth++
	}
	if orig, envID, ok := unmark(name); ok {
		if envID == 0 {
			g := sc.globalLevel()
			b := c.lookupFrom(sc, g, depthOf(sc, g), orig, head)
			if b.kind == bindNone {
				b.names = append([]string{name}, b.names...)
			}
			return b
		}
		if fb := c.root.macroEnvs[envID]; fb != nil {
			for s, d := sc, 0; s != nil; s, d = s.parent, d+1 {
				if s.env == fb || s.shadow == fb {
					return c.lookupFrom(sc, s, d, orig, head)
				}
			}
			return binding{kind: bindDynFB, fb: fb, name: orig}
		}
		return binding{kind: bindNone, name: orig}
	}
	if anyDyn {
		return binding{kind: bindDyn, name: name}
	}
	return binding{kind: bindNone, name: name, names: []string{name}}
}

func depthOf(sc, ancestor *scope) int {
	d := 0
	for s := sc; s != nil && s != ancestor; s = s.parent {
		d++
	}
	return d
}

type compiler struct {
	e             *Evaluator
	root          *Environment
	expansions    int
	expandedNodes int
	sizeScratch   []Value
}

const (
	maxExpansions     = 10_000
	maxExpansionNodes = 1_000_000
)

func (c *compiler) expandStep(args []Value) error {
	if interrupted.Load() {
		return ErrInterrupted
	}
	c.expansions++
	c.expandedNodes += c.sizeAtMost(args, maxExpansionNodes-c.expandedNodes+1)
	if c.expansions > maxExpansions || c.expandedNodes > maxExpansionNodes {
		return fmt.Errorf("macro expansion exceeded %d steps or %d nodes compiling one form — a macro recursing on a runtime value expands forever at compile time; bound the recursion syntactically (e.g. on an ellipsis pattern)", maxExpansions, maxExpansionNodes)
	}
	return nil
}

func (c *compiler) sizeAtMost(vs []Value, budget int) int {
	work := append(c.sizeScratch[:0], vs...)
	n := 0
	for len(work) > 0 && n < budget {
		v := work[len(work)-1]
		work = work[:len(work)-1]
		n++
		switch t := v.(type) {
		case []Value:
			work = append(work, t...)
		case *Pair:
			work = append(work, t.Car, t.Cdr)
		case Vector:
			work = append(work, t...)
		}
	}
	c.sizeScratch = work[:0]
	return n
}

func (e *Evaluator) compileIn(expr Value, env *Environment, tail bool) (node, error) {
	list, ok := expr.([]Value)
	if !ok || len(list) == 0 || env.parent == nil || e.ccache == nil {
		c := &compiler{e: e, root: rootOf(env)}
		return c.compile(expr, scopeFromEnv(env), tail)
	}
	key := astKey{ptr: &list[0], n: len(list), tail: tail}
	var buf [8]levelSig
	sig := scopeSig(env, buf[:0])
	for _, ent := range e.ccache.m[key] {
		if sigEqual(ent.sig, sig) {
			return ent.n, nil
		}
	}
	c := &compiler{e: e, root: rootOf(env)}
	n, err := c.compile(expr, scopeFromEnv(env), tail)
	if err != nil {
		return nil, err
	}
	e.ccache.store(key, compiledEntry{ast: list, sig: append([]levelSig(nil), sig...), n: n})
	return n, nil
}

type compileCache struct {
	m map[astKey][]compiledEntry
	n int
}

const compileCacheCap = 4096

type astKey struct {
	ptr  *Value
	n    int
	tail bool
}

type compiledEntry struct {
	ast []Value
	sig []levelSig
	n   node
}

type levelSig struct {
	names  *Symbol
	nnames int
	nbind  int
	nforms int
	shadow *Environment
}

func scopeSig(env *Environment, sig []levelSig) []levelSig {
	for e := env; e != nil; e = e.parent {
		l := levelSig{nnames: len(e.paramNames), nbind: len(e.bindings), nforms: len(e.forms), shadow: e.shadow}
		if len(e.paramNames) > 0 {
			l.names = &e.paramNames[0]
		}
		sig = append(sig, l)
	}
	return sig
}

func sigEqual(a, b []levelSig) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (cc *compileCache) store(key astKey, ent compiledEntry) {
	if cc.n >= compileCacheCap {
		cc.m = make(map[astKey][]compiledEntry)
		cc.n = 0
	}
	cc.m[key] = append(cc.m[key], ent)
	cc.n++
}

func (c *compiler) compile(x Value, sc *scope, tail bool) (node, error) {
	switch v := x.(type) {
	case Symbol:
		return c.compileRef(sc, string(v))
	case []Value:
		return c.compileList(v, sc, tail)
	case Dictionary:
		n := &dictNode{keys: make([]string, 0, len(v)), vals: make([]node, 0, len(v))}
		for k, val := range v {
			vn, err := c.compile(val, sc, false)
			if err != nil {
				return nil, err
			}
			n.keys = append(n.keys, k)
			n.vals = append(n.vals, vn)
		}
		return n, nil
	case *compiledForm:
		return v.n, nil
	default:
		return &constNode{v: x}, nil
	}
}

func (c *compiler) compileRef(sc *scope, name string) (node, error) {
	b := c.lookup(sc, name, false)
	return c.refNode(b), nil
}

func (c *compiler) refNode(b binding) node {
	switch b.kind {
	case bindLocal:
		switch b.depth {
		case 0:
			return &localRef0{idx: b.idx}
		case 1:
			return &localRef1{idx: b.idx}
		}
		return &localRef{depth: b.depth, idx: b.idx}
	case bindGlobal:
		return &globalRef{name: b.name, c: b.cell, root: c.root}
	case bindDyn:
		return &dynRef{name: b.name}
	case bindDynFB:
		return &fbRef{env: b.fb, name: b.name}
	}
	return &globalRef{name: b.name, names: b.names, root: c.root}
}

func (c *compiler) compileList(list []Value, sc *scope, tail bool) (node, error) {
	if len(list) == 0 {
		return &errNode{err: errors.New("empty list")}, nil
	}
	var fn node
	switch head := list[0].(type) {
	case Symbol:
		b := c.lookup(sc, string(head), true)
		if b.kind == bindForm {
			switch {
			case b.entry.cf != nil:
				return b.entry.cf(c, list, sc, tail)
			case b.entry.sf != nil:
				return &sfNode{sf: b.entry.sf, args: list[1:], tail: tail}, nil
			case b.entry.mac != nil:
				if err := c.expandStep(list[1:]); err != nil {
					return nil, err
				}
				expanded, err := b.entry.mac(list[1:])
				if err != nil {
					return nil, err
				}
				return c.compile(expanded, sc, tail)
			}
		}
		fn = c.refNode(b)
	case []Value:
		if len(head) >= 3 && c.isForm(sc, head[0], "lambda") {
			if n, ok, err := c.compileLet(head[1], head[2:], list[1:], sc, tail); ok || err != nil {
				return n, err
			}
		}
		var err error
		fn, err = c.compile(head, sc, false)
		if err != nil {
			return nil, err
		}
	default:
		var err error
		fn, err = c.compile(head, sc, false)
		if err != nil {
			return nil, err
		}
	}
	args := make([]node, len(list)-1)
	for i, a := range list[1:] {
		n, err := c.compile(a, sc, false)
		if err != nil {
			return nil, err
		}
		args[i] = n
	}
	if g, ok := fn.(*globalRef); ok {
		return &globalCallNode{g: g, args: args, tail: tail}, nil
	}
	return &callNode{fn: fn, args: args, tail: tail}, nil
}

func (c *compiler) isForm(sc *scope, v Value, tag string) bool {
	s, ok := v.(Symbol)
	if !ok {
		return false
	}
	b := c.lookup(sc, string(s), true)
	return b.kind == bindForm && b.entry.tag == tag
}

func (c *compiler) compileLet(formals Value, body []Value, inits []Value, sc *scope, tail bool) (node, bool, error) {
	params, rest, err := parseFormals(formals)
	if err != nil || rest != "" || len(params) != len(inits) {
		return nil, false, nil
	}
	initNodes := make([]node, len(inits))
	for i, x := range inits {
		n, err := c.compile(x, sc, false)
		if err != nil {
			return nil, true, err
		}
		initNodes[i] = n
	}
	child := newStaticScope(sc, params)
	bodyNode, err := c.compileBody(body, child, tail)
	if err != nil {
		return nil, true, err
	}
	return &letNode{names: child.names, shadow: child.shadow, inits: initNodes, body: bodyNode, tail: tail}, true, nil
}

func (c *compiler) compileBody(forms []Value, sc *scope, tail bool) (node, error) {
	forms, err := c.expandBody(forms, sc)
	if err != nil {
		return nil, err
	}
	if len(forms) == 0 {
		return &constNode{v: nil}, nil
	}
	if len(forms) == 1 {
		return c.compile(forms[0], sc, tail)
	}
	nodes := make([]node, len(forms))
	for i, f := range forms {
		n, err := c.compile(f, sc, tail && i == len(forms)-1)
		if err != nil {
			return nil, err
		}
		nodes[i] = n
	}
	return &seqNode{nodes: nodes}, nil
}

func (c *compiler) expandBody(forms []Value, sc *scope) ([]Value, error) {
	out := make([]Value, 0, len(forms))
	for _, f := range forms {
		f, err := c.expandHead(f, sc)
		if err != nil {
			return nil, err
		}
		if list, ok := f.([]Value); ok && len(list) > 0 {
			if sym, ok := list[0].(Symbol); ok {
				b := c.lookup(sc, string(sym), true)
				if b.kind == bindForm && b.entry.cf != nil {
					switch b.entry.tag {
					case "begin":
						sub, err := c.expandBody(list[1:], sc)
						if err != nil {
							return nil, err
						}
						out = append(out, sub...)
						continue
					case "define-syntax":
						n, err := b.entry.cf(c, list, sc, false)
						if err != nil {
							return nil, err
						}
						out = append(out, &compiledForm{n: n})
						continue
					case "define":
						if name, _, _, ok := defineParts(list[1:]); ok {
							sc.declare(name)
						}
					case "define-values", "define-record-type":
						for _, name := range formDefines(b.entry.tag, list[1:]) {
							sc.declare(name)
						}
					}
				}
			}
		}
		out = append(out, f)
	}
	return out, nil
}

func (c *compiler) expandHead(f Value, sc *scope) (Value, error) {
	for {
		list, ok := f.([]Value)
		if !ok || len(list) == 0 {
			return f, nil
		}
		sym, ok := list[0].(Symbol)
		if !ok {
			return f, nil
		}
		b := c.lookup(sc, string(sym), true)
		if b.kind != bindForm || b.entry.cf != nil || b.entry.sf != nil || b.entry.mac == nil {
			return f, nil
		}
		if err := c.expandStep(list[1:]); err != nil {
			return nil, err
		}
		expanded, err := b.entry.mac(list[1:])
		if err != nil {
			return nil, err
		}
		f = expanded
	}
}

func defineParts(args []Value) (name string, formals Value, body []Value, ok bool) {
	if len(args) < 1 {
		return "", nil, nil, false
	}
	switch sig := args[0].(type) {
	case []Value:
		if len(sig) == 0 {
			return "", nil, nil, false
		}
		s, ok := sig[0].(Symbol)
		if !ok {
			return "", nil, nil, false
		}
		return string(s), sig[1:], args[1:], true
	case *Pair:
		s, ok := sig.Car.(Symbol)
		if !ok {
			return "", nil, nil, false
		}
		return string(s), sig.Cdr, args[1:], true
	case Symbol:
		return string(sig), nil, nil, true
	}
	return "", nil, nil, false
}

func (env *Environment) setCompiler(name, tag string, cf compileFunc) {
	env.setForm(name, func(entry *formEntry) {
		entry.cf = cf
		entry.tag = tag
		entry.sf = nil
	})
}

func compileForms(env *Environment) {
	env.setCompiler("quote", "quote", cfQuote)
	env.setCompiler("if", "if", cfIf)
	env.setCompiler("define", "define", cfDefine)
	env.setCompiler("set!", "set!", cfSet)
	env.setCompiler("lambda", "lambda", cfLambda)
	env.setCompiler("begin", "begin", cfBegin)
	env.setCompiler("and", "and", cfAnd)
	env.setCompiler("or", "or", cfOr)
	env.setCompiler("pipe", "pipe", cfPipe)
	env.setCompiler("quasiquote", "quasiquote", cfQuasiquote)
	env.setCompiler("define-syntax", "define-syntax", cfDefineSyntax)
}

func cfQuote(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	if len(form) != 2 {
		return nil, errors.New("quote expects 1 argument")
	}
	return &constNode{v: form[1]}, nil
}

func cfIf(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	args := form[1:]
	if len(args) != 2 && len(args) != 3 {
		return nil, errors.New("if expects 2 or 3 arguments: (if cond then [else])")
	}
	cond, err := c.compile(args[0], sc, false)
	if err != nil {
		return nil, err
	}
	then, err := c.compile(args[1], sc, tail)
	if err != nil {
		return nil, err
	}
	n := &ifNode{cond: cond, then: then}
	if len(args) == 3 {
		if n.els, err = c.compile(args[2], sc, tail); err != nil {
			return nil, err
		}
	}
	return n, nil
}

func grantBinding(name string) bool {
	return name == "agent-allow-working-dir" || name == allowCommandsBinding
}

func cfDefine(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	args := form[1:]
	if len(args) < 2 {
		return nil, errors.New("define: invalid syntax")
	}
	var val node
	name, formals, body, ok := defineParts(args)
	switch {
	case !ok:
		switch args[0].(type) {
		case []Value:
			return nil, errors.New("define: expected function name in signature")
		case *Pair:
			return nil, errors.New("define: function name must be a symbol")
		}
		return nil, errors.New("define: first argument must be a symbol")
	case formals != nil || body != nil:
		if len(body) == 0 {
			return nil, errors.New("define: function body cannot be empty")
		}
		ln, err := c.compileLambda(formals, body, sc)
		if err != nil {
			return nil, err
		}
		val = ln
	default:
		if len(args) != 2 {
			return nil, errors.New("define: invalid syntax for variable")
		}
		var err error
		if val, err = c.compile(args[1], sc, false); err != nil {
			return nil, err
		}
	}
	var n node
	switch {
	case sc.static:
		n = &defineLocal{idx: sc.slotFor(name), name: Symbol(name), val: val}
	case sc.isGlobal():
		n = &defineGlobal{name: name, val: val, root: c.root}
	default:
		sc.declare(name)
		n = &defineDyn{name: name, val: val}
	}
	if grantBinding(name) {
		n = &grantGuard{name: name, root: c.root, inner: n}
	}
	return n, nil
}

func cfSet(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	args := form[1:]
	if len(args) != 2 {
		return nil, errors.New("set!: invalid syntax. Expected (set! name value)")
	}
	sym, ok := args[0].(Symbol)
	if !ok {
		return nil, errors.New("set!: first argument must be a symbol")
	}
	val, err := c.compile(args[1], sc, false)
	if err != nil {
		return nil, err
	}
	name := string(sym)
	var n node
	b := c.lookup(sc, name, false)
	switch b.kind {
	case bindLocal:
		n = &setLocal{depth: b.depth, idx: b.idx, val: val}
	case bindGlobal:
		n = &setGlobal{name: b.name, c: b.cell, root: c.root, val: val}
	case bindDyn:
		n = &setDyn{name: b.name, val: val}
	case bindDynFB:
		n = &setDyn{name: b.name, env: b.fb, val: val}
	default:
		n = &setGlobal{name: b.name, names: b.names, root: c.root, val: val}
	}
	if grantBinding(name) {
		n = &grantGuard{name: name, root: c.root, inner: n}
	}
	return n, nil
}

func cfLambda(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	if len(form) < 3 {
		return nil, errors.New("lambda expects at least 2 arguments")
	}
	return c.compileLambda(form[1], form[2:], sc)
}

func (c *compiler) compileLambda(formals Value, body []Value, sc *scope) (*lambdaNode, error) {
	params, rest, err := parseFormals(formals)
	if err != nil {
		return nil, err
	}
	names := params
	if rest != "" {
		names = append(append(make([]Symbol, 0, len(params)+1), params...), rest)
	}
	child := newStaticScope(sc, names)
	bodyNode, err := c.compileBody(body, child, true)
	if err != nil {
		return nil, err
	}
	return &lambdaNode{params: params, rest: rest, frame: child.names, shadow: child.shadow, body: bodyNode, src: body}, nil
}

func cfBegin(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	return c.compileBody(form[1:], sc, tail)
}

func cfAnd(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	if len(form) == 1 {
		return &constNode{v: true}, nil
	}
	nodes, err := c.compileSeq(form[1:], sc, tail)
	if err != nil {
		return nil, err
	}
	return &andNode{nodes: nodes}, nil
}

func cfOr(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	if len(form) == 1 {
		return &constNode{v: false}, nil
	}
	nodes, err := c.compileSeq(form[1:], sc, tail)
	if err != nil {
		return nil, err
	}
	return &orNode{nodes: nodes}, nil
}

func (c *compiler) compileSeq(forms []Value, sc *scope, tail bool) ([]node, error) {
	nodes := make([]node, len(forms))
	for i, f := range forms {
		n, err := c.compile(f, sc, tail && i == len(forms)-1)
		if err != nil {
			return nil, err
		}
		nodes[i] = n
	}
	return nodes, nil
}

func cfPipe(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	args := form[1:]
	if len(args) == 0 {
		return nil, errors.New("pipe expects at least 1 argument: (pipe expr stage...)")
	}
	first, err := c.compile(args[0], sc, false)
	if err != nil {
		return nil, err
	}
	stageScope := newStaticScope(sc, []Symbol{"_"})
	stages := make([]node, 0, len(args)-1)
	for _, stage := range args[1:] {
		var sform []Value
		switch s := stage.(type) {
		case Symbol:
			sform = []Value{s, Symbol("_")}
		case []Value:
			if len(s) == 0 {
				return nil, errors.New("pipe: empty stage")
			}
			if mentionsSymbol(s, "_") {
				sform = s
			} else {
				sform = append(append(make([]Value, 0, len(s)+1), s...), Symbol("_"))
			}
		default:
			return nil, errors.New("pipe: each stage must be a call form or a function name")
		}
		n, err := c.compile(sform, stageScope, false)
		if err != nil {
			return nil, err
		}
		stages = append(stages, n)
	}
	return &pipeNode{first: first, stages: stages, names: stageScope.names}, nil
}

func cfDefineSyntax(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	args := form[1:]
	if len(args) != 2 {
		return nil, errors.New("define-syntax expects 2 arguments: (define-syntax name transformer)")
	}
	name, ok := args[0].(Symbol)
	if !ok {
		return nil, errors.New("define-syntax: name must be a symbol")
	}
	sr, err := c.transformer("define-syntax", args[1], sc)
	if err != nil {
		return nil, err
	}
	mac := sr.macro(string(name))
	c.envFor(sc).SetMacro(string(name), mac)
	return &constNode{v: name}, nil
}

func (c *compiler) transformer(form string, spec Value, sc *scope) (*SyntaxRules, error) {
	if list, ok := spec.([]Value); ok && len(list) > 0 && c.isSF(sc, list[0], "syntax-rules") {
		return parseSyntaxRules(list[1:], c.envFor(sc))
	}
	notSR := fmt.Errorf("%s expects a syntax-rules transformer", form)
	if c.e == nil {
		return nil, notSR
	}
	var v Value
	var err error
	if sc.env != nil {
		v, err = c.e.Eval(spec, sc.env)
	} else {
		n, cerr := c.compile(spec, sc, false)
		if cerr != nil {
			return nil, cerr
		}
		g, ok := n.(*globalRef)
		if !ok {
			return nil, notSR
		}
		v, err = g.eval(c.e, nil)
	}
	if err != nil {
		return nil, err
	}
	sr, ok := v.(*SyntaxRules)
	if !ok {
		return nil, notSR
	}
	return sr, nil
}

func (c *compiler) isSF(sc *scope, v Value, name string) bool {
	s, ok := v.(Symbol)
	if !ok {
		return false
	}
	b := c.lookup(sc, string(s), true)
	if b.kind != bindForm || b.entry.sf == nil {
		return false
	}
	ref, ok := c.root.forms[name]
	return ok && ref.sf != nil && symBase(string(s)) == name
}

func cfQuasiquote(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	if len(form) != 2 {
		return nil, errors.New("quasiquote expects 1 argument")
	}
	return c.compileQQ(form[1], 1, sc)
}

type qqItem struct {
	n      node
	splice bool
}

func (c *compiler) compileQQ(x Value, depth int, sc *scope) (node, error) {
	switch v := x.(type) {
	case *Pair:
		car, err := c.compileQQ(v.Car, depth, sc)
		if err != nil {
			return nil, err
		}
		cdr, err := c.compileQQ(v.Cdr, depth, sc)
		if err != nil {
			return nil, err
		}
		if isConst(car) && isConst(cdr) {
			return &constNode{v: x}, nil
		}
		return &qqPairNode{car: car, cdr: cdr}, nil
	case Vector:
		items, allConst, err := c.compileQQItems(v, depth, sc)
		if err != nil {
			return nil, err
		}
		if allConst {
			return &constNode{v: x}, nil
		}
		return &qqVecNode{items: items}, nil
	case []Value:
		if len(v) == 2 {
			if sym, ok := v[0].(Symbol); ok {
				switch symBase(string(sym)) {
				case "unquote":
					if depth == 1 {
						return c.compile(v[1], sc, false)
					}
					return c.qqWrap(Symbol("unquote"), v[1], depth-1, sc, x)
				case "unquote-splicing":
					if depth == 1 {
						return nil, errors.New("unquote-splicing: not in list context")
					}
					return c.qqWrap(Symbol("unquote-splicing"), v[1], depth-1, sc, x)
				case "quasiquote":
					return c.qqWrap(Symbol("quasiquote"), v[1], depth+1, sc, x)
				}
			}
		}
		if len(v) >= 3 {
			if sym, ok := v[len(v)-2].(Symbol); ok && symBase(string(sym)) == "unquote" {
				items, itemsConst, err := c.compileQQItems(v[:len(v)-2], depth, sc)
				if err != nil {
					return nil, err
				}
				cdr, err := c.compileQQ([]Value{v[len(v)-2], v[len(v)-1]}, depth, sc)
				if err != nil {
					return nil, err
				}
				if itemsConst && isConst(cdr) {
					return &constNode{v: x}, nil
				}
				return &qqDottedNode{items: items, cdr: cdr}, nil
			}
		}
		items, allConst, err := c.compileQQItems(v, depth, sc)
		if err != nil {
			return nil, err
		}
		if allConst {
			return &constNode{v: x}, nil
		}
		return &qqListNode{items: items}, nil
	}
	return &constNode{v: x}, nil
}

func (c *compiler) qqWrap(head Symbol, inner Value, depth int, sc *scope, orig Value) (node, error) {
	in, err := c.compileQQ(inner, depth, sc)
	if err != nil {
		return nil, err
	}
	if isConst(in) {
		return &constNode{v: orig}, nil
	}
	return &qqListNode{items: []qqItem{{n: &constNode{v: head}}, {n: in}}}, nil
}

func (c *compiler) compileQQItems(items []Value, depth int, sc *scope) ([]qqItem, bool, error) {
	out := make([]qqItem, 0, len(items))
	allConst := true
	for _, item := range items {
		if sub, ok := item.([]Value); ok && len(sub) == 2 && depth == 1 {
			if sym, ok := sub[0].(Symbol); ok && symBase(string(sym)) == "unquote-splicing" {
				n, err := c.compile(sub[1], sc, false)
				if err != nil {
					return nil, false, err
				}
				out = append(out, qqItem{n: n, splice: true})
				allConst = false
				continue
			}
		}
		n, err := c.compileQQ(item, depth, sc)
		if err != nil {
			return nil, false, err
		}
		if !isConst(n) {
			allConst = false
		}
		out = append(out, qqItem{n: n})
	}
	return out, allConst, nil
}

func isConst(n node) bool {
	_, ok := n.(*constNode)
	return ok
}

type constNode struct{ v Value }

func (n *constNode) eval(e *Evaluator, fr *Environment) (Value, error) { return n.v, nil }

type errNode struct{ err error }

func (n *errNode) eval(e *Evaluator, fr *Environment) (Value, error) { return nil, n.err }

type localRef0 struct{ idx int }

func (n *localRef0) eval(e *Evaluator, fr *Environment) (Value, error) {
	return fr.vals[n.idx], nil
}

type localRef1 struct{ idx int }

func (n *localRef1) eval(e *Evaluator, fr *Environment) (Value, error) {
	return fr.parent.vals[n.idx], nil
}

type localRef struct{ depth, idx int }

func (n *localRef) eval(e *Evaluator, fr *Environment) (Value, error) {
	for d := n.depth; d > 0; d-- {
		fr = fr.parent
	}
	return fr.vals[n.idx], nil
}

type globalRef struct {
	name  string   // base spelling, for the error
	names []string // spellings to try, marked first (binding.names)
	c     *cell
	root  *Environment
}

func (n *globalRef) eval(e *Evaluator, fr *Environment) (Value, error) {
	if c := n.c; c != nil {
		return c.val, nil
	}
	return n.resolve()
}

func (n *globalRef) resolve() (Value, error) {
	c := findGlobal(n.root, n.names)
	if c == nil {
		return nil, fmt.Errorf("unbound symbol: %s", symBase(n.name))
	}
	n.c = c
	return c.val, nil
}

func findGlobal(root *Environment, names []string) *cell {
	for _, name := range names {
		if c, ok := root.bindings[name]; ok {
			return c
		}
	}
	return nil
}

type dynRef struct{ name string }

func (n *dynRef) eval(e *Evaluator, fr *Environment) (Value, error) { return fr.Lookup(n.name) }

type fbRef struct {
	env  *Environment
	name string
}

func (n *fbRef) eval(e *Evaluator, fr *Environment) (Value, error) { return n.env.Lookup(n.name) }

type setLocal struct {
	depth, idx int
	val        node
}

func (n *setLocal) eval(e *Evaluator, fr *Environment) (Value, error) {
	v, err := n.val.eval(e, fr)
	if err != nil {
		return nil, err
	}
	f := fr
	for d := n.depth; d > 0; d-- {
		f = f.parent
	}
	f.vals[n.idx] = v
	return v, nil
}

type setGlobal struct {
	name  string
	names []string
	c     *cell
	root  *Environment
	val   node
}

func (n *setGlobal) eval(e *Evaluator, fr *Environment) (Value, error) {
	v, err := n.val.eval(e, fr)
	if err != nil {
		return nil, err
	}
	if n.c == nil {
		c := findGlobal(n.root, n.names)
		if c == nil {
			return nil, fmt.Errorf("unbound variable: %s", symBase(n.name))
		}
		n.c = c
	}
	n.c.val = v
	return v, nil
}

type setDyn struct {
	name string
	env  *Environment // nil: from the current frame
	val  node
}

func (n *setDyn) eval(e *Evaluator, fr *Environment) (Value, error) {
	v, err := n.val.eval(e, fr)
	if err != nil {
		return nil, err
	}
	target := fr
	if n.env != nil {
		target = n.env
	}
	if err := target.SetScoped(n.name, v); err != nil {
		return nil, err
	}
	return v, nil
}

type defineLocal struct {
	idx  int
	name Symbol
	val  node
}

func (n *defineLocal) eval(e *Evaluator, fr *Environment) (Value, error) {
	v, err := n.val.eval(e, fr)
	if err != nil {
		return nil, err
	}
	fr.vals[n.idx] = v
	return n.name, nil
}

type defineGlobal struct {
	name string
	val  node
	root *Environment
}

func (n *defineGlobal) eval(e *Evaluator, fr *Environment) (Value, error) {
	v, err := n.val.eval(e, fr)
	if err != nil {
		return nil, err
	}
	n.root.Set(n.name, v)
	return Symbol(n.name), nil
}

type defineDyn struct {
	name string
	val  node
}

func (n *defineDyn) eval(e *Evaluator, fr *Environment) (Value, error) {
	v, err := n.val.eval(e, fr)
	if err != nil {
		return nil, err
	}
	fr.Set(n.name, v)
	return Symbol(n.name), nil
}

type grantGuard struct {
	name  string
	root  *Environment
	inner node
}

func (n *grantGuard) eval(e *Evaluator, fr *Environment) (Value, error) {
	if ev := n.root.ev; ev != nil && ev.Caller() != CallerUser {
		return nil, fmt.Errorf("%s can only be bound by the user at the prompt (or the prelude)", n.name)
	}
	return n.inner.eval(e, fr)
}

type ifNode struct {
	cond, then, els node
}

func (n *ifNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	c, err := n.cond.eval(e, fr)
	if err != nil {
		return nil, err
	}
	if !isFalsy(c) {
		return n.then.eval(e, fr)
	}
	if n.els == nil {
		return nil, nil
	}
	return n.els.eval(e, fr)
}

type seqNode struct{ nodes []node }

func (n *seqNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	last := len(n.nodes) - 1
	for _, sub := range n.nodes[:last] {
		if _, err := sub.eval(e, fr); err != nil {
			return nil, err
		}
	}
	return n.nodes[last].eval(e, fr)
}

type andNode struct{ nodes []node }

func (n *andNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	last := len(n.nodes) - 1
	for _, sub := range n.nodes[:last] {
		v, err := sub.eval(e, fr)
		if err != nil {
			return nil, err
		}
		if isFalsy(v) {
			return false, nil
		}
	}
	return n.nodes[last].eval(e, fr)
}

type orNode struct{ nodes []node }

func (n *orNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	last := len(n.nodes) - 1
	for _, sub := range n.nodes[:last] {
		v, err := sub.eval(e, fr)
		if err != nil {
			return nil, err
		}
		if !isFalsy(v) {
			return v, nil
		}
	}
	return n.nodes[last].eval(e, fr)
}

type dictNode struct {
	keys []string
	vals []node
}

func (n *dictNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	d := make(Dictionary, len(n.keys))
	for i, k := range n.keys {
		v, err := n.vals[i].eval(e, fr)
		if err != nil {
			return nil, err
		}
		d[k] = v
	}
	return d, nil
}

type lambdaNode struct {
	params []Symbol
	rest   Symbol
	frame  []Symbol
	shadow *Environment
	body   node
	src    []Value
}

func (n *lambdaNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	fr.markCaptured() // the closure stores the frame (frame.go)
	return &Lambda{params: n.params, rest: n.rest, frame: n.frame, shadow: n.shadow, body: n.body, env: fr, src: n.src}, nil
}

type letNode struct {
	names  []Symbol
	shadow *Environment
	inits  []node
	body   node
	tail   bool
}

func (n *letNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	frame := e.acquireFrame(fr, n.names, len(n.names))
	frame.shadow = n.shadow
	for i, init := range n.inits {
		v, err := init.eval(e, fr)
		if err != nil {
			e.releaseFrame(frame) // not linked anywhere yet
			return nil, err
		}
		frame.vals[i] = v
	}
	if n.tail {
		return e.tailFrame(n.body, frame), nil
	}
	frame.fresh = true
	return e.run(n.body, frame)
}

type pipeNode struct {
	first  node
	stages []node
	names  []Symbol
}

func (n *pipeNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	val, err := n.first.eval(e, fr)
	if err != nil {
		return nil, err
	}
	for _, stage := range n.stages {
		frame := e.acquireFrame(fr, n.names, 1)
		frame.vals[0] = val
		frame.fresh = true
		prev := val
		if val, err = e.run(stage, frame); err != nil {
			if s, isStream := prev.(*Stream); isStream {
				_ = s.Close()
			}
			return nil, err
		}
	}
	return val, nil
}

type qqPairNode struct{ car, cdr node }

func (n *qqPairNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	car, err := n.car.eval(e, fr)
	if err != nil {
		return nil, err
	}
	cdr, err := n.cdr.eval(e, fr)
	if err != nil {
		return nil, err
	}
	return &Pair{Car: car, Cdr: cdr}, nil
}

type qqListNode struct{ items []qqItem }

func (n *qqListNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	out := make([]Value, 0, len(n.items))
	out, err := qqBuild(e, fr, n.items, out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

type qqDottedNode struct {
	items []qqItem
	cdr   node
}

func (n *qqDottedNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	out, err := qqBuild(e, fr, n.items, make([]Value, 0, len(n.items)+1))
	if err != nil {
		return nil, err
	}
	cdr, err := n.cdr.eval(e, fr)
	if err != nil {
		return nil, err
	}
	if tail, ok := cdr.([]Value); ok {
		return append(out, tail...), nil
	}
	res := cdr
	for i := len(out) - 1; i >= 0; i-- {
		res = &Pair{Car: out[i], Cdr: res}
	}
	return res, nil
}

type qqVecNode struct{ items []qqItem }

func (n *qqVecNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	out := make([]Value, 0, len(n.items))
	out, err := qqBuild(e, fr, n.items, out)
	if err != nil {
		return nil, err
	}
	return Vector(out), nil
}

func qqBuild(e *Evaluator, fr *Environment, items []qqItem, out []Value) ([]Value, error) {
	for _, it := range items {
		v, err := it.n.eval(e, fr)
		if err != nil {
			return nil, err
		}
		if !it.splice {
			out = append(out, v)
			continue
		}
		spliced, ok, err := AsList(v)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("unquote-splicing expects a list")
		}
		out = append(out, spliced...)
	}
	return out, nil
}

type sfNode struct {
	sf   SpecialFormFunc
	args []Value
	tail bool
}

func (n *sfNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	v, err := n.sf(n.args, fr, e)
	if err != nil {
		return nil, err
	}
	if tc, ok := v.(*TailCall); ok && !n.tail {
		return e.runTail(tc)
	}
	return v, nil
}

type callNode struct {
	fn   node
	args []node
	tail bool
}

func (n *callNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	fv, err := n.fn.eval(e, fr)
	if err != nil {
		return nil, err
	}
	return e.applyNodes(fv, n.args, fr, n.tail)
}

type globalCallNode struct {
	g    *globalRef
	args []node
	tail bool
}

func (n *globalCallNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	var fv Value
	if c := n.g.c; c != nil {
		fv = c.val
	} else {
		var err error
		if fv, err = n.g.resolve(); err != nil {
			return nil, err
		}
	}
	return e.applyNodes(fv, n.args, fr, n.tail)
}

func (e *Evaluator) applyNodes(fv Value, args []node, fr *Environment, tail bool) (Value, error) {
	if interrupted.Load() {
		return nil, ErrInterrupted
	}
	switch f := fv.(type) {
	case *Lambda:
		return e.callCompiled(f, args, fr, tail)
	case *FastBuiltin:
		if len(args) == 1 && f.Fn1 != nil {
			a, err := args[0].eval(e, fr)
			if err != nil {
				return nil, err
			}
			return f.Fn1(a, fr)
		}
		if len(args) == 2 && f.Fn2 != nil {
			a, err := args[0].eval(e, fr)
			if err != nil {
				return nil, err
			}
			b, err := args[1].eval(e, fr)
			if err != nil {
				return nil, err
			}
			return f.Fn2(a, b, fr)
		}
		vals, err := e.evalArgs(args, fr)
		if err != nil {
			return nil, err
		}
		return f.Fn(vals, fr)
	case BuiltinFunc:
		vals, err := e.evalArgs(args, fr)
		if err != nil {
			return nil, err
		}
		v, err := f(vals, fr)
		if tc, ok := v.(*TailCall); ok && !tail {
			return e.runTail(tc)
		}
		return v, err
	case *CaseLambda:
		l := f.match(len(args))
		if l == nil {
			return nil, fmt.Errorf("case-lambda: no clause matches %d arguments", len(args))
		}
		return e.callCompiled(l, args, fr, tail)
	case *Continuation:
		vals, err := e.evalArgs(args, fr)
		if err != nil {
			return nil, err
		}
		return nil, f.invoke(vals)
	case *Parameter:
		if len(args) != 0 {
			return nil, errors.New("a parameter object takes no arguments")
		}
		return f.current(), nil
	}
	return nil, notCallable(fv)
}

func (e *Evaluator) evalArgs(args []node, fr *Environment) ([]Value, error) {
	vals := make([]Value, len(args))
	for i, a := range args {
		v, err := a.eval(e, fr)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return vals, nil
}

func (e *Evaluator) callCompiled(l *Lambda, args []node, fr *Environment, tail bool) (Value, error) {
	nargs := len(args)
	nparams := len(l.params)
	if l.rest == "" {
		if nargs != nparams {
			return nil, fmt.Errorf("lambda: expected %d arguments, got %d", nparams, nargs)
		}
	} else if nargs < nparams {
		return nil, fmt.Errorf("lambda: expected at least %d arguments, got %d", nparams, nargs)
	}
	frame := e.acquireFrame(l.env, l.frame, len(l.frame))
	frame.shadow = l.shadow
	for i := 0; i < nparams; i++ {
		v, err := args[i].eval(e, fr)
		if err != nil {
			e.releaseFrame(frame) // not linked anywhere yet
			return nil, err
		}
		frame.vals[i] = v
	}
	if l.rest != "" {
		extra := make([]Value, nargs-nparams)
		for i := range extra {
			v, err := args[nparams+i].eval(e, fr)
			if err != nil {
				e.releaseFrame(frame)
				return nil, err
			}
			extra[i] = v
		}
		frame.vals[nparams] = listFromSlice(extra)
	}
	if tail {
		return e.tailFrame(l.body, frame), nil
	}
	frame.fresh = true
	return e.run(l.body, frame)
}

func (e *Evaluator) applyLambda(l *Lambda, args []Value) (Value, error) {
	frame, err := e.bindFrame(l, args)
	if err != nil {
		return nil, err
	}
	frame.fresh = true
	return e.run(l.body, frame)
}

func (e *Evaluator) tailApply(fn Value, args []Value) (Value, bool, error) {
	var l *Lambda
	switch f := fn.(type) {
	case *Lambda:
		l = f
	case *CaseLambda:
		if l = f.match(len(args)); l == nil {
			return nil, false, fmt.Errorf("case-lambda: no clause matches %d arguments", len(args))
		}
	default:
		return nil, false, nil
	}
	frame, err := e.bindFrame(l, args)
	if err != nil {
		return nil, false, err
	}
	return e.tailFrame(l.body, frame), true, nil
}

func (e *Evaluator) bindFrame(l *Lambda, args []Value) (*Environment, error) {
	nparams := len(l.params)
	if l.rest == "" {
		if len(args) != nparams {
			return nil, fmt.Errorf("lambda: expected %d arguments, got %d", nparams, len(args))
		}
	} else if len(args) < nparams {
		return nil, fmt.Errorf("lambda: expected at least %d arguments, got %d", nparams, len(args))
	}
	frame := e.acquireFrame(l.env, l.frame, len(l.frame))
	frame.shadow = l.shadow
	copy(frame.vals, args[:nparams])
	if l.rest != "" {
		frame.vals[nparams] = listFromSlice(args[nparams:])
	}
	return frame, nil
}

func (e *Evaluator) run(n node, fr *Environment) (Value, error) {
	var owned *Environment
	if fr.fresh {
		fr.fresh = false
		owned = fr
	}
	for {
		if interrupted.Load() {
			e.releaseOwned(owned)
			return nil, ErrInterrupted
		}
		v, err := n.eval(e, fr)
		if err != nil {
			e.releaseOwned(owned)
			return nil, err
		}
		tc, ok := v.(*TailCall)
		if !ok {
			e.releaseOwned(owned)
			return v, nil
		}
		n, fr = tc.Node, tc.Env
		if n == nil {
			if n, err = e.compileIn(tc.Expr, fr, true); err != nil {
				e.releaseOwned(owned)
				return nil, err
			}
		}
		if fr.fresh {
			fr.fresh = false
			release := true
			for p := fr.parent; p != nil && !p.captured; p = p.parent {
				if p == owned {
					release = false
					break
				}
			}
			if release {
				e.releaseOwned(owned)
			}
			owned = fr
		}
	}
}

func (e *Evaluator) runTail(tc *TailCall) (Value, error) {
	n, fr := tc.Node, tc.Env
	if n == nil {
		var err error
		if n, err = e.compileIn(tc.Expr, fr, true); err != nil {
			return nil, err
		}
	}
	return e.run(n, fr)
}
