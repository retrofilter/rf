package eval

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

type librarySpec struct {
	preloaded bool
	exports   map[string]string
}

var standardLibraries = []string{
	"scheme base", "scheme case-lambda", "scheme char", "scheme complex",
	"scheme cxr", "scheme eval", "scheme file", "scheme inexact",
	"scheme lazy", "scheme load", "scheme process-context", "scheme r5rs",
	"scheme read", "scheme repl", "scheme time", "scheme write",
}

func featureNames() []string {
	feats := []string{"r7rs", "ieee-float", "full-unicode", "retrofilter"}
	switch runtime.GOOS {
	case "darwin":
		feats = append(feats, "posix", "unix", "darwin", "bsd")
	case "linux":
		feats = append(feats, "posix", "unix", "gnu-linux")
	case "windows":
		feats = append(feats, "windows")
	default:
		feats = append(feats, "posix", "unix")
	}
	switch runtime.GOARCH {
	case "amd64":
		feats = append(feats, "x86-64", "little-endian", "lp64")
	case "arm64":
		feats = append(feats, "aarch64", "little-endian", "lp64")
	}
	return feats
}

func rootOf(env *Environment) *Environment {
	for env.parent != nil {
		env = env.parent
	}
	return env
}

func (env *Environment) lookupLibrary(key string) *librarySpec {
	return rootOf(env).libraries[key]
}

func (env *Environment) setLibrary(key string, spec *librarySpec) {
	root := rootOf(env)
	if root.libraries == nil {
		root.libraries = make(map[string]*librarySpec)
	}
	root.libraries[key] = spec
}

func formParts(v Value) ([]Value, bool) {
	switch f := v.(type) {
	case []Value:
		return f, true
	case *Pair:
		if s, err := pairToSlice(f); err == nil {
			return s, true
		}
	}
	return nil, false
}

func libraryNameKey(v Value) (string, error) {
	parts, ok := formParts(v)
	if !ok || len(parts) == 0 {
		return "", fmt.Errorf("expected a library name like (scheme base), got %s", PrintValue(v))
	}
	key := ""
	for i, p := range parts {
		if i > 0 {
			key += " "
		}
		switch e := p.(type) {
		case Symbol:
			key += symBase(string(e))
		case Integer:
			if e < 0 {
				return "", fmt.Errorf("library name parts must be identifiers or unsigned integers, got %s", PrintValue(p))
			}
			key += e.String()
		default:
			return "", fmt.Errorf("library name parts must be identifiers or unsigned integers, got %s", PrintValue(p))
		}
	}
	return key, nil
}

type importBinding struct {
	local, internal string
}

func resolveImportSet(env *Environment, set Value) ([]importBinding, bool, error) {
	parts, ok := formParts(set)
	if !ok || len(parts) == 0 {
		return nil, false, fmt.Errorf("import: expected an import set, got %s", PrintValue(set))
	}
	head, _ := parts[0].(Symbol)
	switch symBase(string(head)) {
	case "only", "except", "prefix", "rename":
		if len(parts) < 2 {
			return nil, false, fmt.Errorf("import: malformed %s set", symBase(string(head)))
		}
		bindings, preloaded, err := resolveImportSet(env, parts[1])
		if err != nil {
			return nil, false, err
		}
		mod := symBase(string(head))
		if preloaded {
			if mod == "prefix" || mod == "rename" {
				return nil, false, fmt.Errorf("import: %s over a built-in library is not supported (thin library layer — built-in bindings are global already)", mod)
			}
			return nil, true, nil
		}
		return applyImportModifier(mod, bindings, parts[2:])
	}
	key, err := libraryNameKey(set)
	if err != nil {
		return nil, false, err
	}
	spec := env.lookupLibrary(key)
	if spec == nil {
		return nil, false, fmt.Errorf("import: unknown library (%s)", key)
	}
	if spec.preloaded {
		return nil, true, nil
	}
	bindings := make([]importBinding, 0, len(spec.exports))
	for external, internal := range spec.exports {
		bindings = append(bindings, importBinding{local: external, internal: internal})
	}
	return bindings, false, nil
}

func applyImportModifier(mod string, bindings []importBinding, args []Value) ([]importBinding, bool, error) {
	byLocal := make(map[string]int, len(bindings))
	for i, b := range bindings {
		byLocal[b.local] = i
	}
	switch mod {
	case "only":
		out := make([]importBinding, 0, len(args))
		for _, a := range args {
			sym, ok := a.(Symbol)
			if !ok {
				return nil, false, fmt.Errorf("import: only expects identifiers, got %s", PrintValue(a))
			}
			i, ok := byLocal[symBase(string(sym))]
			if !ok {
				return nil, false, fmt.Errorf("import: only names %s, which the set does not export", symBase(string(sym)))
			}
			out = append(out, bindings[i])
		}
		return out, false, nil
	case "except":
		drop := map[string]bool{}
		for _, a := range args {
			sym, ok := a.(Symbol)
			if !ok {
				return nil, false, fmt.Errorf("import: except expects identifiers, got %s", PrintValue(a))
			}
			name := symBase(string(sym))
			if _, ok := byLocal[name]; !ok {
				return nil, false, fmt.Errorf("import: except names %s, which the set does not export", name)
			}
			drop[name] = true
		}
		out := make([]importBinding, 0, len(bindings))
		for _, b := range bindings {
			if !drop[b.local] {
				out = append(out, b)
			}
		}
		return out, false, nil
	case "prefix":
		if len(args) != 1 {
			return nil, false, errors.New("import: prefix expects (prefix <set> <identifier>)")
		}
		sym, ok := args[0].(Symbol)
		if !ok {
			return nil, false, fmt.Errorf("import: prefix expects an identifier, got %s", PrintValue(args[0]))
		}
		p := symBase(string(sym))
		out := make([]importBinding, len(bindings))
		for i, b := range bindings {
			out[i] = importBinding{local: p + b.local, internal: b.internal}
		}
		return out, false, nil
	case "rename":
		out := append([]importBinding(nil), bindings...)
		for _, a := range args {
			pair, ok := formParts(a)
			if !ok || len(pair) != 2 {
				return nil, false, fmt.Errorf("import: rename expects (from to) pairs, got %s", PrintValue(a))
			}
			from, fok := pair[0].(Symbol)
			to, tok := pair[1].(Symbol)
			if !fok || !tok {
				return nil, false, fmt.Errorf("import: rename expects (from to) identifier pairs, got %s", PrintValue(a))
			}
			i, ok := byLocal[symBase(string(from))]
			if !ok {
				return nil, false, fmt.Errorf("import: rename names %s, which the set does not export", symBase(string(from)))
			}
			out[i].local = symBase(string(to))
		}
		return out, false, nil
	}
	return nil, false, fmt.Errorf("import: unknown modifier %s", mod)
}

func applyImportSet(env *Environment, set Value) error {
	bindings, preloaded, err := resolveImportSet(env, set)
	if err != nil {
		return err
	}
	if preloaded {
		return nil
	}
	root := rootOf(env)
	for _, b := range bindings {
		if v, lerr := root.Lookup(b.internal); lerr == nil {
			if b.local != b.internal { // else already global under this name
				root.Set(b.local, v)
			}
			continue
		}
		if m, ok := root.LookupMacro(b.internal); ok {
			if b.local != b.internal {
				root.SetMacro(b.local, m)
			}
			continue
		}
		return fmt.Errorf("import: the library exports %s but defines no such binding", b.internal)
	}
	return nil
}

func matchFeatureRequirement(env *Environment, req Value) (bool, error) {
	switch r := req.(type) {
	case Symbol:
		name := symBase(string(r))
		if name == "else" {
			return true, nil
		}
		for _, f := range featureNames() {
			if f == name {
				return true, nil
			}
		}
		return false, nil
	default:
		parts, ok := formParts(req)
		if !ok || len(parts) == 0 {
			return false, fmt.Errorf("cond-expand: malformed requirement %s", PrintValue(req))
		}
		head, _ := parts[0].(Symbol)
		switch symBase(string(head)) {
		case "library":
			if len(parts) != 2 {
				return false, errors.New("cond-expand: library requirement expects one library name")
			}
			key, err := libraryNameKey(parts[1])
			if err != nil {
				return false, err
			}
			return env.lookupLibrary(key) != nil, nil
		case "and":
			for _, sub := range parts[1:] {
				ok, err := matchFeatureRequirement(env, sub)
				if err != nil || !ok {
					return false, err
				}
			}
			return true, nil
		case "or":
			for _, sub := range parts[1:] {
				ok, err := matchFeatureRequirement(env, sub)
				if err != nil {
					return false, err
				}
				if ok {
					return true, nil
				}
			}
			return false, nil
		case "not":
			if len(parts) != 2 {
				return false, errors.New("cond-expand: not expects one requirement")
			}
			ok, err := matchFeatureRequirement(env, parts[1])
			return !ok, err
		}
		return false, fmt.Errorf("cond-expand: unknown requirement %s", PrintValue(req))
	}
}

func parseSchemeFile(filename string, foldCase bool) ([]Value, error) {
	data, err := os.ReadFile(expandHome(filename))
	if err != nil {
		return nil, err
	}
	tokens := tokenize(string(data))
	if foldCase {
		foldTokens(tokens)
	}
	return parseAllTokens(tokens)
}

func evalSchemeFile(e *Evaluator, env *Environment, filename string, foldCase bool) (Value, error) {
	forms, err := parseSchemeFile(filename, foldCase)
	if err != nil {
		return nil, err
	}
	var last Value
	for _, form := range forms {
		if last, err = e.Eval(form, env); err != nil {
			return nil, err
		}
	}
	return last, nil
}

func processLibraryDecls(e *Evaluator, root *Environment, decls []Value, exports map[string]string) error {
	for _, decl := range decls {
		parts, ok := formParts(decl)
		if !ok || len(parts) == 0 {
			return fmt.Errorf("define-library: malformed declaration %s", PrintValue(decl))
		}
		head, _ := parts[0].(Symbol)
		switch symBase(string(head)) {
		case "export":
			for _, spec := range parts[1:] {
				if sym, ok := spec.(Symbol); ok {
					name := symBase(string(sym))
					exports[name] = name
					continue
				}
				rn, ok := formParts(spec)
				if ok && len(rn) == 3 {
					if kw, _ := rn[0].(Symbol); symBase(string(kw)) == "rename" {
						from, fok := rn[1].(Symbol)
						to, tok := rn[2].(Symbol)
						if fok && tok {
							exports[symBase(string(to))] = symBase(string(from))
							continue
						}
					}
				}
				return fmt.Errorf("define-library: malformed export spec %s", PrintValue(spec))
			}
		case "import":
			for _, set := range parts[1:] {
				if err := applyImportSet(root, set); err != nil {
					return err
				}
			}
		case "begin":
			for _, form := range parts[1:] {
				if _, err := e.Eval(form, root); err != nil {
					return err
				}
			}
		case "include", "include-ci":
			fold := symBase(string(head)) == "include-ci"
			for _, fn := range parts[1:] {
				name, ok := stringText(fn)
				if !ok {
					return fmt.Errorf("define-library: %s expects string filenames", symBase(string(head)))
				}
				if _, err := evalSchemeFile(e, root, name, fold); err != nil {
					return err
				}
			}
		case "include-library-declarations":
			for _, fn := range parts[1:] {
				name, ok := stringText(fn)
				if !ok {
					return errors.New("define-library: include-library-declarations expects string filenames")
				}
				sub, err := parseSchemeFile(name, false)
				if err != nil {
					return err
				}
				if err := processLibraryDecls(e, root, sub, exports); err != nil {
					return err
				}
			}
		case "cond-expand":
			matched := false
			for _, clause := range parts[1:] {
				cl, ok := formParts(clause)
				if !ok || len(cl) == 0 {
					return fmt.Errorf("define-library: malformed cond-expand clause %s", PrintValue(clause))
				}
				hit, err := matchFeatureRequirement(root, cl[0])
				if err != nil {
					return err
				}
				if hit {
					matched = true
					if err := processLibraryDecls(e, root, cl[1:], exports); err != nil {
						return err
					}
					break
				}
			}
			if !matched {
				return errors.New("define-library: cond-expand matched no clause")
			}
		default:
			return fmt.Errorf("define-library: unknown declaration %s", PrintValue(parts[0]))
		}
	}
	return nil
}

func libraryBuiltins(env *Environment) {
	for _, name := range standardLibraries {
		env.setLibrary(name, &librarySpec{preloaded: true})
	}

	Register("define-library", "define an R7RS library: body evaluates into the shared global env, exports recorded", CommandMeta{})
	env.SetSpecialForm("define-library", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		if len(args) < 1 {
			return nil, errors.New("define-library expects (define-library (name ...) declaration...)")
		}
		key, err := libraryNameKey(args[0])
		if err != nil {
			return nil, err
		}
		root := rootOf(env)
		exports := map[string]string{}
		if err := processLibraryDecls(e, root, args[1:], exports); err != nil {
			return nil, err
		}
		env.setLibrary(key, &librarySpec{exports: exports})
		return nil, nil
	})

	Register("import", "import libraries (thin aliasing: (scheme *) are no-ops; only/except/prefix/rename supported)", CommandMeta{})
	env.SetSpecialForm("import", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		if len(args) == 0 {
			return nil, errors.New("import expects at least one import set")
		}
		for _, set := range args {
			if err := applyImportSet(env, set); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})

	Register("cond-expand", "evaluate the first clause whose feature requirement holds: (cond-expand (req body...) ...)", CommandMeta{})
	env.SetSpecialForm("cond-expand", func(args []Value, env *Environment, e *Evaluator) (Value, error) {
		for _, clause := range args {
			cl, ok := formParts(clause)
			if !ok || len(cl) == 0 {
				return nil, fmt.Errorf("cond-expand: malformed clause %s", PrintValue(clause))
			}
			hit, err := matchFeatureRequirement(env, cl[0])
			if err != nil {
				return nil, err
			}
			if !hit {
				continue
			}
			var res Value
			for i, form := range cl[1:] {
				if i == len(cl[1:])-1 {
					return e.tail(form, env), nil
				}
				if res, err = e.Eval(form, env); err != nil {
					return nil, err
				}
			}
			return res, nil
		}
		return nil, errors.New("cond-expand: no matching clause (add an else clause?)")
	})

	includeForm := func(name string, fold bool) SpecialFormFunc {
		return func(args []Value, env *Environment, e *Evaluator) (Value, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("%s expects at least one filename", name)
			}
			var last Value
			for _, a := range args {
				fn, ok := stringText(a)
				if !ok {
					return nil, fmt.Errorf("%s expects string filenames, got %s", name, PrintValue(a))
				}
				v, err := evalSchemeFile(e, env, fn, fold)
				if err != nil {
					return nil, err
				}
				last = v
			}
			return last, nil
		}
	}
	Register("include", "read and evaluate source files in place: (include \"file\"...)", CommandMeta{})
	env.SetSpecialForm("include", includeForm("include", false))
	Register("include-ci", "include with #!fold-case symbol folding", CommandMeta{})
	env.SetSpecialForm("include-ci", includeForm("include-ci", true))
}
