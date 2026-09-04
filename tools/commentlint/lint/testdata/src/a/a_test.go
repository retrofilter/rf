// Package doc in a test file. // want "only inside function bodies"
package a

import "testing"

// TestX has a doc. // want "only inside function bodies"
func TestX(t *testing.T) {
	// brief is fine
	x := 1

	// two lines // want "only single-line"
	// here
	_ = x

	// this comment runs on and on and on and on and on and on well past the limit // want "longer than"
}
