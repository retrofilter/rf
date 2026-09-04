package cmd

import (
	"os"
	"testing"
)

func TestHandbookCurrent(t *testing.T) {
	want, err := generateHandbook()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../HANDBOOK.md")
	if err != nil {
		t.Fatalf("HANDBOOK.md missing — run `make handbook`: %v", err)
	}
	if string(got) != want {
		t.Error("HANDBOOK.md is stale — run `make handbook`")
	}
}
