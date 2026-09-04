package eval

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/bep/gogitignore"
)

const (
	grepPeekSize = 8192    // binary sniff window: a NUL here means skip the file
	grepBufSize  = 1 << 20 // pooled whole-file buffer; larger files stream
)

var grepBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, grepBufSize)
		return &b
	},
}

var grepReaderPool = sync.Pool{
	New: func() any {
		return bufio.NewReaderSize(nil, grepBufSize)
	},
}

var grepDirWorkers = 0

func grepWorkerCount() int {
	if grepDirWorkers > 0 {
		return grepDirWorkers
	}
	return max(2, runtime.NumCPU()/2)
}

type grepIgnore struct {
	root string
	tree *gogitignore.Tree
}

func newGrepIgnore(root string) *grepIgnore {
	ign := &grepIgnore{root: root, tree: gogitignore.New()}
	ign.loadDir(root, "")
	return ign
}

func (ign *grepIgnore) ensureDir(relDir string) {
	if relDir == "." || relDir == "" {
		return
	}
	ign.loadDir(filepath.Join(ign.root, relDir), relDir)
}

func (ign *grepIgnore) loadDir(absDir, relDir string) {
	var lines []string
	for _, name := range []string{".gitignore", ".ignore"} {
		data, err := os.ReadFile(filepath.Join(absDir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			lines = append(lines, strings.TrimSuffix(line, "\r"))
		}
	}
	if len(lines) == 0 {
		return
	}
	treePath := "/"
	if relDir != "" {
		treePath = "/" + filepath.ToSlash(relDir)
	}
	ign.tree.InsertPatterns(treePath, lines...)
}

func (ign *grepIgnore) match(rel string, isDir bool) bool {
	if rel == "" || rel == "." {
		return false
	}
	return ign.tree.Match("/"+filepath.ToSlash(rel), isDir)
}

type grepTask struct {
	path string
	out  chan []Value
}

type grepCtxOpt struct {
	before, after int
}

func (o grepCtxOpt) active() bool { return o.before > 0 || o.after > 0 }

type grepEmit struct {
	num     int
	text    string
	matched bool
}

type grepContext struct {
	opt       grepCtxOpt
	ring      []grepEmit // last `before` unmatched, unemitted lines
	afterLeft int
}

func (c *grepContext) feed(num int, text string, matched bool) []grepEmit {
	if matched {
		out := append(c.ring, grepEmit{num, text, true})
		c.ring = nil
		c.afterLeft = c.opt.after
		return out
	}
	if c.afterLeft > 0 {
		c.afterLeft--
		return []grepEmit{{num, text, false}}
	}
	if c.opt.before > 0 {
		c.ring = append(c.ring, grepEmit{num, text, false})
		if len(c.ring) > c.opt.before {
			c.ring = c.ring[1:]
		}
	}
	return nil
}

func dirGrepStream(m *grepMatcher, pat String, root string, copt grepCtxOpt) *Stream {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	ctx, cancel := context.WithCancel(context.Background())
	tasks := make(chan *grepTask, 256)
	order := make(chan *grepTask, 256)

	var start sync.Once
	launch := func() {
		go func() {
			defer close(tasks)
			defer close(order)
			ign := newGrepIgnore(root)
			_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if ctx.Err() != nil || Interrupted() {
					return fs.SkipAll
				}
				if err != nil {
					return nil // unreadable entries are skipped, like ls
				}
				name := d.Name()
				if d.IsDir() {
					if path != root {
						// .git is always skipped: VCS internals, not content.
						if name == ".git" {
							return fs.SkipDir
						}
						if strings.HasPrefix(name, ".") {
							return fs.SkipDir
						}
						if rel, err := filepath.Rel(root, path); err == nil {
							if ign.match(rel, true) {
								return fs.SkipDir
							}
							ign.ensureDir(rel)
						}
					}
					return nil
				}
				if strings.HasPrefix(name, ".") || !d.Type().IsRegular() {
					return nil
				}
				if rel, err := filepath.Rel(root, path); err == nil && ign.match(rel, false) {
					return nil
				}
				t := &grepTask{path: path, out: make(chan []Value, 1)}
				select {
				case order <- t:
				case <-ctx.Done():
					return fs.SkipAll
				}
				select {
				case tasks <- t:
				case <-ctx.Done():
					return fs.SkipAll
				}
				return nil
			})
		}()
		for i := 0; i < grepWorkerCount(); i++ {
			go func() {
				for t := range tasks {
					if ctx.Err() != nil {
						t.out <- nil // drain without scanning after Close
						continue
					}
					t.out <- scanGrepFile(m, pat, t.path, copt)
				}
			}()
		}
	}

	var cur []Value
	next := func() (Value, bool, error) {
		start.Do(launch)
		for {
			if len(cur) > 0 {
				v := cur[0]
				cur = cur[1:]
				return v, true, nil
			}
			if Interrupted() {
				return nil, false, ErrInterrupted
			}
			t, ok := <-order
			if !ok {
				return nil, false, nil
			}
			cur = <-t.out
		}
	}
	return newStream(next, func() error { cancel(); return nil })
}

func scanGrepFile(m *grepMatcher, pat String, path string, copt grepCtxOpt) []Value {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	display := filepath.ToSlash(path)

	bufp := grepBufPool.Get().(*[]byte)
	defer grepBufPool.Put(bufp)
	buf := *bufp

	n, err := io.ReadFull(f, buf)
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF):
		// File fit in the buffer (possibly empty).
		return scanGrepBody(m, pat, display, buf[:n], copt)
	case err == nil:
		// Buffer filled exactly; probe for extra bytes.
		var probe [1]byte
		if k, _ := f.Read(probe[:]); k == 0 {
			return scanGrepBody(m, pat, display, buf, copt)
		}
		if _, e := f.Seek(0, io.SeekStart); e != nil {
			return nil
		}
		return scanGrepLarge(m, pat, display, f, copt)
	default:
		return nil
	}
}

func scanGrepBody(m *grepMatcher, pat String, path string, data []byte, copt grepCtxOpt) []Value {
	headLimit := min(len(data), grepPeekSize)
	if bytes.IndexByte(data[:headLimit], 0) >= 0 {
		return nil // binary
	}

	var rows []Value
	if copt.active() {
		if len(m.litBytes) > 0 && !bytes.Contains(data, m.litBytes) {
			return nil
		}
		tracker := &grepContext{opt: copt}
		lineNum := 1
		start := 0
		for start < len(data) {
			end := len(data)
			if i := bytes.IndexByte(data[start:], '\n'); i >= 0 {
				end = start + i
			}
			line := grepTrimCR(data[start:end])
			for _, e := range tracker.feed(lineNum, string(line), m.Match(line)) {
				rows = append(rows, grepDirRow(pat, path, e.num, []byte(e.text), e.matched))
			}
			start = end + 1
			lineNum++
		}
		return rows
	}
	if len(m.litBytes) > 0 {
		lineNum := 1
		cursor := 0
		for {
			idx := bytes.Index(data[cursor:], m.litBytes)
			if idx < 0 {
				break
			}
			matchPos := cursor + idx
			lineNum += bytes.Count(data[cursor:matchPos], []byte{'\n'})
			lineStart := 0
			if i := bytes.LastIndexByte(data[:matchPos], '\n'); i >= 0 {
				lineStart = i + 1
			}
			lineEnd := len(data)
			if i := bytes.IndexByte(data[matchPos:], '\n'); i >= 0 {
				lineEnd = matchPos + i
			}
			line := grepTrimCR(data[lineStart:lineEnd])
			if m.re == nil || m.re.Match(line) {
				rows = append(rows, grepDirRow(pat, path, lineNum, line, true))
			}
			// Advance past this line so we don't re-match on it.
			cursor = lineEnd
			if cursor < len(data) {
				cursor++ // skip the '\n'
				lineNum++
			}
		}
		return rows
	}

	lineNum := 1
	start := 0
	for start < len(data) {
		end := len(data)
		if i := bytes.IndexByte(data[start:], '\n'); i >= 0 {
			end = start + i
		}
		line := grepTrimCR(data[start:end])
		if m.Match(line) {
			rows = append(rows, grepDirRow(pat, path, lineNum, line, true))
		}
		start = end + 1
		lineNum++
	}
	return rows
}

func scanGrepLarge(m *grepMatcher, pat String, path string, f *os.File, copt grepCtxOpt) []Value {
	br := grepReaderPool.Get().(*bufio.Reader)
	defer grepReaderPool.Put(br)
	br.Reset(f)

	head, _ := br.Peek(grepPeekSize)
	if bytes.IndexByte(head, 0) >= 0 {
		return nil
	}

	var rows []Value
	tracker := &grepContext{opt: copt}
	lineNum := 0
	for {
		line, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			return nil
		}
		if len(line) > 0 || err == nil {
			lineNum++
			if n := len(line); n > 0 && line[n-1] == '\n' {
				line = line[:n-1]
			}
			line = grepTrimCR(line)
			if copt.active() {
				for _, e := range tracker.feed(lineNum, string(line), m.Match(line)) {
					rows = append(rows, grepDirRow(pat, path, e.num, []byte(e.text), e.matched))
				}
			} else if m.Match(line) {
				rows = append(rows, grepDirRow(pat, path, lineNum, line, true))
			}
		}
		if err != nil {
			return rows
		}
	}
}

func grepDirRow(pat String, path string, lineNum int, line []byte, matched bool) Value {
	row := Dictionary{
		"file": String(path),
		"line": Integer(lineNum),
		"text": String(line),
	}
	if matched {
		row["_match"] = pat
	}
	return row
}

func grepTrimCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}
