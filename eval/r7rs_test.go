package eval

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestR7RS(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "r7rs-tests.scm"))
	if err != nil {
		t.Fatalf("read suite: %v", err)
	}
	forms := splitTopLevelForms(string(src))
	if len(forms) < 500 {
		t.Fatalf("splitter produced only %d top-level forms — scanner is broken", len(forms))
	}

	timer := time.AfterFunc(60*time.Second, Interrupt)
	defer timer.Stop()
	defer ClearInterrupt()

	ev := NewEvaluator()
	c := newR7RSCollector()
	registerR7RSTestForms(ev.globalEnv, c)

	for _, form := range forms {
		exprs, err := ParseAll(form)
		if err != nil {
			c.current().parseFails++
			continue
		}
		for _, expr := range exprs {
			if _, err := ev.Eval(expr, ev.globalEnv); err != nil {
				c.current().setupErrs++
				break
			}
		}
	}

	statusPath := filepath.Join("testdata", "r7rs_status.txt")
	if os.Getenv("RF_UPDATE_R7RS") != "" {
		if err := os.WriteFile(statusPath, []byte(c.statusFile()), 0o644); err != nil {
			t.Fatalf("write status: %v", err)
		}
		t.Logf("ratchet updated:\n%s", c.statusFile())
		return
	}

	recorded, err := readR7RSStatus(statusPath)
	if err != nil {
		t.Fatalf("read ratchet %s: %v (run with RF_UPDATE_R7RS=1 to create it)", statusPath, err)
	}
	total := 0
	for _, name := range c.order {
		sec := c.sections[name]
		total += sec.pass
		old, ok := recorded[name]
		if !ok {
			if sec.pass > 0 {
				t.Errorf("section %q is new with %d passes — regenerate the ratchet (RF_UPDATE_R7RS=1)", name, sec.pass)
			}
			continue
		}
		delete(recorded, name)
		switch {
		case sec.pass < old:
			t.Errorf("REGRESSION in %q: %d passes, ratchet has %d\n%s",
				name, sec.pass, old, strings.Join(sec.failures, "\n"))
		case sec.pass > old:
			t.Errorf("progress in %q: %d passes, ratchet has %d — regenerate the ratchet (RF_UPDATE_R7RS=1)",
				name, sec.pass, old)
		}
	}
	for name, old := range recorded {
		t.Errorf("section %q (ratchet: %d passes) missing from this run — sectioning broke or the suite changed", name, old)
	}
	t.Logf("r7rs conformance: %d tests passing across %d sections", total, len(c.order))
	if testing.Verbose() {
		t.Log("\n" + c.statusFile())
	}
	if os.Getenv("RF_R7RS_FAILURES") != "" {
		for _, name := range c.order {
			sec := c.sections[name]
			if sec.fail == 0 && sec.parseFails == 0 && sec.setupErrs == 0 {
				continue
			}
			t.Logf("%s: %d fail, %d parse-fail, %d setup-err\n%s",
				name, sec.fail, sec.parseFails, sec.setupErrs, strings.Join(sec.failures, "\n"))
		}
	}
}

func splitTopLevelForms(src string) []string {
	var forms []string
	n := len(src)
	i := 0
	start := -1 // start of the current form; -1 between forms
	depth := 0
	flush := func(end int) {
		if start >= 0 {
			if form := strings.TrimSpace(src[start:end]); form != "" {
				forms = append(forms, form)
			}
			start = -1
		}
	}
	for i < n {
		c := src[i]
		switch {
		case c == ';':
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '#' && i+1 < n && src[i+1] == '|': // nested block comment
			bd := 1
			i += 2
			for i < n && bd > 0 {
				if src[i] == '#' && i+1 < n && src[i+1] == '|' {
					bd++
					i += 2
				} else if src[i] == '|' && i+1 < n && src[i+1] == '#' {
					bd--
					i += 2
				} else {
					i++
				}
			}
		case c == '#' && i+1 < n && src[i+1] == ';': // datum comment: prefixes the next datum, balance continues
			if start < 0 {
				start = i
			}
			i += 2
		case c == '#' && i+1 < n && src[i+1] == '\\': // character literal, possibly #\( or #\newline or #\x41
			if start < 0 {
				start = i
			}
			i += 2
			if i < n {
				i++ // the character itself, even when it is a delimiter
			}
			for i < n && isAtomChar(src[i]) {
				i++ // named / hex tail
			}
			if depth == 0 {
				flush(i)
			}
		case c == '"': // string literal, backslash escapes
			if start < 0 {
				start = i
			}
			i++
			for i < n {
				if src[i] == '\\' {
					i += 2
					continue
				}
				if src[i] == '"' {
					i++
					break
				}
				i++
			}
			if depth == 0 {
				flush(i)
			}
		case c == '|': // |symbol with spaces|
			if start < 0 {
				start = i
			}
			i++
			for i < n {
				if src[i] == '\\' {
					i += 2
					continue
				}
				if src[i] == '|' {
					i++
					break
				}
				i++
			}
		case c == '(' || c == '[' || c == '{':
			if start < 0 {
				start = i
			}
			depth++
			i++
		case c == ')' || c == ']' || c == '}':
			depth--
			i++
			if depth <= 0 {
				flush(i)
				depth = 0
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if depth == 0 {
				flush(i)
			}
			i++
		default:
			if start < 0 {
				start = i
			}
			i++
		}
	}
	flush(n)
	return forms
}

const maxRecordedFailures = 5

type r7rsSection struct {
	name       string
	pass       int
	fail       int
	parseFails int
	setupErrs  int
	failures   []string
}

func (s *r7rsSection) record(ok bool, detail string) {
	if ok {
		s.pass++
		return
	}
	s.fail++
	if len(s.failures) < maxRecordedFailures {
		s.failures = append(s.failures, "  "+detail)
	}
}

type r7rsCollector struct {
	stack    []string
	sections map[string]*r7rsSection
	order    []string
}

func newR7RSCollector() *r7rsCollector {
	return &r7rsCollector{sections: map[string]*r7rsSection{}}
}

func (c *r7rsCollector) current() *r7rsSection {
	name := "(toplevel)"
	if len(c.stack) > 0 {
		name = c.stack[len(c.stack)-1]
	}
	s, ok := c.sections[name]
	if !ok {
		s = &r7rsSection{name: name}
		c.sections[name] = s
		c.order = append(c.order, name)
	}
	return s
}

func (c *r7rsCollector) statusFile() string {
	var b strings.Builder
	b.WriteString("# R7RS conformance ratchet: passes per suite section.\n")
	b.WriteString("# Machine-written by RF_UPDATE_R7RS=1 go test ./eval -run TestR7RS — do not edit.\n")
	total := 0
	for _, name := range c.order {
		s := c.sections[name]
		total += s.pass
		fmt.Fprintf(&b, "%d\t%s\n", s.pass, name)
	}
	fmt.Fprintf(&b, "# total %d\n", total)
	return b.String()
}

func readR7RSStatus(path string) (map[string]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		count, name, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("malformed ratchet line: %q", line)
		}
		var n int
		if _, err := fmt.Sscanf(count, "%d", &n); err != nil {
			return nil, fmt.Errorf("malformed ratchet count in %q", line)
		}
		out[name] = n
	}
	return out, nil
}

func approxEqual(a, b Value) bool {
	if an, ok := a.(Integer); ok {
		bn, ok := b.(Integer)
		return ok && an == bn
	}
	if an, ok := a.(Number); ok {
		bn, ok := b.(Number)
		if !ok {
			return false
		}
		x, y := float64(an), float64(bn)
		if x == y || (math.IsNaN(x) && math.IsNaN(y)) {
			return true
		}
		diff := math.Abs(x - y)
		scale := math.Max(1, math.Max(math.Abs(x), math.Abs(y)))
		return diff <= 1e-9*scale
	}
	al, aok := a.([]Value)
	bl, bok := b.([]Value)
	if aok && bok {
		if len(al) != len(bl) {
			return false
		}
		for i := range al {
			if !approxEqual(al[i], bl[i]) {
				return false
			}
		}
		return true
	}
	amv, aok := a.(*MultipleValues)
	bmv, bok := b.(*MultipleValues)
	if aok && bok {
		if len(amv.Vals) != len(bmv.Vals) {
			return false
		}
		for i := range amv.Vals {
			if !approxEqual(amv.Vals[i], bmv.Vals[i]) {
				return false
			}
		}
		return true
	}
	return deepEqual(a, b)
}

func registerR7RSTestForms(env *Environment, c *r7rsCollector) {
	evalArg := func(e *Evaluator, arg Value, env *Environment) (Value, error) {
		v, err := e.Eval(arg, env)
		if err != nil {
			return nil, err
		}
		return v, nil
	}

	// (test [name] expected expr) / (test-values (values ...) expr)
	testForm := func(formName string) SpecialFormFunc {
		return func(args []Value, env *Environment, e *Evaluator) (Value, error) {
			sec := c.current()
			if len(args) == 3 {
				args = args[1:] // name string — informational only
			}
			if len(args) != 2 {
				sec.record(false, fmt.Sprintf("malformed %s form (%d args)", formName, len(args)))
				return nil, nil
			}
			expected, err := evalArg(e, args[0], env)
			if err != nil {
				sec.record(false, fmt.Sprintf("%s: expected-side error: %v", PrintValue(args[1]), err))
				return nil, nil
			}
			got, err := evalArg(e, args[1], env)
			if err != nil {
				sec.record(false, fmt.Sprintf("%s: %v", PrintValue(args[1]), err))
				return nil, nil
			}
			if approxEqual(expected, got) {
				sec.record(true, "")
			} else {
				sec.record(false, fmt.Sprintf("%s: expected %s, got %s",
					PrintValue(args[1]), PrintValue(expected), PrintValue(got)))
			}
			return nil, nil
		}
	}
	env.SetSpecialForm("test", testForm("test"))
	env.SetSpecialForm("test-values", testForm("test-values"))

	env.SetSpecialForm("test-error", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		sec := c.current()
		if len(args) == 0 {
			sec.record(false, "malformed test-error form")
			return nil, nil
		}
		expr := args[len(args)-1]
		if _, err := e.Eval(expr, env); err != nil {
			sec.record(true, "")
		} else {
			sec.record(false, fmt.Sprintf("%s: expected an error, got none", PrintValue(expr)))
		}
		return nil, nil
	})

	// (test-assert [name] expr) — passes when expr evaluates truthy.
	env.SetSpecialForm("test-assert", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		sec := c.current()
		if len(args) == 0 {
			sec.record(false, "malformed test-assert form")
			return nil, nil
		}
		expr := args[len(args)-1]
		v, err := e.Eval(expr, env)
		if err != nil {
			sec.record(false, fmt.Sprintf("%s: %v", PrintValue(expr), err))
		} else if isFalsy(v) {
			sec.record(false, fmt.Sprintf("%s: asserted false", PrintValue(expr)))
		} else {
			sec.record(true, "")
		}
		return nil, nil
	})

	env.SetSpecialForm("test-begin", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		name := fmt.Sprintf("(unnamed %d)", len(c.sections))
		if len(args) == 1 {
			if s, ok := args[0].(String); ok {
				name = string(s)
			}
		}
		c.stack = append(c.stack, name)
		return nil, nil
	})
	env.SetSpecialForm("test-end", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		if len(c.stack) > 0 {
			c.stack = c.stack[:len(c.stack)-1]
		}
		return nil, nil
	})

	env.setLibrary("chibi test", &librarySpec{preloaded: true})
}
