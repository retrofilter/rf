package eval

import (
	"bytes"
	"regexp"
	"regexp/syntax"
	"strings"
)

type grepMatcher struct {
	re       *regexp.Regexp
	literal  string
	litBytes []byte // literal as bytes, for the []byte match path
}

func compileGrepMatcher(pattern string) (*grepMatcher, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	tree, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return &grepMatcher{re: re}, nil
	}

	if tree.Op == syntax.OpLiteral && tree.Flags&syntax.FoldCase == 0 {
		lit := string(tree.Rune)
		return &grepMatcher{literal: lit, litBytes: []byte(lit)}, nil
	}
	lit := requiredLiteral(tree)
	return &grepMatcher{re: re, literal: lit, litBytes: []byte(lit)}, nil
}

func (m *grepMatcher) MatchString(s string) bool {
	if m.re == nil {
		return strings.Contains(s, m.literal)
	}
	if len(m.literal) > 0 && !strings.Contains(s, m.literal) {
		return false
	}
	return m.re.MatchString(s)
}

func (m *grepMatcher) Match(b []byte) bool {
	if m.re == nil {
		return bytes.Contains(b, m.litBytes)
	}
	if len(m.litBytes) > 0 && !bytes.Contains(b, m.litBytes) {
		return false
	}
	return m.re.Match(b)
}

func requiredLiteral(re *syntax.Regexp) string {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 {
			return ""
		}
		return string(re.Rune)

	case syntax.OpConcat:
		// All children are required; pick the best-scoring literal.
		var best string
		for _, sub := range re.Sub {
			if cand := requiredLiteral(sub); literalScore(cand) > literalScore(best) {
				best = cand
			}
		}
		return best

	case syntax.OpCapture:
		if len(re.Sub) == 1 {
			return requiredLiteral(re.Sub[0])
		}

	case syntax.OpPlus:
		// x+ requires at least one x.
		if len(re.Sub) == 1 {
			return requiredLiteral(re.Sub[0])
		}

	case syntax.OpRepeat:
		// {n,m} with n >= 1 requires at least one match.
		if re.Min >= 1 && len(re.Sub) == 1 {
			return requiredLiteral(re.Sub[0])
		}
	}
	return ""
}

func literalScore(lit string) int {
	if lit == "" {
		return -1
	}
	class := 2 // uppercase, digits, punctuation: rare
	switch b := lit[0]; {
	case b == ' ' || b == '\t' || strings.ContainsRune("etaoinsrhld", rune(b)):
		class = 0 // whitespace and the most common English/code letters
	case b >= 'a' && b <= 'z':
		class = 1
	}
	n := len(lit)
	if n > 15 {
		n = 15
	}
	return class<<4 | n
}
