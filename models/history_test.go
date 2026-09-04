package models

import "testing"

func TestRecentHistorySeeds(t *testing.T) {
	db, err := CreateTempDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, e := range []HistoryEntry{
		{Line: "oldest", Mode: "command", Cwd: "/a", Session: "s"},
		{Line: "ls -la", Mode: "command", Cwd: "/a", Session: "s"},
		{Line: "why is the build red", Mode: "agent", Cwd: "/b", Session: "s"},
	} {
		if err := AddHistory(db, &e); err != nil {
			t.Fatal(err)
		}
	}

	seeds, err := RecentHistorySeeds(db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 2 {
		t.Fatalf("expected 2 seeds, got %d", len(seeds))
	}
	if seeds[0].Line != "ls -la" || seeds[0].Cwd != "/a" || seeds[0].Mode != "command" {
		t.Errorf("first seed wrong: %+v", seeds[0])
	}
	if seeds[1].Line != "why is the build red" || seeds[1].Mode != "agent" {
		t.Errorf("second seed wrong: %+v", seeds[1])
	}
}
