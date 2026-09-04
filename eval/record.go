package eval

import (
	"errors"
	"fmt"
	"strings"
)

// RecordType is a record type descriptor, bound to the definition's type
// name. Field order is the declaration order of the field specs.
type RecordType struct {
	Name   string
	Fields []Symbol
}

// Record is an instance: values aligned with the type's field order.
// Fields the constructor doesn't name start nil (unspecified, per spec).
type Record struct {
	rtd  *RecordType
	vals []Value
}

func recordTypeName(rtd *RecordType) string {
	return strings.TrimSuffix(strings.TrimPrefix(rtd.Name, "<"), ">")
}

func printRecord(r *Record) string {
	var sb strings.Builder
	sb.WriteString("#<")
	sb.WriteString(recordTypeName(r.rtd))
	for i, f := range r.rtd.Fields {
		sb.WriteByte(' ')
		sb.WriteString(string(f))
		sb.WriteByte(':')
		sb.WriteByte(' ')
		sb.WriteString(PrintValue(r.vals[i]))
	}
	sb.WriteByte('>')
	return sb.String()
}

func (rtd *RecordType) fieldIndex(name Symbol) (int, bool) {
	for i, f := range rtd.Fields {
		if f == name {
			return i, true
		}
	}
	return 0, false
}

type recordDef struct {
	typeName, ctorName, predName Symbol
	fields                       []Symbol
	ctorIdx                      []int // constructor argument position → field slot
	specs                        []recordFieldSpec
}

type recordFieldSpec struct {
	accessor Symbol
	modifier Symbol // "" when read-only
}

func parseRecordDef(args []Value) (*recordDef, error) {
	if len(args) < 3 {
		return nil, errors.New("define-record-type expects (define-record-type <name> (ctor field...) pred (field accessor [modifier])...)")
	}
	typeName, ok := args[0].(Symbol)
	if !ok {
		return nil, errors.New("define-record-type: type name must be a symbol")
	}
	ctorSpec, ok := args[1].([]Value)
	if !ok || len(ctorSpec) == 0 {
		return nil, errors.New("define-record-type: constructor spec must be (ctor field...)")
	}
	ctorName, ok := ctorSpec[0].(Symbol)
	if !ok {
		return nil, errors.New("define-record-type: constructor name must be a symbol")
	}
	predName, ok := args[2].(Symbol)
	if !ok {
		return nil, errors.New("define-record-type: predicate name must be a symbol")
	}
	def := &recordDef{typeName: typeName, ctorName: ctorName, predName: predName, specs: make([]recordFieldSpec, 0, len(args)-3)}
	for _, f := range args[3:] {
		spec, ok := f.([]Value)
		if !ok || len(spec) < 2 || len(spec) > 3 {
			return nil, errors.New("define-record-type: each field spec must be (field accessor [modifier])")
		}
		fieldName, ok1 := spec[0].(Symbol)
		accName, ok2 := spec[1].(Symbol)
		if !ok1 || !ok2 {
			return nil, errors.New("define-record-type: field and accessor names must be symbols")
		}
		fs := recordFieldSpec{accessor: accName}
		if len(spec) == 3 {
			modName, ok := spec[2].(Symbol)
			if !ok {
				return nil, errors.New("define-record-type: modifier name must be a symbol")
			}
			fs.modifier = modName
		}
		def.fields = append(def.fields, fieldName)
		def.specs = append(def.specs, fs)
	}
	def.ctorIdx = make([]int, len(ctorSpec)-1)
	rtd := &RecordType{Name: string(typeName), Fields: def.fields}
	for i, f := range ctorSpec[1:] {
		fieldName, ok := f.(Symbol)
		if !ok {
			return nil, errors.New("define-record-type: constructor fields must be symbols")
		}
		idx, ok := rtd.fieldIndex(fieldName)
		if !ok {
			return nil, fmt.Errorf("define-record-type: constructor field %s is not a declared field", fieldName)
		}
		def.ctorIdx[i] = idx
	}
	return def, nil
}

func (d *recordDef) names() []Symbol {
	names := make([]Symbol, 0, 3+2*len(d.specs))
	names = append(names, d.typeName, d.ctorName, d.predName)
	for _, fs := range d.specs {
		names = append(names, fs.accessor)
		if fs.modifier != "" {
			names = append(names, fs.modifier)
		}
	}
	return names
}

func (d *recordDef) instantiate() []Value {
	rtd := &RecordType{Name: string(d.typeName), Fields: d.fields}
	ctorIdx, ctorName, predName := d.ctorIdx, d.ctorName, d.predName
	vals := make([]Value, 0, 3+2*len(d.specs))
	vals = append(vals, rtd)
	vals = append(vals, BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != len(ctorIdx) {
			return nil, fmt.Errorf("%s expects %d arguments, got %d", ctorName, len(ctorIdx), len(args))
		}
		r := &Record{rtd: rtd, vals: make([]Value, len(rtd.Fields))}
		for i, idx := range ctorIdx {
			r.vals[idx] = args[i]
		}
		return r, nil
	}))
	vals = append(vals, BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s expects 1 argument", predName)
		}
		r, ok := args[0].(*Record)
		return ok && r.rtd == rtd, nil
	}))
	for i, fs := range d.specs {
		idx := i
		accName := fs.accessor
		vals = append(vals, BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s expects 1 argument", accName)
			}
			r, ok := args[0].(*Record)
			if !ok || r.rtd != rtd {
				return nil, fmt.Errorf("%s expects a %s record, got %s", accName, rtd.Name, PrintValue(args[0]))
			}
			return r.vals[idx], nil
		}))
		if fs.modifier == "" {
			continue
		}
		modName := fs.modifier
		vals = append(vals, BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("%s expects 2 arguments", modName)
			}
			r, ok := args[0].(*Record)
			if !ok || r.rtd != rtd {
				return nil, fmt.Errorf("%s expects a %s record, got %s", modName, rtd.Name, PrintValue(args[0]))
			}
			r.vals[idx] = args[1]
			return nil, nil
		}))
	}
	return vals
}

func recordBuiltins(env *Environment) {
	Register("define-record-type", "define a record type: (define-record-type <name> (ctor field...) pred (field accessor [modifier])...)", CommandMeta{})
	env.setCompiler("define-record-type", "define-record-type", cfDefineRecordType)
}
