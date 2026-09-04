package lint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
)

const listFiles = `{{$d := .Dir}}{{range .GoFiles}}{{$d}}/{{.}}
{{end}}{{range .TestGoFiles}}{{$d}}/{{.}}
{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}
{{end}}`

func TestRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("loads the whole module")
	}
	root, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	list := exec.Command("go", "list", "-f", listFiles, "./...")
	list.Dir = strings.TrimSpace(string(root))
	out, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	// The analyzer is purely syntactic, so parse without type-checking.
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range strings.Fields(string(out)) {
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	pass := &analysis.Pass{
		Analyzer: Analyzer,
		Fset:     fset,
		Files:    files,
		ReadFile: os.ReadFile,
		Report:   func(d analysis.Diagnostic) { t.Errorf("%s: %s", fset.Position(d.Pos), d.Message) },
	}
	if _, err := run(pass); err != nil {
		t.Fatal(err)
	}
}
