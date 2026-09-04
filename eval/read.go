package eval

import (
	"fmt"
	"strings"
)

func readError(msg string) error {
	return &raisedError{value: &ErrorObject{Message: msg, Kind: "read"}}
}

func scanDatum(p *Port) (string, bool, error) {
	for {
		r, eof, err := p.readRune()
		if err != nil {
			return "", false, err
		}
		if eof {
			return "", false, nil
		}
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			continue
		case r == ';':
			if err := skipLineComment(p); err != nil {
				return "", false, err
			}
			continue
		case r == '#':
			r2, eof, err := p.readRune()
			if err != nil {
				return "", false, err
			}
			if eof {
				return "", false, readError("unexpected end of input after #")
			}
			switch r2 {
			case '|':
				if err := skipBlockComment(p); err != nil {
					return "", false, err
				}
				continue
			case ';':
				// Datum comment: scan and discard the next datum.
				_, found, err := scanDatum(p)
				if err != nil {
					return "", false, err
				}
				if !found {
					return "", false, readError("#; with no following datum")
				}
				continue
			case '!':
				word, err := scanDirectiveWord(p)
				if err != nil {
					return "", false, err
				}
				switch word {
				case "fold-case":
					p.foldCase = true
				case "no-fold-case":
					p.foldCase = false
				default:
					return "", false, readError("unknown directive #!" + word)
				}
				continue
			default:
				var sb strings.Builder
				sb.WriteRune('#')
				if err := scanDatumBody(p, &sb, r2); err != nil {
					return "", false, err
				}
				return sb.String(), true, nil
			}
		case r == ')' || r == '}' || r == ']':
			return "", false, readError(fmt.Sprintf("unexpected %c", r))
		default:
			var sb strings.Builder
			if err := scanDatumBody(p, &sb, r); err != nil {
				return "", false, err
			}
			return sb.String(), true, nil
		}
	}
}

func scanDatumBody(p *Port, sb *strings.Builder, first rune) error {
	switch first {
	case '(', '{', '[':
		sb.WriteRune(first)
		return scanBalanced(p, sb, 1)
	case '"':
		sb.WriteRune('"')
		return scanDelimited(p, sb, '"')
	case '|':
		sb.WriteRune('|')
		return scanDelimited(p, sb, '|')
	case '\'', '`':
		sb.WriteRune(first)
		return scanFollowingDatum(p, sb)
	case ',':
		sb.WriteRune(',')
		r, eof, err := p.readRune()
		if err != nil {
			return err
		}
		if eof {
			return readError("unexpected end of input after ,")
		}
		if r == '@' {
			sb.WriteRune('@')
		} else if err := p.br.UnreadRune(); err != nil {
			return err
		}
		return scanFollowingDatum(p, sb)
	case '\\':
		sb.WriteRune('\\')
		r, eof, err := p.readRune()
		if err != nil {
			return err
		}
		if eof {
			return readError("unexpected end of input in character literal")
		}
		sb.WriteRune(r)
		return scanAtomTail(p, sb)
	default:
		if !isAtomRune(first) {
			return readError(fmt.Sprintf("unexpected %c", first))
		}
		sb.WriteRune(first)
		if err := scanAtomTail(p, sb); err != nil {
			return err
		}
		text := sb.String()
		if text == "#u8" {
			r, eof, err := p.readRune()
			if err != nil {
				return err
			}
			if eof {
				return readError("unexpected end of input after " + text)
			}
			if r != '(' {
				return readError("invalid token " + text + string(r))
			}
			sb.WriteRune('(')
			return scanBalanced(p, sb, 1)
		}
		if isDatumLabelDef(text) {
			return scanFollowingDatum(p, sb)
		}
		return nil
	}
}

func scanFollowingDatum(p *Port, sb *strings.Builder) error {
	text, found, err := scanDatum(p)
	if err != nil {
		return err
	}
	if !found {
		return readError("unexpected end of input: incomplete datum")
	}
	sb.WriteByte(' ')
	sb.WriteString(text)
	return nil
}

func scanBalanced(p *Port, sb *strings.Builder, depth int) error {
	for depth > 0 {
		r, eof, err := p.readRune()
		if err != nil {
			return err
		}
		if eof {
			return readError("unexpected end of input: unterminated list")
		}
		switch r {
		case '(', '{', '[':
			sb.WriteRune(r)
			depth++
		case ')', '}', ']':
			sb.WriteRune(r)
			depth--
		case '"':
			sb.WriteRune('"')
			if err := scanDelimited(p, sb, '"'); err != nil {
				return err
			}
		case '|':
			sb.WriteRune('|')
			if err := scanDelimited(p, sb, '|'); err != nil {
				return err
			}
		case ';':
			sb.WriteRune(';')
			if err := copyLineComment(p, sb); err != nil {
				return err
			}
		case '#':
			sb.WriteRune('#')
			r2, eof, err := p.readRune()
			if err != nil {
				return err
			}
			if eof {
				return readError("unexpected end of input after #")
			}
			switch r2 {
			case '\\':
				sb.WriteRune('\\')
				r3, eof, err := p.readRune()
				if err != nil {
					return err
				}
				if eof {
					return readError("unexpected end of input in character literal")
				}
				sb.WriteRune(r3) // even a delimiter: #\)
			case '|':
				sb.WriteRune('|')
				if err := copyBlockComment(p, sb); err != nil {
					return err
				}
			case '(':
				sb.WriteRune('(')
				depth++
			case ';':
				sb.WriteRune(';')
			default:
				if err := p.br.UnreadRune(); err != nil {
					return err
				}
			}
		default:
			sb.WriteRune(r)
		}
	}
	return nil
}

func scanDelimited(p *Port, sb *strings.Builder, close rune) error {
	for {
		r, eof, err := p.readRune()
		if err != nil {
			return err
		}
		if eof {
			return readError(fmt.Sprintf("unexpected end of input: unterminated %c...%c", close, close))
		}
		sb.WriteRune(r)
		if r == '\\' {
			r2, eof, err := p.readRune()
			if err != nil {
				return err
			}
			if eof {
				return readError("unexpected end of input after \\")
			}
			sb.WriteRune(r2)
			continue
		}
		if r == close {
			return nil
		}
	}
}

func scanAtomTail(p *Port, sb *strings.Builder) error {
	for {
		r, eof, err := p.readRune()
		if err != nil {
			return err
		}
		if eof {
			return nil
		}
		if !isAtomRune(r) {
			return p.br.UnreadRune()
		}
		sb.WriteRune(r)
	}
}

func scanDirectiveWord(p *Port) (string, error) {
	var sb strings.Builder
	if err := scanAtomTail(p, &sb); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func skipLineComment(p *Port) error {
	for {
		r, eof, err := p.readRune()
		if err != nil {
			return err
		}
		if eof || r == '\n' {
			return nil
		}
	}
}

func skipBlockComment(p *Port) error {
	var sb strings.Builder
	return copyBlockComment(p, &sb)
}

func copyBlockComment(p *Port, sb *strings.Builder) error {
	depth := 1
	var prev rune
	for depth > 0 {
		r, eof, err := p.readRune()
		if err != nil {
			return err
		}
		if eof {
			return readError("unexpected end of input: unterminated block comment")
		}
		sb.WriteRune(r)
		switch {
		case prev == '#' && r == '|':
			depth++
			r = 0 // consume the pair
		case prev == '|' && r == '#':
			depth--
			r = 0
		}
		prev = r
	}
	return nil
}

func copyLineComment(p *Port, sb *strings.Builder) error {
	for {
		r, eof, err := p.readRune()
		if err != nil {
			return err
		}
		if eof {
			return nil
		}
		sb.WriteRune(r)
		if r == '\n' {
			return nil
		}
	}
}

func isAtomRune(r rune) bool {
	if r > 127 {
		return true
	}
	return isAtomChar(byte(r))
}

func foldTokens(tokens []string) {
	for i, t := range tokens {
		if t == "" || strings.HasPrefix(t, "\"") || strings.HasPrefix(t, "|") || strings.HasPrefix(t, "#\\") {
			continue
		}
		tokens[i] = foldString(t)
	}
}

func readFromPort(p *Port) (Value, error) {
	if err := p.checkRead("read", false); err != nil {
		return nil, err
	}
	text, found, err := scanDatum(p)
	if err != nil {
		return nil, err
	}
	if !found {
		return theEOFObject, nil
	}
	tokens := tokenize(text)
	if p.foldCase {
		foldTokens(tokens)
	}
	if len(tokens) == 0 {
		return theEOFObject, nil
	}
	v, rest, err := parseTokens(tokens)
	if err != nil {
		return nil, readError(err.Error())
	}
	if len(rest) > 0 {
		return nil, readError(fmt.Sprintf("unexpected trailing tokens: %v", rest))
	}
	return v, nil
}

func readBuiltins(env *Environment, curIn *Parameter) {
	env.SetBuiltin("read", "read one datum from a textual input port, or the eof object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) > 1 {
			return nil, fmt.Errorf("read expects at most 1 argument: a port")
		}
		p, err := optPort("read", args, 0, curIn)
		if err != nil {
			return nil, err
		}
		return readFromPort(p)
	}))
}
