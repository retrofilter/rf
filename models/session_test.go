package models

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionTitleRuneSafe(t *testing.T) {
	long := strings.Repeat("é", 60) // 120 bytes; byte 80 falls mid-rune
	got := sessionTitle(long)
	if !utf8.ValidString(got) {
		t.Errorf("sessionTitle produced invalid UTF-8: %q", got)
	}
	if len(got) > 80 {
		t.Errorf("sessionTitle length = %d, want <= 80", len(got))
	}
}

func TestSessionAppendAndTranscript(t *testing.T) {
	db, err := CreateTempDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := EnsureSession(db, "s1", "/home/u/proj", "proj", ""); err != nil {
		t.Fatal(err)
	}
	// Idempotent: a second ensure must not error or reset anything.
	if err := EnsureSession(db, "s1", "/elsewhere", "", ""); err != nil {
		t.Fatal(err)
	}

	msgs := []*Message{
		{Role: MessageRoleUser, Type: MessageTypeText, Content: "list the files"},
		{Role: MessageRoleAssistant, Type: MessageTypeTool, FunctionName: "scheme",
			Code: "(ls)", Content: "(ls)", Result: `(("name" . "a.txt"))`, ToolCallID: "t1"},
		{Role: MessageRoleAssistant, Type: MessageTypeText, Content: "One file: a.txt"},
	}
	for _, m := range msgs {
		if _, err := AppendSessionMessage(db, "s1", m); err != nil {
			t.Fatal(err)
		}
	}

	got, err := SessionTranscript(db, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(msgs) {
		t.Fatalf("transcript has %d messages, want %d", len(got), len(msgs))
	}
	for i, m := range msgs {
		if *got[i] != *m {
			t.Errorf("message %d = %+v, want %+v", i, *got[i], *m)
		}
	}

	// Parent chain: root entry has NULL parent, each next points to previous.
	entries, err := SessionEntries(db, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].ParentID.Valid {
		t.Errorf("root entry has parent %d, want NULL", entries[0].ParentID.Int64)
	}
	for i := 1; i < len(entries); i++ {
		if !entries[i].ParentID.Valid || entries[i].ParentID.Int64 != entries[i-1].ID {
			t.Errorf("entry %d parent = %+v, want %d", i, entries[i].ParentID, entries[i-1].ID)
		}
	}
}

func TestSessionCompactionRebuild(t *testing.T) {
	db, err := CreateTempDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := EnsureSession(db, "s1", "", "", ""); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, m := range []*Message{
		{Role: MessageRoleUser, Type: MessageTypeText, Content: "old question"},
		{Role: MessageRoleAssistant, Type: MessageTypeText, Content: "old answer"},
		{Role: MessageRoleUser, Type: MessageTypeText, Content: "recent question"},
		{Role: MessageRoleAssistant, Type: MessageTypeText, Content: "recent answer"},
	} {
		id, err := AppendSessionMessage(db, "s1", m)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	// Compact everything before "recent question".
	if _, err := AppendSessionCompaction(db, "s1", Compaction{
		Summary:          "User asked an old question; it was answered.",
		FirstKeptEntryID: ids[2],
		TokensBefore:     1234,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := SessionTranscript(db, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("transcript has %d messages, want 3 (summary + 2 kept)", len(got))
	}
	if got[0].Role != MessageRoleUser || !strings.Contains(got[0].Content, "<summary>") ||
		!strings.Contains(got[0].Content, "old question; it was answered") {
		t.Errorf("first message is not the injected summary: %+v", got[0])
	}
	if got[1].Content != "recent question" || got[2].Content != "recent answer" {
		t.Errorf("kept messages wrong: %q, %q", got[1].Content, got[2].Content)
	}

	// The log itself keeps everything: compaction never deletes.
	entries, err := SessionEntries(db, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Errorf("log has %d entries, want 5", len(entries))
	}
}

func TestSessionTitleAndListing(t *testing.T) {
	db, err := CreateTempDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, s := range []string{"a", "b"} {
		if err := EnsureSession(db, s, "/dir/"+s, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	long := strings.Repeat("word ", 40)
	if _, err := AppendSessionMessage(db, "a",
		&Message{Role: MessageRoleUser, Type: MessageTypeText, Content: "first\nline"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendSessionMessage(db, "a",
		&Message{Role: MessageRoleUser, Type: MessageTypeText, Content: "second"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendSessionMessage(db, "b",
		&Message{Role: MessageRoleUser, Type: MessageTypeText, Content: long}); err != nil {
		t.Fatal(err)
	}

	sessions, err := ListSessions(db, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("listed %d sessions, want 2", len(sessions))
	}
	byID := map[string]Session{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	// Title comes from the first user line only, newlines flattened.
	if got := byID["a"].Title; got != "first line" {
		t.Errorf("session a title = %q, want %q", got, "first line")
	}
	if got := byID["b"].Title; len(got) > 80 {
		t.Errorf("session b title not truncated: %d chars", len(got))
	}
	if byID["a"].Cwd != "/dir/a" {
		t.Errorf("session a cwd = %q", byID["a"].Cwd)
	}
}

func TestAppendSessionMessageRejectsUnknownRole(t *testing.T) {
	db, err := CreateTempDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := EnsureSession(db, "s1", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendSessionMessage(db, "s1",
		&Message{Role: MessageRoleSystem, Type: MessageTypeText, Content: "x"}); err == nil {
		t.Fatal("expected error for system-role message")
	}
}
