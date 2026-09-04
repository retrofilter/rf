package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/models"
	_ "modernc.org/sqlite"
)

func fileToolResponse(id, name string, input any) *Response {
	raw, _ := json.Marshal(input)
	return &Response{
		StopReason: "tool_use",
		Content: []ContentBlock{{
			Type:  "tool_use",
			ID:    id,
			Name:  name,
			Input: raw,
		}},
	}
}

func newToolTestChat(t *testing.T, model Model) *Chat {
	t.Helper()
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return newChat(core.NewGraphStore(db), Config{Provider: models.ProviderOllama, Model: "fake"}, model)
}

func TestFileToolsRegistered(t *testing.T) {
	chat := newToolTestChat(t, &fakeModel{})
	var names []string
	for _, tool := range chat.Tools() {
		names = append(names, tool.Name)
	}
	want := []string{"scheme", "read", "edit", "write"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestReadToolLoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeModel{responses: []*Response{
		fileToolResponse("toolu_1", "read", map[string]any{"path": path}),
		textResponse("done"),
	}}
	chat := newToolTestChat(t, fake)

	replies, err := chat.Message(context.Background(), "read it", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	var toolMsg *models.Message
	for _, m := range replies {
		if m.Type == models.MessageTypeTool {
			toolMsg = m
		}
	}
	if toolMsg == nil || toolMsg.FunctionName != "read" {
		t.Fatalf("expected a read tool message, got %+v", replies)
	}
	if toolMsg.Result != "line one\nline two" {
		t.Fatalf("read result = %q", toolMsg.Result)
	}

	// The result went back to the model as a structured tool_result block.
	second := fake.calls[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Role != "user" || last.Content[0].Type != "tool_result" || !strings.Contains(last.Content[0].Content, "line one") {
		t.Fatalf("tool_result not fed back: %+v", last)
	}
}

func TestEditToolLoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeModel{responses: []*Response{
		fileToolResponse("toolu_1", "edit", map[string]any{
			"path": path,
			"edits": []map[string]string{
				{"oldText": "func main() {}", "newText": "func main() {\n\tprintln(\"hi\")\n}"},
			},
		}),
		textResponse("done"),
	}}
	chat := newToolTestChat(t, fake)
	chat.Approve = func(string) bool { return true } // writes fail closed without an approver

	if _, err := chat.Message(context.Background(), "edit it", nil, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "println(\"hi\")") {
		t.Fatalf("file not edited: %q", string(data))
	}
}

func TestWriteToolLoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "new.txt")

	fake := &fakeModel{responses: []*Response{
		fileToolResponse("toolu_1", "write", map[string]any{"path": path, "content": "hello\n"}),
		textResponse("done"),
	}}
	chat := newToolTestChat(t, fake)
	chat.Approve = func(string) bool { return true } // writes fail closed without an approver

	if _, err := chat.Message(context.Background(), "write it", nil, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("file content = %q", string(data))
	}
}

func TestEditWriteApprovalGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeModel{responses: []*Response{
		fileToolResponse("toolu_1", "write", map[string]any{"path": path, "content": "clobbered"}),
		textResponse("done"),
	}}
	chat := newToolTestChat(t, fake)
	var asked []string
	chat.Approve = func(action string) bool {
		asked = append(asked, action)
		return false
	}

	replies, err := chat.Message(context.Background(), "overwrite it", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "write") {
		t.Fatalf("approval not requested: %v", asked)
	}
	var toolMsg *models.Message
	for _, m := range replies {
		if m.Type == models.MessageTypeTool {
			toolMsg = m
		}
	}
	if toolMsg == nil || !strings.Contains(toolMsg.ErrorMessage, "denied by user") {
		t.Fatalf("expected denial error, got %+v", toolMsg)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original\n" {
		t.Fatalf("file was modified despite denial: %q", string(data))
	}
}

func TestReadToolApproval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeModel{responses: []*Response{
		fileToolResponse("toolu_1", "read", map[string]any{"path": path}),
		textResponse("done"),
	}}
	chat := newToolTestChat(t, fake)
	var asked []string
	chat.Approve = func(action string) bool {
		asked = append(asked, action)
		return true
	}
	if _, err := chat.Message(context.Background(), "read it", nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "read") {
		t.Errorf("read should prompt once, approver saw %v", asked)
	}

	// With a read grant over the directory, the prompt is skipped.
	fake2 := &fakeModel{responses: []*Response{
		fileToolResponse("toolu_2", "read", map[string]any{"path": path}),
		textResponse("done"),
	}}
	chat2 := newToolTestChat(t, fake2)
	chat2.Eval.AllowDir(dir, false)
	chat2.Approve = func(action string) bool {
		t.Errorf("granted read requested approval: %s", action)
		return false
	}
	if _, err := chat2.Message(context.Background(), "read it", nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestWorkingDirGrant(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	chat := newToolTestChat(t, &fakeModel{responses: []*Response{textResponse("hi")}})
	chat.Env.Set("agent-allow-working-dir", eval.Keyword("write"))
	if chat.Eval.PathAllowed(filepath.Join(dir, "x"), true) {
		t.Fatal("grant must not exist before the first message")
	}
	if _, err := chat.Message(context.Background(), "hello", nil, ""); err != nil {
		t.Fatal(err)
	}
	if !chat.Eval.PathAllowed(filepath.Join(dir, "x"), true) {
		t.Error("agent-allow-working-dir :write should grant writes under the captured cwd")
	}
	if chat.Eval.PathAllowed(filepath.Join(orig, "x"), false) {
		t.Error("the grant must not cover paths outside the captured cwd")
	}

	// Unset binding: no grant.
	chat2 := newToolTestChat(t, &fakeModel{responses: []*Response{textResponse("hi")}})
	if _, err := chat2.Message(context.Background(), "hello", nil, ""); err != nil {
		t.Fatal(err)
	}
	if chat2.Eval.PathAllowed(filepath.Join(dir, "x"), false) {
		t.Error("no agent-allow-working-dir binding must mean no grant")
	}
}

func TestPageLines(t *testing.T) {
	content := "a\nb\nc\nd\ne\n"
	if got := pageLines(content, 0, 0); got != "a\nb\nc\nd\ne" {
		t.Fatalf("full read = %q", got)
	}
	got := pageLines(content, 2, 2)
	if !strings.HasPrefix(got, "b\nc\n") || !strings.Contains(got, "lines 2-3 of 5") {
		t.Fatalf("windowed read = %q", got)
	}
	// Past-the-end offset yields an empty window, not a panic.
	if got := pageLines(content, 99, 0); !strings.Contains(got, "lines 100-5 of 5") && !strings.Contains(got, "of 5") {
		t.Fatalf("past-end read = %q", got)
	}
}

func TestReadToolBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin")
	if err := os.WriteFile(path, []byte{0x7f, 0x45, 0x4c, 0x46, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	chat := newToolTestChat(t, &fakeModel{})
	_, err := chat.execRead(`{"path": "` + path + `"}`)
	if err == nil || !strings.Contains(err.Error(), "binary file") {
		t.Fatalf("expected binary-file error, got %v", err)
	}
}

func TestFileToolsFailClosedWithoutApprover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	chat := newToolTestChat(t, &fakeModel{})
	if err := chat.requirePathApproval("write test", path, true); err == nil {
		t.Error("write without an approver should fail closed")
	}
	if err := chat.requirePathApproval("read test", path, false); err != nil {
		t.Errorf("read without an approver should pass: %v", err)
	}
	chat.Eval.AllowDir(dir, true)
	if err := chat.requirePathApproval("write test", path, true); err != nil {
		t.Errorf("granted write should pass: %v", err)
	}

	target := filepath.Join(dir, "new.txt")
	fake := &fakeModel{responses: []*Response{
		fileToolResponse("toolu_1", "write", map[string]any{"path": target, "content": "x"}),
		textResponse("done"),
	}}
	chat2 := newToolTestChat(t, fake)
	replies, err := chat2.Message(context.Background(), "write it", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var toolMsg *models.Message
	for _, m := range replies {
		if m.Type == models.MessageTypeTool {
			toolMsg = m
		}
	}
	if toolMsg == nil || !strings.Contains(toolMsg.ErrorMessage, "no approver") {
		t.Fatalf("expected fail-closed error, got %+v", toolMsg)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("file was written despite the missing approver")
	}
}
