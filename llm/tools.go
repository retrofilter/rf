package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/retrofilter/rf/models"
)

const (
	maxReadLines = 2000
	maxReadBytes = 256 * 1024
)

var readSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Path to the file to read (relative or absolute; ~ expands)."},
		"offset": {"type": "integer", "description": "Line number to start reading from (1-indexed)."},
		"limit": {"type": "integer", "description": "Maximum number of lines to read."}
	},
	"required": ["path"]
}`)

var editSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Path to the file to edit (relative or absolute; ~ expands)."},
		"edits": {
			"type": "array",
			"description": "One or more targeted replacements. Each oldText is matched against the original file, not after earlier edits are applied. Do not include overlapping edits; merge changes to the same region into one edit.",
			"items": {
				"type": "object",
				"properties": {
					"oldText": {"type": "string", "description": "Exact text to replace. Must match a unique region of the file; keep it as small as possible while still unique."},
					"newText": {"type": "string", "description": "Replacement text."}
				},
				"required": ["oldText", "newText"]
			}
		}
	},
	"required": ["path", "edits"]
}`)

var writeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Path to the file to write (relative or absolute; ~ expands)."},
		"content": {"type": "string", "description": "Full content to write."}
	},
	"required": ["path", "content"]
}`)

func fileTools() []Tool {
	return []Tool{
		{
			Name:        "read",
			Description: "Read a file as plain text. Large files are truncated; page through them with offset and limit.",
			InputSchema: readSchema,
		},
		{
			Name:        "edit",
			Description: "Edit a file with exact text replacement. Every edits[].oldText must match a unique region of the original file. When changing several places in one file, use one call with multiple entries in edits[].",
			InputSchema: editSchema,
		},
		{
			Name:        "write",
			Description: "Write content to a file. Creates the file (and parent directories) if needed, overwrites if it exists. For small changes to an existing file, prefer edit.",
			InputSchema: writeSchema,
		},
	}
}

func isFileTool(name string) bool {
	return name == "read" || name == "edit" || name == "write"
}

func expandToolHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func (c *Chat) execFileTool(msg *models.Message) {
	var result string
	var err error
	switch msg.FunctionName {
	case "read":
		result, err = c.execRead(msg.Code)
	case "edit":
		result, err = c.execEdit(msg.Code)
	case "write":
		result, err = c.execWrite(msg.Code)
	}
	if err != nil {
		msg.ErrorMessage = err.Error()
		return
	}
	msg.Result = result
}

func (c *Chat) requireApproval(action string) error {
	if c.Approve != nil && !c.Approve(action) {
		return fmt.Errorf("%s: denied by user", action)
	}
	return nil
}

func (c *Chat) requirePathApproval(action, path string, write bool) error {
	if c.Eval.PathAllowed(path, write) {
		return nil
	}
	if c.Approve == nil {
		if write {
			return fmt.Errorf("%s: no approver available to grant it", action)
		}
		return nil
	}
	return c.requireApproval(action)
}

func (c *Chat) execRead(input string) (string, error) {
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(input), &in); err != nil || in.Path == "" {
		return "", fmt.Errorf("read: invalid input")
	}
	path := expandToolHome(in.Path)
	if err := c.requirePathApproval(fmt.Sprintf("read %q", in.Path), path, false); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("read %s: binary file (%d bytes)", in.Path, len(data))
	}
	return pageLines(string(data), in.Offset, in.Limit), nil
}

func pageLines(content string, offset, limit int) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	total := len(lines)

	start := 0
	if offset > 1 {
		start = min(offset-1, total)
	}
	if limit <= 0 || limit > maxReadLines {
		limit = maxReadLines
	}
	end := min(start+limit, total)

	out := strings.Join(lines[start:end], "\n")
	if len(out) > maxReadBytes {
		out = out[:maxReadBytes]
		if i := strings.LastIndexByte(out, '\n'); i > 0 {
			out = out[:i]
			end = start + strings.Count(out, "\n") + 1
		}
	}
	if start == 0 && end == total {
		return out
	}
	return fmt.Sprintf("%s\n[showing lines %d-%d of %d; use offset/limit to read more]", out, start+1, end, total)
}

func (c *Chat) execEdit(input string) (string, error) {
	var in struct {
		Path  string     `json:"path"`
		Edits []editSpec `json:"edits"`
	}
	if err := json.Unmarshal([]byte(input), &in); err != nil || in.Path == "" {
		return "", fmt.Errorf("edit: invalid input")
	}
	path := expandToolHome(in.Path)
	if err := c.requirePathApproval(fmt.Sprintf("edit %q (%s)", in.Path, countNoun(len(in.Edits), "edit")), path, true); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("could not edit %s: %w", in.Path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	updated, err := applyEdits(string(data), in.Edits)
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", in.Path, err)
	}
	if err := os.WriteFile(path, []byte(updated), info.Mode().Perm()); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s (%s)", in.Path, countNoun(len(in.Edits), "replacement")), nil
}

func (c *Chat) execWrite(input string) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(input), &in); err != nil || in.Path == "" {
		return "", fmt.Errorf("write: invalid input")
	}
	path := expandToolHome(in.Path)
	if err := c.requirePathApproval(fmt.Sprintf("write %q (%d bytes)", in.Path, len(in.Content)), path, true); err != nil {
		return "", err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d bytes)", in.Path, len(in.Content)), nil
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
