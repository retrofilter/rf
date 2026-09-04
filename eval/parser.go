package eval

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// String makes Number a fmt.Stringer so every print path agrees: exactly-
// representable integral values print as integers ("2178309", not
// "2.178309e+06"), everything else as the shortest round-tripping decimal.
func (n Number) String() string {
	f := float64(n)
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// Parse parses a single line of Scheme code into a Value (AST)
func Parse(input string) (Value, error) {
	tokens := tokenize(input)
	lt := &labelTable{}
	tokens, err := consumeDatumComments(tokens, lt)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty input")
	}
	ast, rest, err := parseDatum(tokens, lt)
	if err != nil {
		return nil, err
	}
	if err := lt.check(); err != nil {
		return nil, err
	}
	if rest, err = consumeDatumComments(rest, lt); err != nil {
		return nil, err
	}
	if len(rest) > 0 {
		return nil, fmt.Errorf("unexpected tokens: %v", rest)
	}
	return ast, nil
}

// ParseAll parses multiple Scheme expressions from a string.
func ParseAll(input string) ([]Value, error) {
	return parseAllTokens(tokenize(input))
}

func parseAllTokens(tokens []string) ([]Value, error) {
	var expressions []Value
	for {
		lt := &labelTable{}
		tokens2, err := consumeDatumComments(tokens, lt)
		if err != nil {
			return nil, err
		}
		tokens = tokens2
		if len(tokens) == 0 {
			return expressions, nil
		}
		expr, rest, err := parseDatum(tokens, lt)
		if err != nil {
			return nil, err
		}
		if err := lt.check(); err != nil {
			return nil, err
		}
		expressions = append(expressions, expr)
		tokens = rest
	}
}

func tokenize(s string) []string {
	tokens := []string{}
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		// Skip whitespace
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		// Parentheses and braces as single tokens
		if c == '(' || c == ')' || c == '{' || c == '}' || c == '[' || c == ']' {
			tokens = append(tokens, string(c))
			i++
			continue
		}
		// Quote as single token
		if c == '\'' {
			tokens = append(tokens, "'")
			i++
			continue
		}
		// Quasiquote as single token
		if c == '`' {
			tokens = append(tokens, "`")
			i++
			continue
		}
		// Unquote and unquote-splicing as single tokens
		if c == ',' {
			if i+1 < n && s[i+1] == '@' {
				tokens = append(tokens, ",@")
				i += 2
			} else {
				tokens = append(tokens, ",")
				i++
			}
			continue
		}
		if c == '#' && i+1 < n {
			switch s[i+1] {
			case '|': // nested block comment
				depth := 1
				i += 2
				for i < n && depth > 0 {
					if s[i] == '#' && i+1 < n && s[i+1] == '|' {
						depth++
						i += 2
					} else if s[i] == '|' && i+1 < n && s[i+1] == '#' {
						depth--
						i += 2
					} else {
						i++
					}
				}
				continue
			case ';': // datum comment: marker token, parser skips the next datum
				tokens = append(tokens, "#;")
				i += 2
				continue
			case '\\': // character literal: #\ + one rune + any atom tail
				start := i
				i += 2
				if i < n {
					_, size := utf8.DecodeRuneInString(s[i:])
					i += size // the character itself, even when it is a delimiter
				}
				for i < n && isAtomChar(s[i]) {
					i++ // named (#\newline) or hex (#\x41) tail
				}
				tokens = append(tokens, s[start:i])
				continue
			case '(': // vector literal opener
				tokens = append(tokens, "#(")
				i += 2
				continue
			}
			if i+3 < n && s[i+1] == 'u' && s[i+2] == '8' && s[i+3] == '(' { // bytevector opener
				tokens = append(tokens, "#u8(")
				i += 4
				continue
			}
		}
		// |symbol with spaces| — token kept with its pipes
		if c == '|' {
			start := i
			i++
			for i < n {
				if s[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if s[i] == '|' {
					i++
					break
				}
				i++
			}
			tokens = append(tokens, s[start:i])
			continue
		}
		if c == '"' {
			start := i
			i++
			for i < n {
				if s[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if s[i] == '"' {
					i++
					break
				}
				i++
			}
			tokens = append(tokens, s[start:i])
			continue
		}
		// Comment: skip from ; to end of line (only if not in string)
		if c == ';' {
			for i < n && s[i] != '\n' {
				i++
			}
			continue
		}
		// Keyword
		if c == ':' {
			start := i
			i++
			for i < n && (isAtomChar(s[i])) {
				i++
			}
			tokens = append(tokens, s[start:i])
			continue
		}
		// Atom (number, symbol, etc.)
		if isAtomChar(c) {
			start := i
			for i < n && isAtomChar(s[i]) {
				i++
			}
			tokens = append(tokens, s[start:i])
			continue
		}
		// Unknown char, skip
		i++
	}
	return tokens
}

// SchemeComplete reports whether Scheme source is lexically complete: every
// (/{/[ group closed and no unterminated string, |symbol|, or block comment.
func SchemeComplete(s string) bool {
	depth := 0
	n := len(s)
	for i := 0; i < n; i++ {
		switch s[i] {
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		case '"':
			i++
			for i < n && s[i] != '"' {
				if s[i] == '\\' {
					i++
				}
				i++
			}
			if i >= n {
				return false // unterminated string: keep reading
			}
		case '|':
			i++
			for i < n && s[i] != '|' {
				if s[i] == '\\' {
					i++
				}
				i++
			}
			if i >= n {
				return false // unterminated |symbol|
			}
		case ';':
			for i < n && s[i] != '\n' {
				i++
			}
		case '#':
			if i+1 < n && s[i+1] == '|' { // block comment
				bd := 1
				i += 2
				for i < n && bd > 0 {
					if s[i] == '#' && i+1 < n && s[i+1] == '|' {
						bd++
						i += 2
					} else if s[i] == '|' && i+1 < n && s[i+1] == '#' {
						bd--
						i += 2
					} else {
						i++
					}
				}
				if bd > 0 {
					return false // unterminated block comment
				}
				i--
			} else if i+1 < n && s[i+1] == '\\' { // character literal: #\( must not count
				i += 2 // skip the escaped character (byte-wise is enough for delimiters)
			}
		}
	}
	return depth <= 0
}

func isAtomChar(c byte) bool {
	// Allow anything except whitespace and delimiters
	switch c {
	case ' ', '\t', '\n', '\r', '(', ')', '{', '}', '[', ']', '\'', '"', '`', ',', ';':
		return false
	default:
		return true
	}
}

type labelTable struct {
	m       map[int]Value
	created int
	patched int
}

type labelRef struct{ n int }

func (lt *labelTable) check() error {
	if lt.created != lt.patched {
		return fmt.Errorf("undefined datum label")
	}
	return nil
}

func datumLabelTok(tok string) (n int, def, ref bool) {
	if len(tok) < 3 || tok[0] != '#' {
		return 0, false, false
	}
	last := tok[len(tok)-1]
	if last != '=' && last != '#' {
		return 0, false, false
	}
	for _, c := range tok[1 : len(tok)-1] {
		if c < '0' || c > '9' {
			return 0, false, false
		}
		n = n*10 + int(c-'0')
	}
	return n, last == '=', last == '#'
}

func isDatumLabelDef(tok string) bool {
	_, def, _ := datumLabelTok(tok)
	return def
}

func (lt *labelTable) patchLabel(v Value, n int, target Value) {
	visited := map[interface{}]bool{}
	var walk func(v Value)
	replace := func(child Value) (Value, bool) {
		if r, ok := child.(*labelRef); ok && r.n == n {
			lt.patched++
			return target, true
		}
		return child, false
	}
	walk = func(v Value) {
		switch val := v.(type) {
		case []Value:
			if len(val) == 0 || visited[&val[0]] {
				return
			}
			visited[&val[0]] = true
			for i := range val {
				if nv, ok := replace(val[i]); ok {
					val[i] = nv
				} else {
					walk(val[i])
				}
			}
		case Vector:
			if len(val) == 0 || visited[&val[0]] {
				return
			}
			visited[&val[0]] = true
			for i := range val {
				if nv, ok := replace(val[i]); ok {
					val[i] = nv
				} else {
					walk(val[i])
				}
			}
		case *Pair:
			if visited[val] {
				return
			}
			visited[val] = true
			if nv, ok := replace(val.Car); ok {
				val.Car = nv
			} else {
				walk(val.Car)
			}
			if nv, ok := replace(val.Cdr); ok {
				val.Cdr = nv
			} else {
				walk(val.Cdr)
			}
		case Dictionary:
			if visited[reflect.ValueOf(val).Pointer()] {
				return
			}
			visited[reflect.ValueOf(val).Pointer()] = true
			for k, dv := range val {
				if nv, ok := replace(dv); ok {
					val[k] = nv
				} else {
					walk(dv)
				}
			}
		}
	}
	walk(v)
}

func consumeDatumComments(tokens []string, lt *labelTable) ([]string, error) {
	for len(tokens) > 0 && tokens[0] == "#;" {
		if len(tokens) == 1 {
			return nil, fmt.Errorf("#; with no following datum")
		}
		_, rest, err := parseDatum(tokens[1:], lt)
		if err != nil {
			return nil, err
		}
		tokens = rest
	}
	return tokens, nil
}

func parseTokens(tokens []string) (Value, []string, error) {
	lt := &labelTable{}
	v, rest, err := parseDatum(tokens, lt)
	if err != nil {
		return v, rest, err
	}
	if err := lt.check(); err != nil {
		return nil, rest, err
	}
	return v, rest, nil
}

func parseDatum(tokens []string, lt *labelTable) (Value, []string, error) {
	var err error
	tokens, err = consumeDatumComments(tokens, lt)
	if err != nil {
		return nil, tokens, err
	}
	if len(tokens) == 0 {
		return nil, tokens, fmt.Errorf("unexpected EOF")
	}
	tok := tokens[0]
	tokens = tokens[1:]
	switch {
	case tok == "(":
		var list []Value
		for {
			if tokens, err = consumeDatumComments(tokens, lt); err != nil {
				return nil, tokens, err
			}
			if len(tokens) == 0 {
				return nil, tokens, fmt.Errorf("missing )")
			}
			if tokens[0] == ")" {
				break
			}
			if tokens[0] == "." {
				if len(list) == 0 {
					return nil, tokens, fmt.Errorf("unexpected . with no preceding datum")
				}
				if tokens, err = consumeDatumComments(tokens[1:], lt); err != nil {
					return nil, tokens, err
				}
				tail, rest, err := parseDatum(tokens, lt)
				if err != nil {
					return nil, tokens, err
				}
				if rest, err = consumeDatumComments(rest, lt); err != nil {
					return nil, rest, err
				}
				if len(rest) == 0 || rest[0] != ")" {
					return nil, rest, fmt.Errorf("expected ) after dotted tail")
				}
				if s, ok := tail.([]Value); ok {
					return append(list, s...), rest[1:], nil
				}
				var out Value = tail
				for i := len(list) - 1; i >= 0; i-- {
					out = &Pair{Car: list[i], Cdr: out}
				}
				return out, rest[1:], nil
			}
			expr, rest, err := parseDatum(tokens, lt)
			if err != nil {
				return nil, tokens, err
			}
			list = append(list, expr)
			tokens = rest
		}
		if len(list) == 0 {
			list = make([]Value, 0)
		}
		return list, tokens[1:], nil // skip ')'
	case tok == "#(":
		var vec Vector
		for {
			if tokens, err = consumeDatumComments(tokens, lt); err != nil {
				return nil, tokens, err
			}
			if len(tokens) == 0 {
				return nil, tokens, fmt.Errorf("missing ) in vector")
			}
			if tokens[0] == ")" {
				break
			}
			expr, rest, err := parseDatum(tokens, lt)
			if err != nil {
				return nil, tokens, err
			}
			vec = append(vec, expr)
			tokens = rest
		}
		if vec == nil {
			vec = Vector{}
		}
		return vec, tokens[1:], nil
	case tok == "#u8(":
		var bv Bytevector
		for {
			if tokens, err = consumeDatumComments(tokens, lt); err != nil {
				return nil, tokens, err
			}
			if len(tokens) == 0 {
				return nil, tokens, fmt.Errorf("missing ) in bytevector")
			}
			if tokens[0] == ")" {
				break
			}
			expr, rest, err := parseDatum(tokens, lt)
			if err != nil {
				return nil, tokens, err
			}
			n, ok := numIndex(expr)
			if !ok || n < 0 || n > 255 {
				return nil, tokens, fmt.Errorf("bytevector elements must be integers 0-255, got %s", PrintValue(expr))
			}
			bv = append(bv, byte(n))
			tokens = rest
		}
		if bv == nil {
			bv = Bytevector{}
		}
		return bv, tokens[1:], nil
	case tok == ")":
		return nil, tokens, fmt.Errorf("unexpected )")
	case tok == "{":
		dict := Dictionary{}
		for {
			if tokens, err = consumeDatumComments(tokens, lt); err != nil {
				return nil, tokens, err
			}
			if len(tokens) == 0 {
				return nil, tokens, fmt.Errorf("missing } in dictionary")
			}
			if tokens[0] == "}" {
				break
			}
			key, rest, err := parseDatum(tokens, lt)
			if err != nil {
				return nil, tokens, err
			}
			tokens = rest
			if len(tokens) == 0 {
				return nil, tokens, fmt.Errorf("dictionary missing value for key")
			}
			if tokens[0] == "}" || strings.HasPrefix(tokens[0], ":") {
				if kw, ok := key.(Keyword); ok {
					dict[string(kw)] = true
					if tokens[0] == "}" {
						break
					}
					continue
				}
				if tokens[0] == "}" {
					return nil, tokens, fmt.Errorf("dictionary missing value for key")
				}
			}
			value, rest, err := parseDatum(tokens, lt)
			if err != nil {
				return nil, tokens, err
			}
			// Only allow Keyword keys
			kw, ok := key.(Keyword)
			if !ok {
				return nil, tokens, fmt.Errorf("dictionary keys must be keywords")
			}
			dict[string(kw)] = value
			tokens = rest
		}
		return dict, tokens[1:], nil // skip '}'
	case tok == "}":
		return nil, tokens, fmt.Errorf("unexpected }")
	case tok == "'":
		val, rest, err := parseDatum(tokens, lt)
		if err != nil {
			return nil, tokens, err
		}
		return []Value{Symbol("quote"), val}, rest, nil
	case tok == "`":
		val, rest, err := parseDatum(tokens, lt)
		if err != nil {
			return nil, tokens, err
		}
		return []Value{Symbol("quasiquote"), val}, rest, nil
	case tok == ",":
		val, rest, err := parseDatum(tokens, lt)
		if err != nil {
			return nil, tokens, err
		}
		return []Value{Symbol("unquote"), val}, rest, nil
	case tok == ",@":
		val, rest, err := parseDatum(tokens, lt)
		if err != nil {
			return nil, tokens, err
		}
		return []Value{Symbol("unquote-splicing"), val}, rest, nil
	case tok == ".":
		return nil, tokens, fmt.Errorf("unexpected .")
	default:
		if n, def, ref := datumLabelTok(tok); def || ref {
			if ref {
				if v, ok := lt.m[n]; ok {
					return v, tokens, nil
				}
				lt.created++
				return &labelRef{n: n}, tokens, nil
			}
			val, rest, err := parseDatum(tokens, lt)
			if err != nil {
				return nil, rest, err
			}
			lt.patchLabel(val, n, val)
			if lt.m == nil {
				lt.m = map[int]Value{}
			}
			lt.m[n] = val
			return val, rest, nil
		}
		v, err := parseAtom(tok)
		if err != nil {
			return nil, tokens, err
		}
		return v, tokens, nil
	}
}

func parseAtom(tok string) (Value, error) {
	switch tok {
	case "#t", "#true":
		return true, nil
	case "#f", "#false":
		return false, nil
	}
	if strings.HasPrefix(tok, "#\\") {
		return parseCharToken(tok)
	}
	if strings.HasPrefix(tok, ":") {
		return Keyword(tok[1:]), nil
	}
	if strings.HasPrefix(tok, "\"") && strings.HasSuffix(tok, "\"") && len(tok) >= 2 {
		body := tok[1 : len(tok)-1]
		if unescaped, err := schemeUnescape(body); err == nil {
			return String(unescaped), nil
		}
		return String(body), nil
	}
	if strings.HasPrefix(tok, "|") && strings.HasSuffix(tok, "|") && len(tok) >= 2 {
		body := tok[1 : len(tok)-1]
		if unescaped, err := schemeUnescape(body); err == nil {
			return Symbol(unescaped), nil
		}
		return Symbol(body), nil
	}
	if n, ok := parseSchemeNumber(tok); ok {
		return n, nil
	}
	return Symbol(tok), nil
}

func parseSchemeNumber(tok string) (Value, bool) {
	switch tok {
	case "+inf.0":
		return Number(math.Inf(1)), true
	case "-inf.0":
		return Number(math.Inf(-1)), true
	case "+nan.0", "-nan.0":
		return Number(math.NaN()), true
	}
	s := tok
	radix := 0
	exactness := byte(0) // 'e', 'i', or 0 for unspecified
	for len(s) >= 2 && s[0] == '#' {
		switch s[1] | 0x20 {
		case 'b':
			radix = 2
		case 'o':
			radix = 8
		case 'd':
			radix = 10
		case 'x':
			radix = 16
		case 'e', 'i':
			exactness = s[1] | 0x20
		default:
			return nil, false
		}
		s = s[2:]
	}
	if s == "" {
		return nil, false
	}
	switch strings.ToLower(s) {
	case "+inf.0":
		return Number(math.Inf(1)), true
	case "-inf.0":
		return Number(math.Inf(-1)), true
	case "+nan.0", "-nan.0":
		return Number(math.NaN()), true
	}
	applyExactness := func(v Value) Value {
		switch exactness {
		case 'i':
			if inexact, ok := toInexact(v); ok {
				return inexact
			}
		case 'e':
			if exact, err := toExact(v); err == nil {
				return exact
			}
		}
		return v
	}
	if slash := strings.IndexByte(s, '/'); slash > 0 {
		r := radix
		if r == 0 {
			r = 10
		}
		p, perr := strconv.ParseInt(s[:slash], r, 64)
		q, qerr := strconv.ParseInt(s[slash+1:], r, 64)
		if perr != nil || qerr != nil || q <= 0 {
			return nil, false
		}
		if exactness == 'i' {
			return Number(float64(p) / float64(q)), true
		}
		if p%q == 0 {
			return Integer(p / q), true
		}
		return nil, false
	}
	if radix != 0 && radix != 10 {
		neg := false
		if s[0] == '+' {
			s = s[1:]
		} else if s[0] == '-' {
			neg = true
			s = s[1:]
		}
		n, err := strconv.ParseInt(s, radix, 64)
		if err != nil || neg && n == math.MinInt64 {
			u, uerr := strconv.ParseUint(s, radix, 64)
			if uerr != nil {
				return nil, false
			}
			f := float64(u)
			if neg {
				f = -f
			}
			return applyExactness(Number(f)), true
		}
		if neg {
			n = -n
		}
		return applyExactness(Integer(n)), true
	}
	i := 0
	if s[0] == '+' || s[0] == '-' {
		i = 1
	}
	if i >= len(s) || !(s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		return nil, false
	}
	inexactSpelling := false
	marker := -1 // position of an s/f/d/l exponent marker (R6RS-style; all float64 here)
	for j := i; j < len(s); j++ {
		c := s[j] | 0x20
		if c == '.' || c == 'e' {
			inexactSpelling = true
		}
		if c == 's' || c == 'f' || c == 'd' || c == 'l' {
			if marker >= 0 || j == i {
				return nil, false
			}
			marker = j
			inexactSpelling = true
			continue
		}
		if c >= 'a' && c <= 'z' && c != 'e' {
			return nil, false
		}
	}
	if marker >= 0 {
		s = s[:marker] + "e" + s[marker+1:]
	}
	if !inexactSpelling {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return applyExactness(Integer(n)), true
		}
		// Integer literal beyond int64: overflow promotes to inexact (D1).
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, false
	}
	return applyExactness(Number(f)), true
}

var charNames = map[string]rune{
	"alarm":     7,
	"backspace": 8,
	"delete":    127,
	"escape":    27,
	"newline":   10,
	"null":      0,
	"return":    13,
	"space":     32,
	"tab":       9,
}

func parseCharToken(tok string) (Value, error) {
	body := tok[2:] // after #\
	if body == "" {
		return nil, fmt.Errorf("empty character literal")
	}
	if r, size := utf8.DecodeRuneInString(body); size == len(body) {
		return Char(r), nil // single character, including #\( and #\λ
	}
	if body[0] == 'x' || body[0] == 'X' {
		if n, err := strconv.ParseUint(body[1:], 16, 32); err == nil && utf8.ValidRune(rune(n)) {
			return Char(rune(n)), nil
		}
	}
	if r, ok := charNames[body]; ok {
		return Char(r), nil
	}
	return nil, fmt.Errorf("invalid character literal: %s", tok)
}

func schemeUnescape(body string) (string, error) {
	if !strings.ContainsRune(body, '\\') {
		return body, nil
	}
	var b strings.Builder
	i := 0
	n := len(body)
	for i < n {
		c := body[i]
		if c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		i++
		if i >= n {
			return "", fmt.Errorf("trailing backslash")
		}
		switch body[i] {
		case 'a':
			b.WriteByte(7)
			i++
		case 'b':
			b.WriteByte(8)
			i++
		case 't':
			b.WriteByte(9)
			i++
		case 'n':
			b.WriteByte(10)
			i++
		case 'r':
			b.WriteByte(13)
			i++
		case '"':
			b.WriteByte('"')
			i++
		case '\\':
			b.WriteByte('\\')
			i++
		case '|':
			b.WriteByte('|')
			i++
		case 'x', 'X':
			semi := strings.IndexByte(body[i:], ';')
			if semi < 0 {
				return "", fmt.Errorf("unterminated \\x escape")
			}
			code, err := strconv.ParseUint(body[i+1:i+semi], 16, 32)
			if err != nil || !utf8.ValidRune(rune(code)) {
				return "", fmt.Errorf("invalid \\x escape")
			}
			b.WriteRune(rune(code))
			i += semi + 1
		case ' ', '\t', '\r', '\n':
			// \<intraline ws>*<newline><intraline ws>* collapses to nothing
			j := i
			for j < n && (body[j] == ' ' || body[j] == '\t' || body[j] == '\r') {
				j++
			}
			if j >= n || body[j] != '\n' {
				return "", fmt.Errorf("invalid escape before line end")
			}
			j++
			for j < n && (body[j] == ' ' || body[j] == '\t') {
				j++
			}
			i = j
		default:
			return "", fmt.Errorf("unknown escape \\%c", body[i])
		}
	}
	return b.String(), nil
}

func charWriteName(c Char) string {
	for name, r := range charNames {
		if rune(c) == r {
			return "#\\" + name
		}
	}
	if strconv.IsPrint(rune(c)) {
		return "#\\" + string(rune(c))
	}
	return fmt.Sprintf("#\\x%x", rune(c))
}

// PrintValue prints a Value in Scheme style
func PrintValue(v Value) string {
	switch val := v.(type) {
	case Integer:
		return val.String()
	case Number:
		return val.String()
	case Dictionary:
		parts := make([]string, 0, len(val))
		for k, v := range val {
			parts = append(parts, ":"+k+" "+PrintValue(v))
		}
		return "{" + strings.Join(parts, " ") + "}"
	case Keyword:
		return string(val)
	case Symbol:
		return symBase(string(val))
	case String:
		return fmt.Sprintf("\"%s\"", string(val))
	case *MutableString:
		return fmt.Sprintf("\"%s\"", string(val.runes))
	case Char:
		return charWriteName(val)
	case []Value:
		parts := make([]string, len(val))
		for i, elem := range val {
			parts[i] = PrintValue(elem)
		}
		return "(" + strings.Join(parts, " ") + ")"
	case *Pair:
		var sb strings.Builder
		sb.WriteByte('(')
		sb.WriteString(PrintValue(val.Car))
		rest := val.Cdr
		for steps := 0; ; steps++ {
			if steps >= 1<<16 {
				sb.WriteString(" ...")
				break
			}
			if p, ok := rest.(*Pair); ok {
				sb.WriteByte(' ')
				sb.WriteString(PrintValue(p.Car))
				rest = p.Cdr
				continue
			}
			if s, ok := rest.([]Value); ok {
				for _, elem := range s {
					sb.WriteByte(' ')
					sb.WriteString(PrintValue(elem))
				}
				break
			}
			sb.WriteString(" . ")
			sb.WriteString(PrintValue(rest))
			break
		}
		sb.WriteByte(')')
		return sb.String()
	case Vector:
		parts := make([]string, len(val))
		for i, elem := range val {
			parts[i] = PrintValue(elem)
		}
		return "#(" + strings.Join(parts, " ") + ")"
	case Bytevector:
		parts := make([]string, len(val))
		for i, b := range val {
			parts[i] = strconv.Itoa(int(b))
		}
		return "#u8(" + strings.Join(parts, " ") + ")"
	case *Stream:
		return "#<stream>"
	case *Record:
		return printRecord(val)
	case *RecordType:
		return "#<record-type " + recordTypeName(val) + ">"
	case *MultipleValues:
		return printValues(val)
	case *Promise:
		return "#<promise>"
	case *EOFObject:
		return "#<eof>"
	case *Port:
		return val.printForm()
	case *ErrorObject:
		return "#<error " + val.String() + ">"
	case *Continuation:
		return "#<continuation>"
	case *Parameter:
		return "#<parameter>"
	case *CaseLambda:
		return "#<case-lambda>"
	case BuiltinFunc, *FastBuiltin:
		return "#<builtin>"
	case *Environment:
		return "#<environment>"
	default:
		return fmt.Sprintf("%v", val)
	}
}
