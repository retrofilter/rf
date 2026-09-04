// Package a is fine.
package a

// A floating block above a declaration, detached // want "multi-line comment block"
// by the blank line that follows it.

// Exported has a doc.
func Exported() {}

// unexported has a doc. // want "doc comment on unexported"
func unexported() {
	// a single line inside a body is fine
	x := 1 // trailing is fine
	// two lines inside a body // want "multi-line comment block"
	// are not
	_ = x
}

// floating line at file level // want "floating comment"

// Long doc line one. // want "doc comment is 4 lines, limit 3"
// Line two.
// Line three.
// Line four.
func Long() {}

type T struct {
	// F has a doc.
	F int
	// g has a doc. // want "doc comment on unexported"
	g int
}

type u struct {
	// F on an unexported type. // want "doc comment on unexported"
	F int
}

// Close is a method on an unexported type. // want "doc comment on unexported"
func (u) Close() {}

// Open is a method on an exported type.
func (*T) Open() {}

//go:generate echo directives are fine
var (
	// Group docs on exported names are fine.
	A = 1
	// b is unexported. // want "doc comment on unexported"
	b = 2
)
