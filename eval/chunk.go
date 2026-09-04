package eval

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

const defaultChunkSize = 256

const minCharsPerChunk = 24

type chunkLevel struct {
	delims      []string
	includeNext bool
}

var textLevels = []chunkLevel{
	{delims: []string{"\n\n", "\r\n", "\n", "\r"}},
	{delims: []string{". ", "! ", "? "}},
	{delims: []string{";", ":", ",", "—", "|", ")", "]", "}", ">", "\""}},
	{delims: []string{" ", "\t"}},
}

var markdownLevels = append([]chunkLevel{
	{delims: []string{"\n# ", "\n## ", "\n### ", "\n#### ", "\n##### ", "\n###### "}, includeNext: true},
}, textLevels...)

var htmlLevels = append([]chunkLevel{
	{delims: []string{
		"</p>", "</div>", "</section>", "</article>", "</blockquote>",
		"</pre>", "</table>", "</ul>", "</ol>", "</li>", "</tr>",
		"</h1>", "</h2>", "</h3>", "</h4>", "</h5>", "</h6>",
		"<br>", "<br/>", "<br />",
	}},
}, textLevels...)

func chunkLevelsFor(format string) ([]chunkLevel, error) {
	switch format {
	case "", "text", "plain":
		return textLevels, nil
	case "markdown", "md":
		return markdownLevels, nil
	case "html":
		return htmlLevels, nil
	}
	return nil, fmt.Errorf("chunk: unknown :format %q (supported: text markdown html)", format)
}

func splitAtDelims(text string, lvl chunkLevel) []string {
	var out []string
	start, i := 0, 0
	for i < len(text) {
		var matched string
		for _, d := range lvl.delims {
			if strings.HasPrefix(text[i:], d) {
				matched = d
				break
			}
		}
		if matched == "" {
			i++
			continue
		}
		if lvl.includeNext {
			if i > start {
				out = append(out, text[start:i])
			}
			start = i
		} else {
			out = append(out, text[start:i+len(matched)])
			start = i + len(matched)
		}
		i += len(matched)
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

func mergeShort(splits []string, min int) []string {
	var out []string
	var cur strings.Builder
	for _, s := range splits {
		cur.WriteString(s)
		if cur.Len() >= min {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		if len(out) > 0 {
			out[len(out)-1] += cur.String()
		} else {
			out = append(out, cur.String())
		}
	}
	return out
}

func hardCut(text string, size int) []string {
	var out []string
	for len(text) > size {
		cut := size
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if cut == 0 {
			_, cut = utf8.DecodeRuneInString(text)
		}
		out = append(out, text[:cut])
		text = text[cut:]
	}
	if len(text) > 0 {
		out = append(out, text)
	}
	return out
}

func recursiveChunk(text string, size int, levels []chunkLevel) []string {
	if len(text) <= size {
		return []string{text}
	}
	if len(levels) == 0 {
		return hardCut(text, size)
	}
	splits := mergeShort(splitAtDelims(text, levels[0]), min(minCharsPerChunk, max(1, size/4)))
	if len(splits) <= 1 {
		return recursiveChunk(text, size, levels[1:])
	}
	var out []string
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
		}
	}
	for _, s := range splits {
		if len(s) > size {
			flush()
			out = append(out, recursiveChunk(s, size, levels[1:])...)
			continue
		}
		if buf.Len() > 0 && buf.Len()+len(s) > size {
			flush()
		}
		buf.WriteString(s)
	}
	flush()
	return out
}

func chunkInputText(v Value, approval *approvalGate) (string, error) {
	switch in := normalizeList(v).(type) {
	case String:
		path := expandHome(string(in))
		if err := approval.requireRead(fmt.Sprintf("chunk %q", path), path); err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("chunk: %v (the input string names a file — use (lines \"text\") to chunk literal text)", err)
		}
		return string(data), nil
	case *Stream:
		if !in.Lines() {
			return "", errors.New("chunk expects text, not rows — serialize first with (text rows) or (json rows)")
		}
		s, _, err := AsString(in)
		return s, err
	case []Value:
		parts := make([]string, len(in))
		for i, item := range in {
			s, ok := item.(String)
			if !ok {
				return "", fmt.Errorf("chunk: list input must be strings (lines of text), got %s", PrintValue(item))
			}
			parts[i] = string(s)
		}
		return strings.Join(parts, "\n"), nil
	}
	return "", fmt.Errorf("chunk: input must be a file path, a stream, or a list of strings, got %s", PrintValue(v))
}

func chunkFormatArg(v Value) (string, bool) {
	switch s := v.(type) {
	case String:
		return string(s), true
	case Keyword:
		return string(s), true
	case Symbol:
		return string(s), true
	}
	return "", false
}

func cleanChunkText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func chunkBuiltins(env *Environment, approval *approvalGate) {
	Register("chunk", "split text into chunks for embedding",
		CommandMeta{Command: true, MinArgs: 1, MaxArgs: 1, Stage: true, Usage: "[input]",
			Options: []Option{
				{Long: "size", Short: "s", Kind: OptionInt, Placeholder: "N", Doc: "target chunk size in characters (default 900)"},
				{Long: "format", Kind: OptionString, Placeholder: "FMT", Doc: "boundary rules: text, markdown, or html"},
				{Long: "raw", Kind: OptionBool, Doc: "keep chunk text verbatim for exact reassembly"},
			}})
	env.Set("chunk", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("chunk", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, errors.New("chunk expects one input: (chunk input [{:size n :format \"markdown\" :raw #t}]) — a file path, or pipe text in")
		}
		input := pos[0]
		size := OptInt(opts, "size", defaultChunkSize)
		if size < 1 {
			return nil, errors.New("chunk: :size expects a positive number of characters")
		}
		format := ""
		if v, ok := opts["format"]; ok {
			s, ok := chunkFormatArg(v)
			if !ok {
				return nil, errors.New("chunk: :format expects a name (text, markdown, html)")
			}
			format = s
		}
		raw := OptBool(opts, "raw")
		levels, err := chunkLevelsFor(format)
		if err != nil {
			return nil, err
		}
		text, err := chunkInputText(input, approval)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" {
			return []Value{}, nil
		}
		pieces := recursiveChunk(text, size, levels)
		rows := make([]Value, 0, len(pieces))
		for _, p := range pieces {
			display := p
			if !raw {
				display = cleanChunkText(p)
				if display == "" {
					continue
				}
			}
			rows = append(rows, Dictionary{
				"chunk": Integer(len(rows) + 1),
				"text":  String(display),
			})
		}
		return rows, nil
	}))
}
