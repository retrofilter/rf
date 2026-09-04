package eval

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

type writeMode int

const (
	modeSimple writeMode = iota // no labels (write-simple)
	modeCyclic                  // label cycles only (write, display)
	modeShared                  // label all shared structure (write-shared)
)

func writeNodeKey(v Value) (interface{}, bool) {
	switch val := v.(type) {
	case *Pair:
		return val, true
	case []Value:
		if len(val) > 0 {
			return &val[0], true
		}
	case Vector:
		if len(val) > 0 {
			return &val[0], true
		}
	case Dictionary:
		if len(val) > 0 {
			return reflect.ValueOf(val).Pointer(), true
		}
	}
	return nil, false
}

func markShared(v Value, seen map[interface{}]bool, onPath map[interface{}]bool, marked map[interface{}]bool, shared bool) {
	k, ok := writeNodeKey(v)
	if ok {
		if onPath[k] {
			marked[k] = true
			return
		}
		if seen[k] {
			if shared {
				marked[k] = true
			}
			return
		}
		seen[k] = true
		onPath[k] = true
		defer delete(onPath, k)
	}
	switch val := v.(type) {
	case *Pair:
		markShared(val.Car, seen, onPath, marked, shared)
		markShared(val.Cdr, seen, onPath, marked, shared)
	case []Value:
		for _, e := range val {
			markShared(e, seen, onPath, marked, shared)
		}
	case Vector:
		for _, e := range val {
			markShared(e, seen, onPath, marked, shared)
		}
	case Dictionary:
		for _, e := range val {
			markShared(e, seen, onPath, marked, shared)
		}
	}
}

type writeState struct {
	sb      strings.Builder
	display bool
	marked  map[interface{}]bool
	labels  map[interface{}]int
	next    int
}

func writeForm(v Value, mode writeMode, display bool) string {
	w := &writeState{display: display, marked: map[interface{}]bool{}, labels: map[interface{}]int{}}
	if mode != modeSimple {
		markShared(v, map[interface{}]bool{}, map[interface{}]bool{}, w.marked, mode == modeShared)
	}
	w.emit(v)
	return w.sb.String()
}

func (w *writeState) emit(v Value) {
	if k, ok := writeNodeKey(v); ok && w.marked[k] {
		if n, done := w.labels[k]; done {
			fmt.Fprintf(&w.sb, "#%d#", n)
			return
		}
		n := w.next
		w.next++
		w.labels[k] = n
		fmt.Fprintf(&w.sb, "#%d=", n)
	}
	switch val := v.(type) {
	case String:
		if w.display {
			w.sb.WriteString(string(val))
		} else {
			w.sb.WriteString(writeStringLiteral(string(val)))
		}
	case *MutableString:
		if w.display {
			w.sb.WriteString(string(val.runes))
		} else {
			w.sb.WriteString(writeStringLiteral(string(val.runes)))
		}
	case Char:
		if w.display {
			w.sb.WriteRune(rune(val))
		} else {
			w.sb.WriteString(charWriteName(val))
		}
	case Symbol:
		base := symBase(string(val))
		if w.display {
			w.sb.WriteString(base)
		} else {
			w.sb.WriteString(writeSymbol(base))
		}
	case Keyword:
		if w.display {
			w.sb.WriteString(string(val))
		} else {
			w.sb.WriteString(":" + string(val))
		}
	case bool:
		if val {
			w.sb.WriteString("#t")
		} else {
			w.sb.WriteString("#f")
		}
	case Integer:
		w.sb.WriteString(val.String())
	case Number:
		w.sb.WriteString(writeFloat(float64(val)))
	case *Pair:
		w.sb.WriteByte('(')
		w.emit(val.Car)
		w.emitTail(val.Cdr)
		w.sb.WriteByte(')')
	case []Value:
		w.sb.WriteByte('(')
		for i, e := range val {
			if i > 0 {
				w.sb.WriteByte(' ')
			}
			w.emit(e)
		}
		w.sb.WriteByte(')')
	case Vector:
		w.sb.WriteString("#(")
		for i, e := range val {
			if i > 0 {
				w.sb.WriteByte(' ')
			}
			w.emit(e)
		}
		w.sb.WriteByte(')')
	case Dictionary:
		w.sb.WriteByte('{')
		first := true
		for k, e := range val {
			if !first {
				w.sb.WriteByte(' ')
			}
			first = false
			w.sb.WriteString(":" + k + " ")
			w.emit(e)
		}
		w.sb.WriteByte('}')
	default:
		w.sb.WriteString(PrintValue(v))
	}
}

func (w *writeState) emitTail(rest Value) {
	for {
		switch r := rest.(type) {
		case *Pair:
			if k, ok := writeNodeKey(r); ok && w.marked[k] {
				w.sb.WriteString(" . ")
				w.emit(r)
				return
			}
			w.sb.WriteByte(' ')
			w.emit(r.Car)
			rest = r.Cdr
		case []Value:
			if len(r) == 0 {
				return
			}
			if k, ok := writeNodeKey(r); ok && w.marked[k] {
				w.sb.WriteString(" . ")
				w.emit(r)
				return
			}
			for _, e := range r {
				w.sb.WriteByte(' ')
				w.emit(e)
			}
			return
		default:
			w.sb.WriteString(" . ")
			w.emit(rest)
			return
		}
	}
}

func writeFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "+inf.0"
	}
	if math.IsInf(f, -1) {
		return "-inf.0"
	}
	if math.IsNaN(f) {
		return "+nan.0"
	}
	s := Number(f).String()
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

func writeStringLiteral(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString("\\\"")
		case '\\':
			sb.WriteString("\\\\")
		case '\n':
			sb.WriteString("\\n")
		case '\r':
			sb.WriteString("\\r")
		case '\t':
			sb.WriteString("\\t")
		case 7:
			sb.WriteString("\\a")
		case 8:
			sb.WriteString("\\b")
		default:
			if r < 32 || r == 127 {
				fmt.Fprintf(&sb, "\\x%x;", r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func symbolNeedsPipes(s string) bool {
	if s == "" || s == "." {
		return true
	}
	if _, ok := parseSchemeNumber(s); ok {
		return true
	}
	low := strings.ToLower(s)
	if low == "+i" || low == "-i" {
		return true
	}
	for _, pre := range []string{"+inf.0", "-inf.0", "+nan.0", "-nan.0"} {
		if strings.HasPrefix(low, pre) {
			return true
		}
	}
	if s[0] == '#' || s[0] == ':' {
		return true
	}
	for _, r := range s {
		if r <= ' ' || r == 127 {
			return true
		}
		switch r {
		case '(', ')', '[', ']', '{', '}', '"', '\'', '`', ',', ';', '|', '\\':
			return true
		}
	}
	return false
}

func writeSymbol(s string) string {
	if !symbolNeedsPipes(s) {
		return s
	}
	escaped := strings.ReplaceAll(s, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "|", "\\|")
	return "|" + escaped + "|"
}

func writeBuiltins(env *Environment, curOut *Parameter) {
	writeOp := func(name string, mode writeMode, display bool) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) < 1 || len(args) > 2 {
				return nil, fmt.Errorf("%s expects (%s obj [port])", name, name)
			}
			p, err := optPort(name, args, 1, curOut)
			if err != nil {
				return nil, err
			}
			obj := args[0]
			if display {
				if obj, err = Materialize(obj); err != nil {
					return nil, err
				}
			}
			return nil, p.writeText(name, writeForm(obj, mode, display))
		}
	}
	env.SetBuiltin("write", "write a value in reader syntax (cycles as datum labels): (write obj [port])", writeOp("write", modeCyclic, false))
	env.SetBuiltin("write-simple", "write without shared-structure labels", writeOp("write-simple", modeSimple, false))
	env.SetBuiltin("write-shared", "write with labels for all shared structure", writeOp("write-shared", modeShared, false))
	env.SetBuiltin("display", "print a value's content form: (display obj [port])", writeOp("display", modeCyclic, true))

	env.SetBuiltin("newline", "write a newline: (newline [port])", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) > 1 {
			return nil, fmt.Errorf("newline expects at most 1 argument: a port")
		}
		p, err := optPort("newline", args, 0, curOut)
		if err != nil {
			return nil, err
		}
		return nil, p.writeText("newline", "\n")
	}))

	env.SetBuiltin("print", "print each argument on its own line; returns nil", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, ok := curOut.current().(*Port)
		if !ok {
			return nil, fmt.Errorf("print: current-output-port is not a port")
		}
		for _, arg := range args {
			v, err := Materialize(arg)
			if err != nil {
				return nil, err
			}
			if err := p.writeText("print", writeForm(v, modeCyclic, true)+"\n"); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}))
}
