package eval

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseOptions splits args into positional arguments and the declared
// options of the named builtin. Unknown keys and kind mismatches error
// naming the supported set; a boolean option given #f counts as unset.
func ParseOptions(name string, args []Value) ([]Value, map[string]Value, error) {
	registryMu.RLock()
	table := registry[name].meta.Options
	registryMu.RUnlock()

	var pos []Value
	opts := map[string]Value{}
	for _, arg := range args {
		switch a := arg.(type) {
		case Keyword:
			opt, ok := findOption(table, string(a))
			if !ok {
				return nil, nil, unknownOption(name, string(a), table)
			}
			if opt.Kind != OptionBool {
				return nil, nil, fmt.Errorf("%s: :%s takes a value — spell it in the options dict: {:%s %s}",
					name, opt.Long, opt.Long, placeholderOr(opt, "..."))
			}
			opts[opt.Long] = true
		case Dictionary:
			for k, v := range a {
				opt, ok := findOption(table, k)
				if !ok {
					return nil, nil, unknownOption(name, k, table)
				}
				val, err := coerceOption(name, opt, v)
				if err != nil {
					return nil, nil, err
				}
				if val == nil { // boolean #f — unset
					continue
				}
				opts[opt.Long] = val
			}
		default:
			pos = append(pos, arg)
		}
	}
	return pos, opts, nil
}

func placeholderOr(opt Option, def string) string {
	if opt.Placeholder != "" {
		return opt.Placeholder
	}
	return def
}

func findOption(table []Option, long string) (Option, bool) {
	for _, o := range table {
		if o.Long == long {
			return o, true
		}
	}
	return Option{}, false
}

func coerceOption(name string, opt Option, v Value) (Value, error) {
	switch opt.Kind {
	case OptionBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s: :%s is a flag — use #t or #f", name, opt.Long)
		}
		if !b {
			return nil, nil
		}
		return true, nil
	case OptionString:
		s, ok := v.(String)
		if !ok {
			return nil, fmt.Errorf("%s: :%s expects a string", name, opt.Long)
		}
		return s, nil
	case OptionInt:
		if n, ok := optionInt(v); ok {
			return Integer(n), nil
		}
		return nil, fmt.Errorf("%s: :%s expects a number", name, opt.Long)
	case OptionNumber:
		if f, ok := optionFloat(v); ok {
			return Number(f), nil
		}
		return nil, fmt.Errorf("%s: :%s expects a number", name, opt.Long)
	}
	return nil, fmt.Errorf("%s: :%s has an undeclared kind", name, opt.Long)
}

func optionInt(v Value) (int, bool) {
	if n, ok := numIndex(v); ok {
		return n, true
	}
	if s, ok := v.(String); ok {
		if n, err := strconv.Atoi(string(s)); err == nil {
			return n, true
		}
	}
	return 0, false
}

func optionFloat(v Value) (float64, bool) {
	switch n := v.(type) {
	case Integer:
		return float64(n), true
	case Number:
		return float64(n), true
	case String:
		if f, err := strconv.ParseFloat(string(n), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func unknownOption(name, key string, table []Option) error {
	if len(table) == 0 {
		return fmt.Errorf("%s: unknown option :%s (%s takes no options)", name, key, name)
	}
	longs := make([]string, len(table))
	for i, o := range table {
		longs[i] = ":" + o.Long
	}
	return fmt.Errorf("%s: unknown option :%s (supported: %s)", name, key, strings.Join(longs, " "))
}

// OptString, OptInt, OptNumber, OptBool read one parsed option with a
// default — the builtin side of ParseOptions' map.
func OptString(opts map[string]Value, long, def string) string {
	if v, ok := opts[long]; ok {
		return string(v.(String))
	}
	return def
}

func OptInt(opts map[string]Value, long string, def int) int {
	if v, ok := opts[long]; ok {
		return int(v.(Integer))
	}
	return def
}

func OptNumber(opts map[string]Value, long string, def float64) float64 {
	if v, ok := opts[long]; ok {
		return float64(v.(Number))
	}
	return def
}

func OptBool(opts map[string]Value, long string) bool {
	_, ok := opts[long]
	return ok
}
