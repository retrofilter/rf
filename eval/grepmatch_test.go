package eval

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestGrepMatcherAgreesWithRegexp(t *testing.T) {
	patterns := []string{
		"error",
		`foo\.bar`,
		`ERR_[0-9]+`,
		`(?i)warn`,
		`cat|dog`,
		`^INFO`,
		`req(uest)+s`,
		`colou?r`,
		`\bword\b`,
		`x{2,4}y`,
		`[0-9]+ms`,
		``,
	}
	lines := []string{
		"an error occurred",
		"all good here",
		"foo.bar baz",
		"fooXbar baz",
		`literally foo\.bar`,
		"ERR_42: disk full",
		"ERR_: no digits",
		"A WARNING was issued",
		"Warn: mixed case",
		"the dog barked",
		"INFO started",
		"prefix INFO not anchored",
		"requestrequests pile up",
		"color and colour",
		"a word apart",
		"xxxy marks the spot",
		"took 130ms total",
		"",
	}
	for _, pat := range patterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			t.Fatalf("pattern %q: %v", pat, err)
		}
		m, err := compileGrepMatcher(pat)
		if err != nil {
			t.Fatalf("compileGrepMatcher(%q): %v", pat, err)
		}
		for _, line := range lines {
			if got, want := m.MatchString(line), re.MatchString(line); got != want {
				t.Errorf("pattern %q on %q: matcher=%v regexp=%v", pat, line, got, want)
			}
		}
	}
}

func TestGrepMatcherInvalidPattern(t *testing.T) {
	if _, err := compileGrepMatcher("["); err == nil {
		t.Fatal("expected an error for an invalid pattern")
	}
}

func TestGrepMatcherRegimes(t *testing.T) {
	cases := []struct {
		pattern    string
		wantRegex  bool
		wantPrefix string // required-literal pre-filter ("" = none)
	}{
		{"error", false, "error"},       // pure literal: no regex engine
		{`foo\.bar`, false, "foo.bar"},  // escaped literal: parsed runes, not pattern text
		{`ERR_[0-9]+`, true, "ERR_"},    // regex with required literal
		{`^begin`, true, "begin"},       // anchor + literal
		{`(?i)warn`, true, ""},          // fold-case: no case-sensitive pre-filter
		{`cat|dog`, true, ""},           // alternation: no guaranteed literal
		{`(request)+`, true, "request"}, // x+ requires one x
		{`[0-9]+`, true, ""},            // char class only
		{`ERR_[A-Z]+ after [0-9]+ms`, true, "ERR_"},
	}
	for _, c := range cases {
		m, err := compileGrepMatcher(c.pattern)
		if err != nil {
			t.Fatalf("compileGrepMatcher(%q): %v", c.pattern, err)
		}
		if gotRegex := m.re != nil; gotRegex != c.wantRegex {
			t.Errorf("pattern %q: regex engine used = %v, want %v", c.pattern, gotRegex, c.wantRegex)
		}
		if c.wantRegex && m.literal != c.wantPrefix {
			t.Errorf("pattern %q: pre-filter literal = %q, want %q", c.pattern, m.literal, c.wantPrefix)
		}
	}
}

func benchCorpus() []string {
	lines := make([]string, 10000)
	for i := range lines {
		if i%500 == 250 {
			lines[i] = fmt.Sprintf("worker %d: ERR_TIMEOUT after 1500ms retrying", i)
		} else {
			lines[i] = fmt.Sprintf("worker %d: request served path=/api/v1/items/%d status=200 bytes=%d", i, i*7, 1000+i)
		}
	}
	return lines
}

var benchSink bool

func benchmarkMatch(b *testing.B, pattern string, compile func(string) func(string) bool) {
	lines := benchCorpus()
	match := compile(pattern)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, line := range lines {
			benchSink = match(line)
		}
	}
}

func regexpBaseline(pattern string) func(string) bool {
	re := regexp.MustCompile(pattern)
	return re.MatchString
}

func matcherCompile(pattern string) func(string) bool {
	m, err := compileGrepMatcher(pattern)
	if err != nil {
		panic(err)
	}
	return m.MatchString
}

func BenchmarkGrepLiteral_Regexp(b *testing.B)  { benchmarkMatch(b, "ERR_TIMEOUT", regexpBaseline) }
func BenchmarkGrepLiteral_Matcher(b *testing.B) { benchmarkMatch(b, "ERR_TIMEOUT", matcherCompile) }

func BenchmarkGrepPrefilter_Regexp(b *testing.B) {
	benchmarkMatch(b, `ERR_[A-Z]+ after [0-9]+ms`, regexpBaseline)
}
func BenchmarkGrepPrefilter_Matcher(b *testing.B) {
	benchmarkMatch(b, `ERR_[A-Z]+ after [0-9]+ms`, matcherCompile)
}

func BenchmarkGrepMidLiteral_Regexp(b *testing.B) {
	benchmarkMatch(b, `[0-9]+ms retrying`, regexpBaseline)
}
func BenchmarkGrepMidLiteral_Matcher(b *testing.B) {
	benchmarkMatch(b, `[0-9]+ms retrying`, matcherCompile)
}

func BenchmarkGrepNoLiteral_Regexp(b *testing.B) {
	benchmarkMatch(b, `TIMEOUT|FAILURE`, regexpBaseline)
}
func BenchmarkGrepNoLiteral_Matcher(b *testing.B) {
	benchmarkMatch(b, `TIMEOUT|FAILURE`, matcherCompile)
}

func BenchmarkGrepBuiltinStream(b *testing.B) {
	eval := NewEvaluator()
	env := eval.globalEnv
	text := strings.Join(benchCorpus(), "\n")
	env.Set("corpus", String(text))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, err := evalExpr(`(length (grep "ERR_TIMEOUT" (lines corpus)))`, eval, env)
		if err != nil {
			b.Fatal(err)
		}
		if n, ok := v.(Number); !ok || int(n) != 20 {
			b.Fatalf("expected 20 matches, got %v", v)
		}
	}
}
