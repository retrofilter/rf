package eval

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

var processStart = time.Now()

func processContextBuiltins(env *Environment, ev *Evaluator) {
	env.SetBuiltin("command-line", "the process's command line as a list of strings", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("command-line expects no arguments")
		}
		out := make([]Value, len(os.Args))
		for i, a := range os.Args {
			out[i] = String(a)
		}
		return out, nil
	}))

	exitFn := func(name string) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if err := ev.RequireUser(name); err != nil {
				return nil, err
			}
			if len(args) > 1 {
				return nil, fmt.Errorf("%s expects at most 1 argument: an exit value", name)
			}
			code := 0
			if len(args) == 1 {
				switch v := args[0].(type) {
				case bool:
					if !v {
						code = 1
					}
				case Integer:
					code = int(v)
				default:
					return nil, fmt.Errorf("%s expects a boolean or exact integer exit value", name)
				}
			}
			os.Exit(code)
			return nil, nil // unreachable
		}
	}
	env.SetBuiltin("exit", "terminate the shell process with an exit value (user-only)", exitFn("exit"))
	env.SetBuiltin("emergency-exit", "terminate the shell process immediately (user-only)", exitFn("emergency-exit"))

	env.SetBuiltin("get-environment-variable", "one environment variable's value, or #f when unset (secrets redacted for the assistant)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("get-environment-variable expects 1 argument: a name string")
		}
		name, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("get-environment-variable expects a name string")
		}
		val, found := os.LookupEnv(name)
		if !found {
			return false, nil
		}
		if ev.Caller() == CallerAssistant && secretEnvName.MatchString(name) {
			return String(redactedEnvValue), nil
		}
		return String(val), nil
	}))

	env.SetBuiltin("get-environment-variables", "the environment as an alist of (name . value) pairs (secrets redacted for the assistant)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("get-environment-variables expects no arguments")
		}
		redact := ev.Caller() == CallerAssistant
		kvs := os.Environ()
		sort.Strings(kvs)
		out := make([]Value, 0, len(kvs))
		for _, kv := range kvs {
			k, v, found := strings.Cut(kv, "=")
			if !found {
				continue
			}
			if redact && secretEnvName.MatchString(k) {
				v = redactedEnvValue
			}
			out = append(out, &Pair{Car: String(k), Cdr: String(v)})
		}
		return out, nil
	}))

	env.SetBuiltin("current-second", "the current time as inexact seconds since the epoch", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("current-second expects no arguments")
		}
		return Number(float64(time.Now().UnixNano()) / 1e9), nil
	}))
	env.SetBuiltin("current-jiffy", "elapsed process time in jiffies (nanoseconds), an exact integer", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("current-jiffy expects no arguments")
		}
		return Integer(time.Since(processStart).Nanoseconds()), nil
	}))
	env.SetBuiltin("jiffies-per-second", "jiffies in one second (1e9: jiffies are nanoseconds)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("jiffies-per-second expects no arguments")
		}
		return Integer(1_000_000_000), nil
	}))
}
