package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
)

func compactTestChat(t *testing.T, fake *fakeModel) (*Chat, *sqlx.DB) {
	t.Helper()
	db, err := models.CreateTempDB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := models.EnsureSession(db, "s1", "/tmp", "", ""); err != nil {
		t.Fatal(err)
	}
	return newChat(core.NewGraphStore(db), Config{Model: "fake"}, fake), db
}

func seedTurns(t *testing.T, db *sqlx.DB, msgs ...*models.Message) []int64 {
	t.Helper()
	var ids []int64
	for _, m := range msgs {
		id, err := models.AppendSessionMessage(db, "s1", m)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func userText(s string) *models.Message {
	return &models.Message{Role: models.MessageRoleUser, Type: models.MessageTypeText, Content: s}
}

func assistantText(s string) *models.Message {
	return &models.Message{Role: models.MessageRoleAssistant, Type: models.MessageTypeText, Content: s}
}

func TestCompactSummarizesOlderTurns(t *testing.T) {
	fake := &fakeModel{responses: []*Response{textResponse("## Goal\nExplore the repo.")}}
	chat, db := compactTestChat(t, fake)

	ids := seedTurns(t, db,
		userText("please explore the repo "+strings.Repeat("in depth ", 50)),
		&models.Message{Role: models.MessageRoleAssistant, Type: models.MessageTypeTool,
			FunctionName: "scheme", Code: "(ls)", Content: "(ls)", Result: "files...", ToolCallID: "t1"},
		assistantText("I explored the repo."),
		userText("now write the summary file"),
		assistantText("Done."),
	)

	res, err := chat.Compact(context.Background(), db, "s1", 30)
	if err != nil {
		t.Fatal(err)
	}
	if res.FirstKeptEntryID != ids[3] {
		t.Errorf("first kept entry = %d, want %d (the last user turn)", res.FirstKeptEntryID, ids[3])
	}
	if res.Summary != "## Goal\nExplore the repo." {
		t.Errorf("summary = %q", res.Summary)
	}
	if res.TokensBefore <= 0 {
		t.Errorf("tokens before = %d, want > 0", res.TokensBefore)
	}

	// The summarization request contains the compacted turns, not the kept ones.
	if len(fake.calls) != 1 {
		t.Fatalf("model called %d times, want 1", len(fake.calls))
	}
	sent := callText(fake.calls[0])
	for _, want := range []string{"please explore the repo", "(ls)", "files...", "I explored the repo."} {
		if !strings.Contains(sent, want) {
			t.Errorf("summarization request missing %q", want)
		}
	}
	if strings.Contains(sent, "now write the summary file") {
		t.Error("summarization request contains a kept turn")
	}

	// The rebuilt transcript is summary + kept turns.
	transcript, err := models.SessionTranscript(db, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 3 {
		t.Fatalf("transcript has %d messages, want 3", len(transcript))
	}
	if !strings.Contains(transcript[0].Content, "## Goal") {
		t.Errorf("transcript does not start with the summary: %q", transcript[0].Content)
	}
	if transcript[1].Content != "now write the summary file" || transcript[2].Content != "Done." {
		t.Errorf("kept turns wrong: %q, %q", transcript[1].Content, transcript[2].Content)
	}
}

func TestCompactKeepsLastTurnWhenEverythingFits(t *testing.T) {
	fake := &fakeModel{responses: []*Response{textResponse("summary")}}
	chat, db := compactTestChat(t, fake)
	ids := seedTurns(t, db,
		userText("first"), assistantText("one"),
		userText("second"), assistantText("two"),
	)

	res, err := chat.Compact(context.Background(), db, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.FirstKeptEntryID != ids[2] {
		t.Errorf("first kept entry = %d, want %d", res.FirstKeptEntryID, ids[2])
	}
}

func TestCompactFoldsInPreviousSummary(t *testing.T) {
	fake := &fakeModel{responses: []*Response{
		textResponse("summary one"),
		textResponse("summary two"),
	}}
	chat, db := compactTestChat(t, fake)
	seedTurns(t, db, userText("first"), assistantText("one"), userText("second"), assistantText("two"))

	if _, err := chat.Compact(context.Background(), db, "s1", 0); err != nil {
		t.Fatal(err)
	}
	seedTurns(t, db, userText("third"), assistantText("three"))
	res, err := chat.Compact(context.Background(), db, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}

	sent := callText(fake.calls[1])
	if !strings.Contains(sent, "summary one") {
		t.Error("second compaction request does not carry the previous summary")
	}
	if !strings.Contains(sent, "second") {
		t.Error("second compaction request missing the live turn to summarize")
	}
	if strings.Contains(sent, "[user] first") {
		t.Error("second compaction re-summarizes already-compacted entries")
	}
	if res.Summary != "summary two" {
		t.Errorf("summary = %q", res.Summary)
	}

	// Rebuild honors only the newest compaction.
	transcript, err := models.SessionTranscript(db, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(transcript[0].Content, "summary two") {
		t.Errorf("transcript starts with %q, want the newest summary", transcript[0].Content)
	}
}

func TestCompactSingleTurnErrors(t *testing.T) {
	fake := &fakeModel{}
	chat, db := compactTestChat(t, fake)
	seedTurns(t, db, userText("only"), assistantText("turn"))

	if _, err := chat.Compact(context.Background(), db, "s1", 0); err == nil {
		t.Fatal("expected an error compacting a single-turn session")
	}
	if len(fake.calls) != 0 {
		t.Error("model was called despite nothing to compact")
	}

	// An empty session errors too.
	if err := models.EnsureSession(db, "empty", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Compact(context.Background(), db, "empty", 0); err == nil {
		t.Fatal("expected an error compacting an empty session")
	}
}
