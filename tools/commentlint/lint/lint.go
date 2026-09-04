// Package lint is the commentlint analyzer: doc comments only on exported
// declarations and at most three lines, no floating comment blocks, and
// test files limited to brief single-line comments inside function bodies.
package lint

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const (
	maxDocLines    = 3
	maxTestComment = 80
)

// Analyzer is the commentlint analysis.Analyzer.
var Analyzer = &analysis.Analyzer{
	Name:             "commentlint",
	Doc:              "restrict comments to brief docs on exported declarations",
	Run:              run,
	RunDespiteErrors: true,
}

type checker struct {
	pass *analysis.Pass
	file *ast.File
	src  []byte
	seen map[*ast.CommentGroup]bool
}

func run(pass *analysis.Pass) (any, error) {
	for _, f := range pass.Files {
		if ast.IsGenerated(f) {
			continue
		}
		name := pass.Fset.File(f.Pos()).Name()
		src, err := pass.ReadFile(name)
		if err != nil {
			return nil, err
		}
		c := &checker{pass: pass, file: f, src: src, seen: map[*ast.CommentGroup]bool{}}
		if strings.HasSuffix(name, "_test.go") {
			c.checkTestFile()
		} else {
			c.checkFile()
		}
	}
	return nil, nil
}

func (c *checker) checkTestFile() {
	for _, g := range c.file.Comments {
		if isDirectiveGroup(g) {
			continue
		}
		switch {
		case !c.inFuncBody(g):
			c.report(g, "test files allow comments only inside function bodies")
		case c.lines(g) > 1:
			c.report(g, "test files allow only single-line comments")
		case len(g.List[0].Text) > maxTestComment:
			c.report(g, "test file comment longer than %d characters", maxTestComment)
		}
	}
}

func (c *checker) checkFile() {
	c.doc(c.file.Doc, true)
	for _, d := range c.file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			c.doc(d.Doc, funcExported(d))
		case *ast.GenDecl:
			c.genDecl(d)
		}
	}
	ast.Inspect(c.file, func(n ast.Node) bool {
		if f, ok := n.(*ast.Field); ok {
			c.doc(f.Doc, false)
			c.trailing(f.Comment)
		}
		return true
	})
	for _, g := range c.file.Comments {
		if c.seen[g] || isDirectiveGroup(g) {
			continue
		}
		if c.lines(g) > 1 {
			c.report(g, "multi-line comment block outside a doc comment")
		} else if !c.inDecl(g) {
			c.report(g, "floating comment at file level")
		}
	}
}

func (c *checker) genDecl(d *ast.GenDecl) {
	if d.Tok == token.IMPORT {
		c.doc(d.Doc, false)
		for _, s := range d.Specs {
			s := s.(*ast.ImportSpec)
			c.doc(s.Doc, false)
			c.trailing(s.Comment)
		}
		return
	}
	group := false
	for _, s := range d.Specs {
		exported := specExported(s)
		group = group || exported
		switch s := s.(type) {
		case *ast.TypeSpec:
			c.doc(s.Doc, exported)
			c.trailing(s.Comment)
			c.fields(s.Type, exported)
		case *ast.ValueSpec:
			c.doc(s.Doc, exported)
			c.trailing(s.Comment)
		}
	}
	c.doc(d.Doc, group)
}

func (c *checker) fields(t ast.Expr, owner bool) {
	ast.Inspect(t, func(n ast.Node) bool {
		f, ok := n.(*ast.Field)
		if !ok {
			return true
		}
		exported := owner
		for _, n := range f.Names {
			exported = exported && n.IsExported()
		}
		c.doc(f.Doc, exported)
		c.trailing(f.Comment)
		return true
	})
}

func (c *checker) doc(g *ast.CommentGroup, exported bool) {
	if g == nil || c.seen[g] {
		return
	}
	c.seen[g] = true
	if isDirectiveGroup(g) {
		return
	}
	if !exported {
		c.report(g, "doc comment on unexported declaration")
	} else if n := c.lines(g); n > maxDocLines {
		c.pass.Reportf(g.Pos(), "doc comment is %d lines, limit %d", n, maxDocLines)
	}
}

func (c *checker) trailing(g *ast.CommentGroup) {
	if g != nil {
		c.seen[g] = true
	}
}

func (c *checker) inFuncBody(g *ast.CommentGroup) bool {
	for _, d := range c.file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Body != nil && f.Body.Pos() < g.Pos() && g.End() < f.Body.End() {
			return true
		}
	}
	return false
}

func (c *checker) inDecl(g *ast.CommentGroup) bool {
	for _, d := range c.file.Decls {
		if d.Pos() < g.Pos() && g.End() < d.End() {
			return true
		}
	}
	return false
}

func (c *checker) lines(g *ast.CommentGroup) int {
	n := 0
	for _, cm := range g.List {
		if isDirective(cm.Text) || cm.Text == "//" {
			continue
		}
		n += c.pass.Fset.Position(cm.End()).Line - c.pass.Fset.Position(cm.Pos()).Line + 1
	}
	return n
}

func (c *checker) report(g *ast.CommentGroup, format string, args ...any) {
	var edits []analysis.TextEdit
	for _, cm := range g.List {
		if !isDirective(cm.Text) {
			edits = append(edits, c.deletion(cm))
		}
	}
	c.pass.Report(analysis.Diagnostic{
		Pos:     g.Pos(),
		Message: fmt.Sprintf(format, args...),
		SuggestedFixes: []analysis.SuggestedFix{{
			Message:   "delete the comment",
			TextEdits: edits,
		}},
	})
}

func (c *checker) deletion(cm *ast.Comment) analysis.TextEdit {
	tf := c.pass.Fset.File(cm.Pos())
	start, end := tf.Offset(cm.Pos()), tf.Offset(cm.End())
	lineStart := bytes.LastIndexByte(c.src[:start], '\n') + 1
	if len(bytes.TrimSpace(c.src[lineStart:start])) == 0 {
		start = lineStart
		if nl := bytes.IndexByte(c.src[end:], '\n'); nl >= 0 && len(bytes.TrimSpace(c.src[end:end+nl])) == 0 {
			end += nl + 1
		}
	} else {
		start = len(bytes.TrimRight(c.src[:start], " \t"))
	}
	return analysis.TextEdit{Pos: tf.Pos(start), End: tf.Pos(end)}
}

func funcExported(d *ast.FuncDecl) bool {
	if !d.Name.IsExported() {
		return false
	}
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return true
	}
	return ast.IsExported(receiverName(d.Recv.List[0].Type))
}

func receiverName(t ast.Expr) string {
	switch t := t.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

func specExported(s ast.Spec) bool {
	switch s := s.(type) {
	case *ast.TypeSpec:
		return s.Name.IsExported()
	case *ast.ValueSpec:
		for _, n := range s.Names {
			if n.IsExported() {
				return true
			}
		}
	}
	return false
}

func isDirectiveGroup(g *ast.CommentGroup) bool {
	for _, cm := range g.List {
		if !isDirective(cm.Text) {
			return false
		}
	}
	return true
}

func isDirective(text string) bool {
	if !strings.HasPrefix(text, "//") {
		return false
	}
	c := text[2:]
	if strings.HasPrefix(c, "line ") || strings.HasPrefix(c, "extern ") || strings.HasPrefix(c, "export ") {
		return true
	}
	colon := strings.Index(c, ":")
	if colon <= 0 || colon+1 >= len(c) {
		return false
	}
	for i := 0; i <= colon+1; i++ {
		if i == colon {
			continue
		}
		b := c[i]
		if !('a' <= b && b <= 'z' || '0' <= b && b <= '9') {
			return false
		}
	}
	return true
}
