package eval

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

const markByte = '\x01'

var (
	expansionInst atomic.Uint64 // uniquifies each expansion's marks
	macroEnvID    atomic.Uint64 // ids for non-global definition envs
)

func markedName(orig string, envID, inst uint64) string {
	return string(markByte) + strconv.FormatUint(envID, 10) + "." +
		strconv.FormatUint(inst, 10) + string(markByte) + orig
}

func unmark(name string) (orig string, envID uint64, ok bool) {
	if len(name) == 0 || name[0] != markByte {
		return "", 0, false
	}
	rest := name[1:]
	i := strings.IndexByte(rest, markByte)
	if i < 0 {
		return "", 0, false
	}
	ids := rest[:i]
	dot := strings.IndexByte(ids, '.')
	if dot < 0 {
		return "", 0, false
	}
	id, err := strconv.ParseUint(ids[:dot], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return rest[i+1:], id, true
}

func symBase(name string) string {
	for {
		orig, _, ok := unmark(name)
		if !ok {
			return name
		}
		name = orig
	}
}

func symIsBase(v Value, name string) bool {
	s, ok := v.(Symbol)
	return ok && symBase(string(s)) == name
}

func listParts(v Value) (elems []Value, tail Value, ok bool) {
	switch n := v.(type) {
	case []Value:
		return n, nil, true
	case *Pair:
		var out []Value
		t, err := walkPairs(n, func(c *Pair) { out = append(out, c.Car) })
		if err != nil {
			return nil, nil, false // circular: not matchable
		}
		if s, isSlice := t.([]Value); isSlice {
			return append(out, s...), nil, true
		}
		if t == nil {
			return out, nil, true
		}
		return out, t, true
	}
	if v == nil {
		return nil, nil, true
	}
	return nil, nil, false
}

func rebuildList(elems []Value, tail Value) Value {
	if tail == nil || isEmptyList(tail) {
		if elems == nil {
			return []Value{}
		}
		return elems
	}
	out := tail
	for i := len(elems) - 1; i >= 0; i-- {
		out = &Pair{Car: elems[i], Cdr: out}
	}
	return out
}

type srRule struct {
	patElems []Value
	patTail  Value // dotted-tail pattern; nil for proper patterns
	template Value
}

// SyntaxRules is a macro transformer value, produced by the syntax-rules
// special form and installed by define-syntax / let-syntax.
type SyntaxRules struct {
	ellipsis string          // base spelling of the ellipsis identifier
	literals map[string]bool // exact spellings from the literals list
	rules    []srRule
	envID    uint64
}

func parseSyntaxRules(args []Value, env *Environment) (*SyntaxRules, error) {
	sr := &SyntaxRules{ellipsis: "...", literals: map[string]bool{}}
	i := 0
	if len(args) > 0 {
		if s, ok := args[0].(Symbol); ok {
			sr.ellipsis = symBase(string(s))
			i = 1
		}
	}
	if i >= len(args) {
		return nil, errors.New("syntax-rules expects (syntax-rules [ellipsis] (literals...) (pattern template)...)")
	}
	lits, ok := args[i].([]Value)
	if !ok {
		return nil, errors.New("syntax-rules: expected a list of literal identifiers")
	}
	for _, l := range lits {
		s, ok := l.(Symbol)
		if !ok {
			return nil, errors.New("syntax-rules: literals must be identifiers")
		}
		sr.literals[string(s)] = true
	}
	for _, r := range args[i+1:] {
		cl, ok := r.([]Value)
		if !ok || len(cl) != 2 {
			return nil, errors.New("syntax-rules: each rule must be (pattern template)")
		}
		elems, tail, ok := listParts(cl[0])
		if !ok || len(elems) == 0 {
			return nil, errors.New("syntax-rules: pattern must be a non-empty list")
		}
		sr.rules = append(sr.rules, srRule{patElems: elems[1:], patTail: tail, template: cl[1]})
	}
	if env.parent != nil {
		env.markCaptured()
		root := env
		for root.parent != nil {
			root = root.parent
		}
		id := macroEnvID.Add(1)
		if root.macroEnvs == nil {
			root.macroEnvs = make(map[uint64]*Environment)
		}
		root.macroEnvs[id] = env
		sr.envID = id
	}
	return sr, nil
}

func (sr *SyntaxRules) isEllipsis(v Value) bool {
	s, ok := v.(Symbol)
	// A literal takes priority over the ellipsis (R7RS 4.3.2).
	return ok && !sr.literals[string(s)] && symBase(string(s)) == sr.ellipsis
}

type matchNode struct {
	leaf  Value
	seq   []*matchNode
	isSeq bool
}

type srBindings map[string]*matchNode

func (sr *SyntaxRules) match(pat, input Value, b srBindings) bool {
	switch p := pat.(type) {
	case Symbol:
		if sr.literals[string(p)] {
			s, ok := input.(Symbol)
			return ok && symBase(string(s)) == symBase(string(p))
		}
		if symBase(string(p)) == "_" {
			return true
		}
		if sr.isEllipsis(p) {
			return false // malformed: bare ellipsis in match position
		}
		b[string(p)] = &matchNode{leaf: input}
		return true
	case []Value:
		return sr.matchListValue(p, nil, input, b)
	case *Pair:
		elems, tail, ok := listParts(p)
		return ok && sr.matchListValue(elems, tail, input, b)
	case Vector:
		iv, ok := input.(Vector)
		return ok && sr.matchParts(p, nil, iv, nil, b)
	default:
		return deepEqual(pat, input)
	}
}

func (sr *SyntaxRules) matchListValue(patElems []Value, patTail Value, input Value, b srBindings) bool {
	inElems, inTail, ok := listParts(input)
	if !ok {
		return false
	}
	return sr.matchParts(patElems, patTail, inElems, inTail, b)
}

func (sr *SyntaxRules) matchParts(patElems []Value, patTail Value, in []Value, inTail Value, b srBindings) bool {
	ell := -1
	for i, p := range patElems {
		if sr.isEllipsis(p) {
			ell = i
			break
		}
	}
	if ell == 0 {
		return false // (... x) is not a valid pattern
	}
	matchTail := func(consumed int) bool {
		if patTail == nil {
			return inTail == nil && consumed == len(in)
		}
		return sr.match(patTail, rebuildList(in[consumed:], inTail), b)
	}
	if ell < 0 {
		if len(in) < len(patElems) || (patTail == nil && len(in) != len(patElems)) {
			return false
		}
		for i := range patElems {
			if !sr.match(patElems[i], in[i], b) {
				return false
			}
		}
		return matchTail(len(patElems))
	}
	rep := patElems[ell-1]
	front := patElems[:ell-1]
	back := patElems[ell+1:]
	if len(in) < len(front)+len(back) {
		return false
	}
	for i := range front {
		if !sr.match(front[i], in[i], b) {
			return false
		}
	}
	backStart := len(in) - len(back)
	for i := range back {
		if !sr.match(back[i], in[backStart+i], b) {
			return false
		}
	}
	vars := sr.patternVars(rep, nil)
	nodes := make(map[string]*matchNode, len(vars))
	for _, v := range vars {
		nodes[v] = &matchNode{isSeq: true}
	}
	for _, x := range in[len(front):backStart] {
		sub := srBindings{}
		if !sr.match(rep, x, sub) {
			return false
		}
		for _, v := range vars {
			nodes[v].seq = append(nodes[v].seq, sub[v])
		}
	}
	for v, n := range nodes {
		b[v] = n
	}
	return matchTail(len(in))
}

func (sr *SyntaxRules) patternVars(pat Value, acc []string) []string {
	switch p := pat.(type) {
	case Symbol:
		if sr.literals[string(p)] || sr.isEllipsis(p) || symBase(string(p)) == "_" {
			return acc
		}
		return append(acc, string(p))
	case []Value:
		for _, e := range p {
			acc = sr.patternVars(e, acc)
		}
	case *Pair:
		if elems, tail, ok := listParts(p); ok {
			for _, e := range elems {
				acc = sr.patternVars(e, acc)
			}
			if tail != nil {
				acc = sr.patternVars(tail, acc)
			}
		}
	case Vector:
		for _, e := range p {
			acc = sr.patternVars(e, acc)
		}
	}
	return acc
}

func (sr *SyntaxRules) macro(name string) MacroFunc {
	return func(args []Value) (Value, error) {
		for _, r := range sr.rules {
			b := srBindings{}
			if sr.matchParts(r.patElems, r.patTail, args, nil, b) {
				x := &srExpander{sr: sr, inst: expansionInst.Add(1), renames: map[string]Symbol{}}
				return x.instantiate(r.template, b, false, false)
			}
		}
		return nil, fmt.Errorf("%s: no syntax-rules pattern matches the form", symBase(name))
	}
}

type srExpander struct {
	sr      *SyntaxRules
	inst    uint64
	renames map[string]Symbol
}

func (x *srExpander) rename(s Symbol) Symbol {
	if r, ok := x.renames[string(s)]; ok {
		return r
	}
	r := Symbol(markedName(string(s), x.sr.envID, x.inst))
	x.renames[string(s)] = r
	return r
}

func (x *srExpander) instantiate(tpl Value, b srBindings, quoted, escaped bool) (Value, error) {
	switch t := tpl.(type) {
	case Symbol:
		if n, ok := b[string(t)]; ok {
			if n.isSeq {
				return nil, fmt.Errorf("syntax-rules: pattern variable %s needs an ellipsis in the template", symBase(string(t)))
			}
			return n.leaf, nil
		}
		if !escaped && x.sr.isEllipsis(t) {
			return nil, errors.New("syntax-rules: misplaced ellipsis in template")
		}
		if quoted {
			return t, nil
		}
		return x.rename(t), nil
	case []Value:
		return x.instList(t, nil, b, quoted, escaped)
	case *Pair:
		elems, tail, ok := listParts(t)
		if !ok {
			return tpl, nil
		}
		return x.instList(elems, tail, b, quoted, escaped)
	case Vector:
		out, err := x.instElems(t, b, quoted, escaped)
		if err != nil {
			return nil, err
		}
		return Vector(out), nil
	default:
		return tpl, nil
	}
}

func (x *srExpander) instList(elems []Value, tail Value, b srBindings, quoted, escaped bool) (Value, error) {
	// (<ellipsis> template) — the template with ellipses made literal.
	if !escaped && len(elems) == 2 && tail == nil && x.sr.isEllipsis(elems[0]) {
		return x.instantiate(elems[1], b, quoted, true)
	}
	if len(elems) == 2 && tail == nil {
		if s, ok := elems[0].(Symbol); ok {
			if _, isVar := b[string(s)]; !isVar {
				switch symBase(string(s)) {
				case "quote", "quasiquote":
					if !quoted {
						sub, err := x.instantiate(elems[1], b, true, escaped)
						if err != nil {
							return nil, err
						}
						return []Value{x.rename(s), sub}, nil
					}
				case "unquote", "unquote-splicing":
					if quoted {
						sub, err := x.instantiate(elems[1], b, false, escaped)
						if err != nil {
							return nil, err
						}
						return []Value{s, sub}, nil
					}
				}
			}
		}
	}
	out, err := x.instElems(elems, b, quoted, escaped)
	if err != nil {
		return nil, err
	}
	if tail == nil {
		return out, nil
	}
	tv, err := x.instantiate(tail, b, quoted, escaped)
	if err != nil {
		return nil, err
	}
	return rebuildList(out, tv), nil
}

func (x *srExpander) instElems(elems []Value, b srBindings, quoted, escaped bool) ([]Value, error) {
	out := make([]Value, 0, len(elems))
	i := 0
	for i < len(elems) {
		el := elems[i]
		n := 0
		if !escaped {
			for j := i + 1; j < len(elems) && x.sr.isEllipsis(elems[j]); j++ {
				n++
			}
		}
		if n > 0 {
			pieces, err := x.instRepeat(el, b, quoted, n)
			if err != nil {
				return nil, err
			}
			out = append(out, pieces...)
			i += 1 + n
		} else {
			v, err := x.instantiate(el, b, quoted, escaped)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			i++
		}
	}
	return out, nil
}

func (x *srExpander) instRepeat(el Value, b srBindings, quoted bool, depth int) ([]Value, error) {
	vars := x.seqVars(el, b, nil)
	if len(vars) == 0 {
		return nil, errors.New("syntax-rules: no pattern variable to repeat before ellipsis")
	}
	length := -1
	for _, v := range vars {
		n := len(b[v].seq)
		if length < 0 {
			length = n
		} else if n != length {
			return nil, errors.New("syntax-rules: mismatched repetition counts in template")
		}
	}
	var out []Value
	for k := 0; k < length; k++ {
		sub := make(srBindings, len(b))
		for name, node := range b {
			sub[name] = node
		}
		for _, v := range vars {
			sub[v] = b[v].seq[k]
		}
		if depth > 1 {
			pieces, err := x.instRepeat(el, sub, quoted, depth-1)
			if err != nil {
				return nil, err
			}
			out = append(out, pieces...)
		} else {
			v, err := x.instantiate(el, sub, quoted, false)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
	}
	return out, nil
}

func (x *srExpander) seqVars(el Value, b srBindings, acc []string) []string {
	switch t := el.(type) {
	case Symbol:
		if n, ok := b[string(t)]; ok && n.isSeq {
			acc = append(acc, string(t))
		}
	case []Value:
		for _, e := range t {
			acc = x.seqVars(e, b, acc)
		}
	case *Pair:
		if elems, tail, ok := listParts(t); ok {
			for _, e := range elems {
				acc = x.seqVars(e, b, acc)
			}
			if tail != nil {
				acc = x.seqVars(tail, b, acc)
			}
		}
	case Vector:
		for _, e := range t {
			acc = x.seqVars(e, b, acc)
		}
	}
	return acc
}

func syntaxBuiltins(env *Environment) {
	Register("syntax-rules", "a macro transformer: (syntax-rules (literal...) (pattern template)...)", CommandMeta{})
	env.SetSpecialForm("syntax-rules", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		return parseSyntaxRules(args, env)
	})

	Register("define-syntax", "define a hygienic macro: (define-syntax name (syntax-rules ...))", CommandMeta{})
	env.SetSpecialForm("define-syntax", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("define-syntax expects 2 arguments: (define-syntax name transformer)")
		}
		name, ok := args[0].(Symbol)
		if !ok {
			return nil, errors.New("define-syntax: name must be a symbol")
		}
		v, err := e.Eval(args[1], env)
		if err != nil {
			return nil, err
		}
		sr, ok := v.(*SyntaxRules)
		if !ok {
			return nil, errors.New("define-syntax expects a syntax-rules transformer")
		}
		env.SetMacro(string(name), sr.macro(string(name)))
		return name, nil
	})

	Register("let-syntax", "bind macros for a body: (let-syntax ((name (syntax-rules ...))) body...)", CommandMeta{})
	env.setCompiler("let-syntax", "let-syntax", cfLetSyntax) // compile_forms.go
	Register("letrec-syntax", "let-syntax whose transformers may reference each other", CommandMeta{})
	env.setCompiler("letrec-syntax", "letrec-syntax", cfLetrecSyntax)
}
