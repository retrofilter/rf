package eval

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Port is a Scheme port. Direction and kind are fixed at creation; ops
// check both and whether the port is still open. Like the rest of the
// evaluator, ports assume one evaluation at a time — no locking.
type Port struct {
	kind   string // "string", "bytevector", "file", "stream", "stdin", "stdout", "stderr"
	desc   string // approval/display detail: quoted path for files, "" for in-memory
	binary bool
	input  bool
	output bool
	closed bool

	br     *bufio.Reader // input backing (runes and bytes)
	w      io.Writer     // output backing
	bw     *bufio.Writer // file output buffering; Flush on flush-output-port/close
	closer io.Closer     // underlying file or stream; nil for in-memory

	sb *strings.Builder // output string port accumulator (get-output-string)
	bb *bytes.Buffer    // output bytevector port accumulator

	gate     *approvalGate // non-nil for non-sandboxed ports (files, stdin)
	approved bool          // this port has passed the gate; ops stop prompting

	foldCase bool // #!fold-case directive state (read.go)
}

func (p *Port) printForm() string {
	dir := "port"
	switch {
	case p.input && p.output:
		dir = "input-output-port"
	case p.input:
		dir = "input-port"
	case p.output:
		dir = "output-port"
	}
	if p.desc != "" && p.desc != p.kind {
		return "#<" + dir + " " + p.kind + " " + p.desc + ">"
	}
	return "#<" + dir + " " + p.kind + ">"
}

func (p *Port) approve(op string) error {
	if p.gate == nil || p.approved || p.gate.fn == nil {
		return nil
	}
	if err := p.gate.require(op + " " + p.desc); err != nil {
		return err
	}
	p.approved = true
	return nil
}

func (p *Port) checkRead(op string, binary bool) error {
	if !p.input {
		return fmt.Errorf("%s: not an input port", op)
	}
	if p.closed {
		return fmt.Errorf("%s: port is closed", op)
	}
	if binary && !p.binary {
		return fmt.Errorf("%s expects a binary port", op)
	}
	if !binary && p.binary {
		return fmt.Errorf("%s expects a textual port", op)
	}
	return p.approve(op)
}

func (p *Port) checkWrite(op string, binary bool) error {
	if !p.output {
		return fmt.Errorf("%s: not an output port", op)
	}
	if p.closed {
		return fmt.Errorf("%s: port is closed", op)
	}
	if binary && !p.binary {
		return fmt.Errorf("%s expects a binary port", op)
	}
	if !binary && p.binary {
		return fmt.Errorf("%s expects a textual port", op)
	}
	return p.approve(op)
}

func (p *Port) writeText(op, s string) error {
	if err := p.checkWrite(op, false); err != nil {
		return err
	}
	_, err := io.WriteString(p.w, s)
	return err
}

func (p *Port) closePort() error {
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	if p.bw != nil {
		err = p.bw.Flush()
	}
	if p.closer != nil {
		if cerr := p.closer.Close(); err == nil {
			err = cerr
		}
	}
	return err
}

func (p *Port) readRune() (rune, bool, error) {
	r, _, err := p.br.ReadRune()
	if err == io.EOF {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	return r, false, nil
}

func newStringInputPort(s string) *Port {
	return &Port{kind: "string", input: true, br: bufio.NewReader(strings.NewReader(s))}
}

func newStringOutputPort() *Port {
	sb := &strings.Builder{}
	return &Port{kind: "string", output: true, w: sb, sb: sb}
}

type byteAtATimeReader struct{ f *os.File }

func (r byteAtATimeReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.f.Read(p)
}

var (
	stdinInit sync.Once
	stdinBuf  *bufio.Reader
)

func newStdinPort(gate *approvalGate) *Port {
	stdinInit.Do(func() { stdinBuf = bufio.NewReader(byteAtATimeReader{os.Stdin}) })
	return &Port{kind: "stdin", desc: "stdin", input: true, br: stdinBuf, gate: gate}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func portParameter(name string, init *Port, wantInput bool) *Parameter {
	conv := BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, ok := args[0].(*Port)
		if !ok || (wantInput && !p.input) || (!wantInput && !p.output) {
			dir := "an output"
			if wantInput {
				dir = "an input"
			}
			return nil, fmt.Errorf("%s must be %s port", name, dir)
		}
		return p, nil
	})
	return &Parameter{vals: []Value{init}, converter: conv}
}

func optPort(name string, args []Value, idx int, cur *Parameter) (*Port, error) {
	var v Value
	if len(args) > idx {
		v = args[idx]
	} else {
		v = cur.current()
	}
	p, ok := v.(*Port)
	if !ok {
		return nil, fmt.Errorf("%s expects a port, got %s", name, PrintValue(v))
	}
	return p, nil
}

func portOnly(name string, args []Value) (*Port, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s expects 1 argument: a port", name)
	}
	p, ok := args[0].(*Port)
	if !ok {
		return nil, fmt.Errorf("%s expects a port, got %s", name, PrintValue(args[0]))
	}
	return p, nil
}

func portBuiltins(env *Environment, approval *approvalGate) {
	curIn := portParameter("current-input-port", newStdinPort(approval), true)
	curOut := portParameter("current-output-port", &Port{kind: "stdout", output: true, w: os.Stdout}, false)
	curErr := portParameter("current-error-port", &Port{kind: "stderr", output: true, w: os.Stderr}, false)
	Register("current-input-port", "the current default input port (a parameter object)", CommandMeta{})
	env.Set("current-input-port", curIn)
	Register("current-output-port", "the current default output port (a parameter object)", CommandMeta{})
	env.Set("current-output-port", curOut)
	Register("current-error-port", "the current default error port (a parameter object)", CommandMeta{})
	env.Set("current-error-port", curErr)

	// --- predicates ---
	env.SetBuiltin("port?", "true when the value is a port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("port? expects 1 argument")
		}
		_, ok := args[0].(*Port)
		return ok, nil
	}))
	dirPred := func(name string, pred func(*Port) bool) {
		env.SetBuiltin(name, "port direction/kind predicate", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s expects 1 argument", name)
			}
			p, ok := args[0].(*Port)
			return ok && pred(p), nil
		}))
	}
	dirPred("input-port?", func(p *Port) bool { return p.input })
	dirPred("output-port?", func(p *Port) bool { return p.output })
	dirPred("textual-port?", func(p *Port) bool { return !p.binary })
	dirPred("binary-port?", func(p *Port) bool { return p.binary })

	env.SetBuiltin("input-port-open?", "true when the port is open and capable of input", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := portOnly("input-port-open?", args)
		if err != nil {
			return nil, err
		}
		return p.input && !p.closed, nil
	}))
	env.SetBuiltin("output-port-open?", "true when the port is open and capable of output", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := portOnly("output-port-open?", args)
		if err != nil {
			return nil, err
		}
		return p.output && !p.closed, nil
	}))

	// --- closing ---
	env.SetBuiltin("close-port", "close a port (no effect if already closed)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := portOnly("close-port", args)
		if err != nil {
			return nil, err
		}
		return nil, p.closePort()
	}))
	env.SetBuiltin("close-input-port", "close an input port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := portOnly("close-input-port", args)
		if err != nil {
			return nil, err
		}
		if !p.input {
			return nil, errors.New("close-input-port: not an input port")
		}
		return nil, p.closePort()
	}))
	env.SetBuiltin("close-output-port", "close an output port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := portOnly("close-output-port", args)
		if err != nil {
			return nil, err
		}
		if !p.output {
			return nil, errors.New("close-output-port: not an output port")
		}
		return nil, p.closePort()
	}))

	env.SetBuiltin("call-with-port", "call proc with a port, closing it when proc returns", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 2 {
			return nil, errors.New("call-with-port expects 2 arguments: (call-with-port port proc)")
		}
		p, ok := args[0].(*Port)
		if !ok {
			return nil, errors.New("call-with-port expects a port")
		}
		if !isCallable(args[1]) {
			return nil, errors.New("call-with-port expects a procedure")
		}
		res, err := callFunction(args[1], []Value{p}, env)
		if err != nil {
			return nil, err
		}
		if cerr := p.closePort(); cerr != nil {
			return nil, cerr
		}
		return res, nil
	}))

	// --- string and bytevector ports (sandboxed: never gated) ---
	env.SetBuiltin("open-input-string", "a textual input port reading from a string", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("open-input-string expects 1 argument")
		}
		s, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("open-input-string expects a string")
		}
		return newStringInputPort(s), nil
	}))
	env.SetBuiltin("open-output-string", "a textual output port accumulating a string", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("open-output-string expects no arguments")
		}
		return newStringOutputPort(), nil
	}))
	env.SetBuiltin("get-output-string", "the string accumulated by an output string port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := portOnly("get-output-string", args)
		if err != nil {
			return nil, err
		}
		if p.sb == nil {
			return nil, errors.New("get-output-string: port was not created by open-output-string")
		}
		return String(p.sb.String()), nil
	}))
	env.SetBuiltin("open-input-bytevector", "a binary input port reading from a bytevector", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("open-input-bytevector expects 1 argument")
		}
		bv, ok := args[0].(Bytevector)
		if !ok {
			return nil, errors.New("open-input-bytevector expects a bytevector")
		}
		return &Port{kind: "bytevector", binary: true, input: true, br: bufio.NewReader(bytes.NewReader(bv))}, nil
	}))
	env.SetBuiltin("open-output-bytevector", "a binary output port accumulating a bytevector", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 0 {
			return nil, errors.New("open-output-bytevector expects no arguments")
		}
		bb := &bytes.Buffer{}
		return &Port{kind: "bytevector", binary: true, output: true, w: bb, bb: bb}, nil
	}))
	env.SetBuiltin("get-output-bytevector", "the bytevector accumulated by an output bytevector port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := portOnly("get-output-bytevector", args)
		if err != nil {
			return nil, err
		}
		if p.bb == nil {
			return nil, errors.New("get-output-bytevector: port was not created by open-output-bytevector")
		}
		out := make(Bytevector, p.bb.Len())
		copy(out, p.bb.Bytes())
		return out, nil
	}))

	// --- textual input ---
	env.SetBuiltin("read-char", "read one character from a textual input port, or the eof object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := optPort("read-char", args, 0, curIn)
		if err != nil {
			return nil, err
		}
		if err := p.checkRead("read-char", false); err != nil {
			return nil, err
		}
		r, eof, err := p.readRune()
		if err != nil {
			return nil, err
		}
		if eof {
			return theEOFObject, nil
		}
		return Char(r), nil
	}))
	env.SetBuiltin("peek-char", "the next character without consuming it, or the eof object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := optPort("peek-char", args, 0, curIn)
		if err != nil {
			return nil, err
		}
		if err := p.checkRead("peek-char", false); err != nil {
			return nil, err
		}
		r, eof, err := p.readRune()
		if err != nil {
			return nil, err
		}
		if eof {
			return theEOFObject, nil
		}
		if err := p.br.UnreadRune(); err != nil {
			return nil, err
		}
		return Char(r), nil
	}))
	env.SetBuiltin("read-line", "read a line from a textual input port, or the eof object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := optPort("read-line", args, 0, curIn)
		if err != nil {
			return nil, err
		}
		if err := p.checkRead("read-line", false); err != nil {
			return nil, err
		}
		var sb strings.Builder
		got := false
		for {
			r, eof, err := p.readRune()
			if err != nil {
				return nil, err
			}
			if eof {
				if !got {
					return theEOFObject, nil
				}
				break
			}
			got = true
			if r == '\n' {
				break
			}
			sb.WriteRune(r)
		}
		line := strings.TrimSuffix(sb.String(), "\r")
		return String(line), nil
	}))
	env.SetBuiltin("read-string", "read up to k characters as a string, or the eof object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("read-string expects (read-string k [port])")
		}
		k, ok := numIndex(args[0])
		if !ok || k < 0 {
			return nil, errors.New("read-string: k must be a non-negative integer")
		}
		p, err := optPort("read-string", args, 1, curIn)
		if err != nil {
			return nil, err
		}
		if err := p.checkRead("read-string", false); err != nil {
			return nil, err
		}
		var sb strings.Builder
		for i := 0; i < k; i++ {
			r, eof, err := p.readRune()
			if err != nil {
				return nil, err
			}
			if eof {
				if i == 0 && k > 0 {
					return theEOFObject, nil
				}
				break
			}
			sb.WriteRune(r)
		}
		return String(sb.String()), nil
	}))
	env.SetBuiltin("char-ready?", "true when a character is ready on a textual input port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := optPort("char-ready?", args, 0, curIn)
		if err != nil {
			return nil, err
		}
		if !p.input || p.binary {
			return nil, errors.New("char-ready? expects a textual input port")
		}
		return !p.closed, nil
	}))

	// --- binary input ---
	env.SetBuiltin("read-u8", "read one byte from a binary input port, or the eof object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := optPort("read-u8", args, 0, curIn)
		if err != nil {
			return nil, err
		}
		if err := p.checkRead("read-u8", true); err != nil {
			return nil, err
		}
		b, err := p.br.ReadByte()
		if err == io.EOF {
			return theEOFObject, nil
		}
		if err != nil {
			return nil, err
		}
		return Integer(b), nil
	}))
	env.SetBuiltin("peek-u8", "the next byte without consuming it, or the eof object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := optPort("peek-u8", args, 0, curIn)
		if err != nil {
			return nil, err
		}
		if err := p.checkRead("peek-u8", true); err != nil {
			return nil, err
		}
		bs, err := p.br.Peek(1)
		if err == io.EOF {
			return theEOFObject, nil
		}
		if err != nil {
			return nil, err
		}
		return Integer(bs[0]), nil
	}))
	env.SetBuiltin("u8-ready?", "true when a byte is ready on a binary input port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		p, err := optPort("u8-ready?", args, 0, curIn)
		if err != nil {
			return nil, err
		}
		if !p.input || !p.binary {
			return nil, errors.New("u8-ready? expects a binary input port")
		}
		return !p.closed, nil
	}))
	env.SetBuiltin("read-bytevector", "read up to k bytes as a bytevector, or the eof object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("read-bytevector expects (read-bytevector k [port])")
		}
		k, ok := numIndex(args[0])
		if !ok || k < 0 {
			return nil, errors.New("read-bytevector: k must be a non-negative integer")
		}
		p, err := optPort("read-bytevector", args, 1, curIn)
		if err != nil {
			return nil, err
		}
		if err := p.checkRead("read-bytevector", true); err != nil {
			return nil, err
		}
		buf := make([]byte, k)
		n, err := io.ReadFull(p.br, buf)
		if err == io.EOF && k > 0 {
			return theEOFObject, nil
		}
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return nil, err
		}
		return Bytevector(buf[:n]), nil
	}))
	env.SetBuiltin("read-bytevector!", "read bytes into a bytevector's [start,end) range; count read or the eof object", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 4 {
			return nil, errors.New("read-bytevector! expects (read-bytevector! bv [port [start [end]]])")
		}
		bv, ok := args[0].(Bytevector)
		if !ok {
			return nil, errors.New("read-bytevector! expects a bytevector")
		}
		p, err := optPort("read-bytevector!", args, 1, curIn)
		if err != nil {
			return nil, err
		}
		var rangeArgs []Value
		if len(args) > 2 {
			rangeArgs = args[2:]
		}
		start, end, err := startEnd("read-bytevector!", len(bv), rangeArgs)
		if err != nil {
			return nil, err
		}
		if err := p.checkRead("read-bytevector!", true); err != nil {
			return nil, err
		}
		if start == end {
			return Integer(0), nil
		}
		n, err := io.ReadFull(p.br, bv[start:end])
		if err == io.EOF {
			return theEOFObject, nil
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, err
		}
		return Integer(n), nil
	}))

	// --- textual output ---
	env.SetBuiltin("write-char", "write one character to a textual output port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("write-char expects (write-char char [port])")
		}
		c, ok := args[0].(Char)
		if !ok {
			return nil, errors.New("write-char expects a character")
		}
		p, err := optPort("write-char", args, 1, curOut)
		if err != nil {
			return nil, err
		}
		return nil, p.writeText("write-char", string(rune(c)))
	}))
	env.SetBuiltin("write-string", "write a string (or its [start,end) range) to a textual output port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 4 {
			return nil, errors.New("write-string expects (write-string string [port [start [end]]])")
		}
		s, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("write-string expects a string")
		}
		p, err := optPort("write-string", args, 1, curOut)
		if err != nil {
			return nil, err
		}
		out := s
		if len(args) > 2 {
			runes := []rune(s)
			start, end, err := startEnd("write-string", len(runes), args[2:])
			if err != nil {
				return nil, err
			}
			out = string(runes[start:end])
		}
		return nil, p.writeText("write-string", out)
	}))
	env.SetBuiltin("flush-output-port", "flush buffered output to the port's destination", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) > 1 {
			return nil, errors.New("flush-output-port expects at most 1 argument")
		}
		p, err := optPort("flush-output-port", args, 0, curOut)
		if err != nil {
			return nil, err
		}
		if !p.output {
			return nil, errors.New("flush-output-port: not an output port")
		}
		if p.closed {
			return nil, errors.New("flush-output-port: port is closed")
		}
		if p.bw != nil {
			return nil, p.bw.Flush()
		}
		return nil, nil
	}))

	// --- binary output ---
	env.SetBuiltin("write-u8", "write one byte to a binary output port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("write-u8 expects (write-u8 byte [port])")
		}
		b, ok := numIndex(args[0])
		if !ok || b < 0 || b > 255 {
			return nil, errors.New("write-u8 expects an integer 0-255")
		}
		p, err := optPort("write-u8", args, 1, curOut)
		if err != nil {
			return nil, err
		}
		if err := p.checkWrite("write-u8", true); err != nil {
			return nil, err
		}
		_, err = p.w.Write([]byte{byte(b)})
		return nil, err
	}))
	env.SetBuiltin("write-bytevector", "write a bytevector (or its [start,end) range) to a binary output port", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) < 1 || len(args) > 4 {
			return nil, errors.New("write-bytevector expects (write-bytevector bv [port [start [end]]])")
		}
		bv, ok := args[0].(Bytevector)
		if !ok {
			return nil, errors.New("write-bytevector expects a bytevector")
		}
		p, err := optPort("write-bytevector", args, 1, curOut)
		if err != nil {
			return nil, err
		}
		var rangeArgs []Value
		if len(args) > 2 {
			rangeArgs = args[2:]
		}
		start, end, err := startEnd("write-bytevector", len(bv), rangeArgs)
		if err != nil {
			return nil, err
		}
		if err := p.checkWrite("write-bytevector", true); err != nil {
			return nil, err
		}
		_, err = p.w.Write(bv[start:end])
		return nil, err
	}))

	// --- (scheme file): every open and delete is approval-gated ---
	openFile := func(name string, binary, input bool) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s expects 1 argument: a path", name)
			}
			raw, ok := stringText(args[0])
			if !ok {
				return nil, fmt.Errorf("%s expects a string path", name)
			}
			path := expandHome(raw)
			action := fmt.Sprintf("%s %q", name, raw)
			if input {
				if err := approval.requireRead(action, path); err != nil {
					return nil, err
				}
			} else if err := approval.requireWrite(action, path); err != nil {
				return nil, err
			}
			approved := approval.fn != nil
			desc := fmt.Sprintf("%q", raw)
			if input {
				f, err := os.Open(path)
				if err != nil {
					return nil, err
				}
				return &Port{kind: "file", desc: desc, binary: binary, input: true,
					br: bufio.NewReader(f), closer: f, gate: approval, approved: approved}, nil
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
			if err != nil {
				return nil, err
			}
			bw := bufio.NewWriter(f)
			return &Port{kind: "file", desc: desc, binary: binary, output: true,
				w: bw, bw: bw, closer: f, gate: approval, approved: approved}, nil
		}
	}
	env.SetBuiltin("open-input-file", "a textual input port over a file (approval-gated)", openFile("open-input-file", false, true))
	env.SetBuiltin("open-binary-input-file", "a binary input port over a file (approval-gated)", openFile("open-binary-input-file", true, true))
	env.SetBuiltin("open-output-file", "a textual output port over a file, truncating (approval-gated)", openFile("open-output-file", false, false))
	env.SetBuiltin("open-binary-output-file", "a binary output port over a file, truncating (approval-gated)", openFile("open-binary-output-file", true, false))

	callWithFile := func(name string, open BuiltinFunc) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("%s expects 2 arguments: (%s path proc)", name, name)
			}
			if !isCallable(args[1]) {
				return nil, fmt.Errorf("%s expects a procedure", name)
			}
			pv, err := open([]Value{args[0]}, env)
			if err != nil {
				return nil, err
			}
			p := pv.(*Port)
			res, err := callFunction(args[1], []Value{p}, env)
			cerr := p.closePort()
			if err != nil {
				return nil, err
			}
			if cerr != nil {
				return nil, cerr
			}
			return res, nil
		}
	}
	env.SetBuiltin("call-with-input-file", "open a file for input and call proc with the port", callWithFile("call-with-input-file", openFile("open-input-file", false, true)))
	env.SetBuiltin("call-with-output-file", "open a file for output and call proc with the port", callWithFile("call-with-output-file", openFile("open-output-file", false, false)))

	withFile := func(name string, open BuiltinFunc, cur *Parameter) BuiltinFunc {
		return func(args []Value, env *Environment) (Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("%s expects 2 arguments: (%s path thunk)", name, name)
			}
			if !isCallable(args[1]) {
				return nil, fmt.Errorf("%s expects a thunk", name)
			}
			pv, err := open([]Value{args[0]}, env)
			if err != nil {
				return nil, err
			}
			p := pv.(*Port)
			cur.vals = append(cur.vals, p)
			res, err := callFunction(args[1], nil, env)
			cur.vals = cur.vals[:len(cur.vals)-1]
			cerr := p.closePort()
			if err != nil {
				return nil, err
			}
			if cerr != nil {
				return nil, cerr
			}
			return res, nil
		}
	}
	env.SetBuiltin("with-input-from-file", "run a thunk with current-input-port reading a file", withFile("with-input-from-file", openFile("open-input-file", false, true), curIn))
	env.SetBuiltin("with-output-to-file", "run a thunk with current-output-port writing a file", withFile("with-output-to-file", openFile("open-output-file", false, false), curOut))

	env.SetBuiltin("file-exists?", "true when the named file exists", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("file-exists? expects 1 argument: a path")
		}
		s, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("file-exists? expects a string path")
		}
		_, err := os.Stat(expandHome(s))
		return err == nil, nil
	}))
	env.SetBuiltin("delete-file", "delete the named file (approval-gated)", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("delete-file expects 1 argument: a path")
		}
		s, ok := stringText(args[0])
		if !ok {
			return nil, errors.New("delete-file expects a string path")
		}
		path := expandHome(s)
		if err := approval.requireWrite(fmt.Sprintf("delete-file %q", s), path); err != nil {
			return nil, err
		}
		return nil, os.Remove(path)
	}))

	// --- streams ↔ ports bridge (extension) ---
	env.SetBuiltin("stream->port", "a textual input port over a line stream's text", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		if len(args) != 1 {
			return nil, errors.New("stream->port expects 1 argument: a line stream")
		}
		s, ok := args[0].(*Stream)
		if !ok || !s.Lines() {
			return nil, errors.New("stream->port expects a line stream (rows serialize first: json or text)")
		}
		return &Port{kind: "stream", input: true,
			br: bufio.NewReader(s.TextReader()), closer: closerFunc(s.Close)}, nil
	}))

	readBuiltins(env, curIn)
	writeBuiltins(env, curOut)
}
