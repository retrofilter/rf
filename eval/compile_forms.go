package eval

import (
	"errors"
	"fmt"
)

type bindTarget struct {
	kind bindKind // bindLocal, bindGlobal, or bindDyn
	idx  int
	name string
}

func (c *compiler) defineTarget(sc *scope, name string) bindTarget {
	switch {
	case sc.static:
		return bindTarget{kind: bindLocal, idx: sc.slotFor(name), name: name}
	case sc.isGlobal():
		return bindTarget{kind: bindGlobal, name: name}
	default:
		sc.declare(name)
		return bindTarget{kind: bindDyn, name: name}
	}
}

func (t bindTarget) store(root, fr *Environment, v Value) {
	switch t.kind {
	case bindLocal:
		fr.vals[t.idx] = v
	case bindGlobal:
		root.Set(t.name, v)
	default:
		fr.Set(t.name, v)
	}
}

func (c *compiler) guardGrants(n node, names []string) node {
	for _, name := range names {
		if grantBinding(name) {
			return &grantGuard{name: name, root: c.root, inner: n}
		}
	}
	return n
}

func cfDefineValues(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	args := form[1:]
	if len(args) != 2 {
		return nil, errors.New("define-values expects 2 arguments: (define-values (names...) expr)")
	}
	params, rest, err := parseFormals(args[0])
	if err != nil {
		return nil, errors.New("define-values: formals must be symbols")
	}
	val, err := c.compile(args[1], sc, false)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(params)+1)
	for _, p := range params {
		names = append(names, string(p))
	}
	if rest != "" {
		names = append(names, string(rest))
	}
	targets := make([]bindTarget, len(names))
	for i, name := range names {
		targets[i] = c.defineTarget(sc, name)
	}
	n := &defineValuesNode{val: val, targets: targets, nparams: len(params), rest: rest != "", root: c.root}
	return c.guardGrants(n, names), nil
}

type defineValuesNode struct {
	val     node
	targets []bindTarget // params, then the rest name when present
	nparams int
	rest    bool
	root    *Environment
}

func (n *defineValuesNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	v, err := n.val.eval(e, fr)
	if err != nil {
		return nil, err
	}
	vals := valuesOf(v)
	if err := checkValues("define-values", n.nparams, n.rest, len(vals)); err != nil {
		return nil, err
	}
	for i := 0; i < n.nparams; i++ {
		n.targets[i].store(n.root, fr, vals[i])
	}
	if n.rest {
		n.targets[n.nparams].store(n.root, fr, listFromSlice(vals[n.nparams:]))
	}
	return nil, nil
}

func checkValues(name string, nparams int, rest bool, got int) error {
	if !rest && got != nparams {
		return fmt.Errorf("%s: expected %d values, got %d", name, nparams, got)
	}
	if rest && got < nparams {
		return fmt.Errorf("%s: expected at least %d values, got %d", name, nparams, got)
	}
	return nil
}

func cfDefineRecordType(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	def, err := parseRecordDef(form[1:])
	if err != nil {
		return nil, err
	}
	names := def.names()
	targets := make([]bindTarget, len(names))
	for i, name := range names {
		targets[i] = c.defineTarget(sc, string(name))
	}
	strs := make([]string, len(names))
	for i, name := range names {
		strs[i] = string(name)
	}
	return c.guardGrants(&defineRecordNode{def: def, targets: targets, root: c.root}, strs), nil
}

type defineRecordNode struct {
	def     *recordDef
	targets []bindTarget
	root    *Environment
}

func (n *defineRecordNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	vals := n.def.instantiate()
	for i, t := range n.targets {
		t.store(n.root, fr, vals[i])
	}
	return n.def.typeName, nil
}

func formDefines(tag string, args []Value) []string {
	switch tag {
	case "define-values":
		if len(args) < 1 {
			return nil
		}
		params, rest, err := parseFormals(args[0])
		if err != nil {
			return nil
		}
		names := make([]string, 0, len(params)+1)
		for _, p := range params {
			names = append(names, string(p))
		}
		if rest != "" {
			names = append(names, string(rest))
		}
		return names
	case "define-record-type":
		def, err := parseRecordDef(args)
		if err != nil {
			return nil
		}
		syms := def.names()
		names := make([]string, len(syms))
		for i, s := range syms {
			names[i] = string(s)
		}
		return names
	}
	return nil
}

func cfCaseLambda(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	args := form[1:]
	if len(args) == 0 {
		return nil, errors.New("case-lambda expects at least one (formals body...) clause")
	}
	n := &caseLambdaNode{clauses: make([]*lambdaNode, len(args))}
	for i, a := range args {
		clause, ok := a.([]Value)
		if !ok || len(clause) < 2 {
			return nil, errors.New("case-lambda: each clause must be (formals body...)")
		}
		ln, err := c.compileLambda(clause[0], clause[1:], sc)
		if err != nil {
			return nil, err
		}
		n.clauses[i] = ln
	}
	return n, nil
}

type caseLambdaNode struct{ clauses []*lambdaNode }

func (n *caseLambdaNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	fr.markCaptured() // the clauses are closures storing the frame (frame.go)
	cl := &CaseLambda{clauses: make([]*Lambda, len(n.clauses))}
	for i, ln := range n.clauses {
		cl.clauses[i] = &Lambda{params: ln.params, rest: ln.rest, frame: ln.frame, shadow: ln.shadow, body: ln.body, env: fr, src: ln.src}
	}
	return cl, nil
}

func cfDelay(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	if len(form) != 2 {
		return nil, errors.New("delay expects 1 argument")
	}
	body, err := c.compile(form[1], sc, false)
	if err != nil {
		return nil, err
	}
	return &delayNode{body: body}, nil
}

func cfDelayForce(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	if len(form) != 2 {
		return nil, errors.New("delay-force expects 1 argument")
	}
	body, err := c.compile(form[1], sc, false)
	if err != nil {
		return nil, err
	}
	return &delayNode{body: body, lazy: true}, nil
}

type delayNode struct {
	body node
	lazy bool
}

func (n *delayNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	fr.markCaptured() // the compute closure stores the frame (frame.go)
	body := n.body
	return &Promise{lazy: n.lazy, compute: func() (Value, error) { return body.eval(e, fr) }}, nil
}

func cfParameterize(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	args := form[1:]
	if len(args) < 1 {
		return nil, errors.New("parameterize expects (parameterize ((param value)...) body...)")
	}
	bindings, ok := args[0].([]Value)
	if !ok {
		return nil, errors.New("parameterize: first argument must be a list of (param value) pairs")
	}
	n := &parameterizeNode{params: make([]node, len(bindings)), vals: make([]node, len(bindings)), src: make([]Value, len(bindings))}
	for i, b := range bindings {
		pair, ok := b.([]Value)
		if !ok || len(pair) != 2 {
			return nil, errors.New("parameterize: each binding must be a (param value) pair")
		}
		var err error
		if n.params[i], err = c.compile(pair[0], sc, false); err != nil {
			return nil, err
		}
		if n.vals[i], err = c.compile(pair[1], sc, false); err != nil {
			return nil, err
		}
		n.src[i] = pair[0]
	}
	// The body can't be in tail position: the parameters pop after it.
	body, err := c.compileBody(args[1:], sc, false)
	if err != nil {
		return nil, err
	}
	n.body = body
	return n, nil
}

type parameterizeNode struct {
	params, vals []node
	src          []Value // the parameter expressions, for the error
	body         node
}

func (n *parameterizeNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	var pbuf [4]*Parameter
	var vbuf [4]Value
	params, vals := pbuf[:0], vbuf[:0]
	for i, pn := range n.params {
		pv, err := pn.eval(e, fr)
		if err != nil {
			return nil, err
		}
		p, ok := pv.(*Parameter)
		if !ok {
			return nil, fmt.Errorf("parameterize: %s is not a parameter object", PrintValue(n.src[i]))
		}
		v, err := n.vals[i].eval(e, fr)
		if err != nil {
			return nil, err
		}
		if v, err = p.convert(v, fr); err != nil {
			return nil, err
		}
		params = append(params, p)
		vals = append(vals, v)
	}
	for i, p := range params {
		p.vals = append(p.vals, vals[i])
	}
	res, err := n.body.eval(e, fr)
	for _, p := range params {
		p.vals = p.vals[:len(p.vals)-1]
	}
	return res, err
}

func cfGuard(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	args := form[1:]
	if len(args) < 2 {
		return nil, errors.New("guard expects (guard (var clause...) body...)")
	}
	spec, ok := args[0].([]Value)
	if !ok || len(spec) == 0 {
		return nil, errors.New("guard: first argument must be (var clause...)")
	}
	varSym, ok := spec[0].(Symbol)
	if !ok {
		return nil, errors.New("guard: condition variable must be a symbol")
	}
	body, err := c.compileBody(args[1:], sc, false)
	if err != nil {
		return nil, err
	}
	child := newStaticScope(sc, []Symbol{varSym})
	clauses := make([]guardClause, 0, len(spec)-1)
	for _, clause := range spec[1:] {
		cl, ok := clause.([]Value)
		if !ok || len(cl) == 0 {
			return nil, errors.New("guard: each clause must be a non-empty list")
		}
		var gc guardClause
		body := cl[1:]
		if symIsBase(cl[0], "else") {
			gc.els = true
			if len(body) == 0 {
				return nil, errors.New("guard: else clause requires a body")
			}
		} else if gc.test, err = c.compile(cl[0], child, false); err != nil {
			return nil, err
		}
		switch {
		case len(body) == 0:
			// (test): the test value is the result.
		case symIsBase(body[0], "=>"):
			if gc.els || len(body) != 2 {
				return nil, errors.New("guard: => expects (test => proc)")
			}
			if gc.arrow, err = c.compile(body[1], child, false); err != nil {
				return nil, err
			}
		default:
			if gc.body, err = c.compileBody(body, child, tail); err != nil {
				return nil, err
			}
		}
		clauses = append(clauses, gc)
	}
	return &guardNode{body: body, names: child.names, shadow: child.shadow, clauses: clauses, tail: tail, root: c.root}, nil
}

type guardClause struct {
	els   bool
	test  node // nil for else
	arrow node // (test => proc): proc, applied to the test value
	body  node // nil for a bare (test) clause
}

type guardNode struct {
	body    node
	names   []Symbol // the clause frame: the condition variable, then any internal defines
	shadow  *Environment
	clauses []guardClause
	tail    bool
	root    *Environment
}

func (n *guardNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	ev := n.root.ev
	if ev == nil {
		ev = e
	}
	saved := ev.handlers
	ev.handlers = append(saved[:len(saved):len(saved)], guardMarker{})
	res, err := n.body.eval(e, fr)
	ev.handlers = saved
	if err == nil {
		return res, nil
	}
	if isUnwindBarrier(err) {
		return nil, err
	}
	frame := e.acquireFrame(fr, n.names, len(n.names))
	frame.shadow = n.shadow
	frame.vals[0] = conditionValue(err)
	for i := range n.clauses {
		cl := &n.clauses[i]
		var test Value
		if !cl.els {
			t, terr := cl.test.eval(e, frame)
			if terr != nil {
				e.releaseFrame(frame)
				return nil, terr
			}
			if isFalsy(t) {
				continue
			}
			test = t
		}
		switch {
		case cl.arrow != nil:
			proc, perr := cl.arrow.eval(e, frame)
			if perr != nil {
				e.releaseFrame(frame)
				return nil, perr
			}
			v, aerr := e.Apply(proc, []Value{test}, frame)
			e.releaseFrame(frame)
			return v, aerr
		case cl.body == nil:
			e.releaseFrame(frame)
			return test, nil
		}
		if n.tail {
			return e.tailFrame(cl.body, frame), nil
		}
		frame.fresh = true
		return e.run(cl.body, frame)
	}
	e.releaseFrame(frame)
	return nil, err // no clause matched: re-raise the original
}

func cfLetValues(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	return c.compileLetValues("let-values", form, sc, tail, false)
}

func cfLetStarValues(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	return c.compileLetValues("let*-values", form, sc, tail, true)
}

func (c *compiler) compileLetValues(name string, form []Value, sc *scope, tail bool, sequential bool) (node, error) {
	args := form[1:]
	if len(args) < 1 {
		return nil, fmt.Errorf("%s expects (%s ((formals expr)...) body...)", name, name)
	}
	bindings, ok := args[0].([]Value)
	if !ok {
		return nil, fmt.Errorf("%s: first argument must be a list of (formals expr) pairs", name)
	}
	body := args[1:]
	if sequential && len(bindings) > 1 {
		inner := append([]Value{form[0], bindings[1:]}, body...)
		bindings, body = bindings[:1], []Value{inner}
	}
	n := &letValuesNode{name: name, clauses: make([]lvClause, len(bindings)), tail: tail}
	var names []Symbol
	for i, b := range bindings {
		pair, ok := b.([]Value)
		if !ok || len(pair) != 2 {
			return nil, fmt.Errorf("%s: each binding must be a (formals expr) pair", name)
		}
		params, rest, err := parseFormals(pair[0])
		if err != nil {
			return nil, fmt.Errorf("%s: formals must be symbols", name)
		}
		init, err := c.compile(pair[1], sc, false)
		if err != nil {
			return nil, err
		}
		n.clauses[i] = lvClause{init: init, base: len(names), nparams: len(params), rest: rest != ""}
		for _, p := range append(params, rest) {
			if p == "" {
				continue
			}
			for _, prev := range names {
				if prev == p {
					return nil, fmt.Errorf("%s: duplicate variable %s", name, p)
				}
			}
			names = append(names, p)
		}
	}
	child := newStaticScope(sc, names)
	bodyNode, err := c.compileBody(body, child, tail)
	if err != nil {
		return nil, err
	}
	n.names, n.shadow, n.body = child.names, child.shadow, bodyNode
	return n, nil
}

type lvClause struct {
	init    node
	base    int // first slot of this clause's formals
	nparams int
	rest    bool
}

type letValuesNode struct {
	name    string
	clauses []lvClause
	names   []Symbol
	shadow  *Environment
	body    node
	tail    bool
}

func (n *letValuesNode) eval(e *Evaluator, fr *Environment) (Value, error) {
	frame := e.acquireFrame(fr, n.names, len(n.names))
	frame.shadow = n.shadow
	for _, cl := range n.clauses {
		v, err := cl.init.eval(e, fr)
		if err != nil {
			e.releaseFrame(frame) // not linked anywhere yet
			return nil, err
		}
		vals := valuesOf(v)
		if err := checkValues(n.name, cl.nparams, cl.rest, len(vals)); err != nil {
			e.releaseFrame(frame)
			return nil, err
		}
		copy(frame.vals[cl.base:], vals[:cl.nparams])
		if cl.rest {
			frame.vals[cl.base+cl.nparams] = listFromSlice(vals[cl.nparams:])
		}
	}
	if n.tail {
		return e.tailFrame(n.body, frame), nil
	}
	frame.fresh = true
	return e.run(n.body, frame)
}

func cfLetSyntax(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	return c.compileLetSyntax("let-syntax", form, sc, tail)
}

func cfLetrecSyntax(c *compiler, form []Value, sc *scope, tail bool) (node, error) {
	return c.compileLetSyntax("letrec-syntax", form, sc, tail)
}

func (c *compiler) compileLetSyntax(formName string, form []Value, sc *scope, tail bool) (node, error) {
	args := form[1:]
	if len(args) < 1 {
		return nil, fmt.Errorf("%s expects (%s ((name transformer)...) body...)", formName, formName)
	}
	bindings, ok := args[0].([]Value)
	if !ok {
		return nil, fmt.Errorf("%s: first argument must be a list of (name transformer) pairs", formName)
	}
	child := newStaticScope(sc, nil)
	for _, bnd := range bindings {
		pair, ok := bnd.([]Value)
		if !ok || len(pair) != 2 {
			return nil, fmt.Errorf("%s: each binding must be (name transformer)", formName)
		}
		name, ok := pair[0].(Symbol)
		if !ok {
			return nil, fmt.Errorf("%s: macro name must be a symbol", formName)
		}
		sr, err := c.transformer(formName, pair[1], child)
		if err != nil {
			return nil, err
		}
		c.envFor(child).SetMacro(string(name), sr.macro(string(name)))
	}
	body, err := c.compileBody(args[1:], child, tail)
	if err != nil {
		return nil, err
	}
	return &letNode{names: child.names, shadow: child.shadow, body: body, tail: tail}, nil
}
