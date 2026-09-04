package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func chunkRows(t *testing.T, v Value) []string {
	t.Helper()
	rows, ok := v.([]Value)
	if !ok {
		t.Fatalf("chunk should return a list, got %T", v)
	}
	var texts []string
	for i, r := range rows {
		d, ok := r.(Dictionary)
		if !ok {
			t.Fatalf("chunk rows should be dictionaries, got %T", r)
		}
		if idx, ok := d["chunk"].(Integer); !ok || int(idx) != i+1 {
			t.Errorf("row %d: :chunk = %v, want %d", i, d["chunk"], i+1)
		}
		texts = append(texts, string(d["text"].(String)))
	}
	return texts
}

func TestRecursiveChunkLossless(t *testing.T) {
	text := strings.Repeat("First paragraph sentence. ", 4) + "\n\n" +
		strings.Repeat("An oversized paragraph keeps going with more words. ", 8) + "\n\n" +
		"Short tail paragraph."
	for _, size := range []int{64, 120, 300, len(text) + 1} {
		chunks := recursiveChunk(text, size, textLevels)
		if got := strings.Join(chunks, ""); got != text {
			t.Fatalf("size %d: chunks do not reassemble the input", size)
		}
		for _, c := range chunks {
			if len(c) > size {
				t.Errorf("size %d: chunk of %d bytes exceeds target: %q", size, len(c), c)
			}
		}
	}
}

func TestRecursiveChunkMergesSmallParagraphs(t *testing.T) {
	text := "alpha paragraph one lines\n\nbeta paragraph two lines\n\ngamma paragraph three"
	chunks := recursiveChunk(text, len(text), textLevels)
	if len(chunks) != 1 {
		t.Errorf("everything fits one chunk, got %d: %q", len(chunks), chunks)
	}
	chunks = recursiveChunk(text, 55, textLevels)
	if len(chunks) != 2 {
		t.Errorf("expected two merged chunks at size 55, got %d: %q", len(chunks), chunks)
	}
}

func TestRecursiveChunkSmallSizeSplitsOnWords(t *testing.T) {
	text := "alpha paragraph here\n\nbeta paragraph here\n"
	for _, c := range recursiveChunk(text, 25, textLevels) {
		if len(c) > 25 {
			t.Errorf("chunk of %d bytes exceeds 25: %q", len(c), c)
		}
		if strings.Contains(c, "paragrap") && !strings.Contains(c, "paragraph") {
			t.Errorf("chunk cut mid-word: %q", c)
		}
	}
}

func TestRecursiveChunkHardCut(t *testing.T) {
	text := strings.Repeat("x", 95) // no delimiter at any level
	chunks := recursiveChunk(text, 10, textLevels)
	if got := strings.Join(chunks, ""); got != text {
		t.Fatal("hard cut lost bytes")
	}
	for _, c := range chunks {
		if len(c) > 10 {
			t.Errorf("hard-cut chunk of %d bytes exceeds 10", len(c))
		}
	}
}

func TestRecursiveChunkUnicodeBoundaries(t *testing.T) {
	text := strings.Repeat("héllo wörld ", 40)
	for _, size := range []int{7, 25, 100} {
		chunks := recursiveChunk(text, size, textLevels)
		if got := strings.Join(chunks, ""); got != text {
			t.Fatalf("size %d: chunks do not reassemble the input", size)
		}
		for _, c := range chunks {
			if !utf8.ValidString(c) {
				t.Errorf("size %d: chunk splits a rune: %q", size, c)
			}
		}
	}
}

func TestChunkMarkdownHeadings(t *testing.T) {
	text := "# Title\n\nIntro paragraph with enough words to matter here.\n" +
		"\n## Section One\n\nBody of section one, several words long enough.\n" +
		"\n## Section Two\n\nBody of section two, also long enough to count."
	chunks := recursiveChunk(text, 120, markdownLevels)
	if got := strings.Join(chunks, ""); got != text {
		t.Fatal("markdown chunks do not reassemble the input")
	}
	var headStarts int
	for _, c := range chunks[1:] {
		if strings.HasPrefix(c, "\n## ") {
			headStarts++
		}
	}
	if headStarts != 2 {
		t.Errorf("expected both sections to start their own chunk, got %d of 2: %q", headStarts, chunks)
	}
}

func TestChunkHTMLBlocks(t *testing.T) {
	text := "<div><p>" + strings.Repeat("first block words ", 4) + "</p><p>" +
		strings.Repeat("second block words ", 4) + "</p></div>"
	chunks := recursiveChunk(text, 100, htmlLevels)
	if got := strings.Join(chunks, ""); got != text {
		t.Fatal("html chunks do not reassemble the input")
	}
	if !strings.HasSuffix(chunks[0], "</p>") {
		t.Errorf("expected the first chunk to end at a block boundary, got %q", chunks[0])
	}
}

func TestChunkBuiltin(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	original := "para one is here\n\npara two is here\n\npara three is here"

	v, err := evalExpr(`(chunk (lines "para one is here\n\npara two is here\n\npara three is here") {:size 40})`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	texts := chunkRows(t, v)
	if len(texts) < 2 {
		t.Errorf("expected multiple chunks at size 40, got %d: %q", len(texts), texts)
	}
	for _, text := range texts {
		if strings.ContainsAny(text, "\n\r") {
			t.Errorf("cleaned :text should hold no newlines: %q", text)
		}
	}

	// :raw keeps the verbatim slices, which concatenate back exactly.
	v, err = evalExpr(`(chunk (lines "para one is here\n\npara two is here\n\npara three is here") {:size 40 :raw #t})`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	if texts = chunkRows(t, v); strings.Join(texts, "") != original {
		t.Errorf("raw chunks do not reassemble the input: %q", texts)
	}

	v, err = evalExpr(`(pipe (lines "para one is here\n\npara two is here") (chunk {:size 20}))`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	texts = chunkRows(t, v)
	if len(texts) != 2 || texts[0] != "para one is here" || texts[1] != "para two is here" {
		t.Errorf("expected the two cleaned paragraphs from the pipe, got %q", texts)
	}
}

func TestChunkBuiltinCommandModeSpellings(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	fn, err := env.Lookup("chunk")
	if err != nil {
		t.Fatal(err)
	}
	chunk := fn.(BuiltinFunc)

	stream := streamFromList([]Value{String("first line of text here"), String("second line of text here")}, true)
	v, err := chunk([]Value{Dictionary{"size": String("30")}, stream}, env)
	if err != nil {
		t.Fatal(err)
	}
	texts := chunkRows(t, v)
	if len(texts) != 2 || texts[0] != "first line of text here" || texts[1] != "second line of text here" {
		t.Errorf("expected the two cleaned lines, got %q", texts)
	}

	stream = streamFromList([]Value{String("first line of text here"), String("second line of text here")}, true)
	v, err = chunk([]Value{Keyword("raw"), stream}, env)
	if err != nil {
		t.Fatal(err)
	}
	texts = chunkRows(t, v)
	if strings.Join(texts, "") != "first line of text here\nsecond line of text here\n" {
		t.Errorf("raw stream chunks mis-assembled: %q", texts)
	}
	stream = streamFromList([]Value{String("# T"), String(""), String("body words"), String(""), String("## S"), String(""), String("more body words here")}, true)
	if _, err := chunk([]Value{Dictionary{"format": String("markdown")}, stream}, env); err != nil {
		t.Fatal(err)
	}
}

func TestChunkFileInput(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	path := filepath.Join(t.TempDir(), "doc.txt")
	content := "alpha paragraph here\n\nbeta paragraph here\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := evalExpr(`(chunk "`+path+`" {:size 25 :raw #t})`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	texts := chunkRows(t, v)
	if strings.Join(texts, "") != content {
		t.Errorf("raw file chunks do not reassemble the file: %q", texts)
	}
	v, err = evalExpr(`(chunk "`+path+`" {:size 25})`, ev, env)
	if err != nil {
		t.Fatal(err)
	}
	chunkRows(t, v) // cleaned rows still index 1..n
}

func TestChunkErrors(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv
	for expr, want := range map[string]string{
		`(chunk)`:                         "expects one input",
		`(chunk (lines "a") (lines "b"))`: "one input",
		`(chunk (lines "a") {:size "not-a-number"})`: ":size expects",
		`(chunk (lines "a") :size)`:                  "takes a value",
		`(chunk (lines "a") {:format "wat"})`:        "unknown :format",
		`(chunk (lines "a") {:flavor "x"})`:          "unknown option",
		`(chunk "./no-such-file-anywhere")`:          "names a file",
		`(chunk (list 1 2))`:                         "must be strings",
	} {
		_, err := evalExpr(expr, ev, env)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: expected error containing %q, got %v", expr, want, err)
		}
	}

	// Row streams have no text form; the error points at the serializers.
	fn, _ := env.Lookup("chunk")
	rows := streamFromList([]Value{Dictionary{"name": String("x")}}, false)
	if _, err := fn.(BuiltinFunc)([]Value{rows}, env); err == nil || !strings.Contains(err.Error(), "serialize") {
		t.Errorf("row stream should error toward the serializers, got %v", err)
	}
}
