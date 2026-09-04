package cmd

import (
	"strings"
	"testing"

	"github.com/retrofilter/rf/models"
)

func toolMsg(name, code string) *models.Message {
	return &models.Message{
		Role:         models.MessageRoleAssistant,
		Type:         models.MessageTypeTool,
		FunctionName: name,
		Code:         code,
	}
}

func TestRenderToolCallScheme(t *testing.T) {
	lines := renderToolCall(toolMsg("scheme", "(+ 1 2)"))
	if len(lines) != 2 || !strings.Contains(lines[0], "Scheme") || !strings.Contains(lines[1], "1") {
		t.Fatalf("scheme render = %q", lines)
	}
}

func TestRenderToolCallRead(t *testing.T) {
	lines := renderToolCall(toolMsg("read", `{"path": "notes.txt", "offset": 10, "limit": 50}`))
	if len(lines) != 1 || !strings.Contains(lines[0], "Read notes.txt") || !strings.Contains(lines[0], "offset 10") {
		t.Fatalf("read render = %q", lines)
	}
}

func TestRenderToolCallEdit(t *testing.T) {
	lines := renderToolCall(toolMsg("edit",
		`{"path": "main.go", "edits": [{"oldText": "a\nb", "newText": "c"}]}`))
	joined := strings.Join(lines, "\n")
	if !strings.Contains(lines[0], "Edit main.go") {
		t.Fatalf("edit header = %q", lines[0])
	}
	if !strings.Contains(joined, "- a") || !strings.Contains(joined, "- b") || !strings.Contains(joined, "+ c") {
		t.Fatalf("edit hunks = %q", joined)
	}
}

func TestRenderToolCallWrite(t *testing.T) {
	lines := renderToolCall(toolMsg("write", `{"path": "new.txt", "content": "hello\nworld\n"}`))
	joined := strings.Join(lines, "\n")
	if !strings.Contains(lines[0], "Write new.txt") || !strings.Contains(joined, "hello") {
		t.Fatalf("write render = %q", lines)
	}
}

func TestRenderToolCallBadInput(t *testing.T) {
	lines := renderToolCall(toolMsg("edit", `{broken`))
	if len(lines) != 1 || !strings.Contains(lines[0], "{broken") {
		t.Fatalf("fallback render = %q", lines)
	}
}
