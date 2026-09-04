// Command commentlint runs the comment linter: go run ./tools/commentlint ./...
package main

import (
	"github.com/retrofilter/rf/tools/commentlint/lint"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() { singlechecker.Main(lint.Analyzer) }
