package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetEnvVarCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".rf.env")
	if err := SetEnvVar(path, "ANTHROPIC_API_KEY", "sk-test"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ANTHROPIC_API_KEY=sk-test\n" {
		t.Fatalf("unexpected content: %q", data)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected 0600, got %o", perm)
	}
	if !EnvFileDefines(path, "ANTHROPIC_API_KEY") {
		t.Fatal("EnvFileDefines should see the new assignment")
	}
	if EnvFileDefines(path, "OTHER") {
		t.Fatal("EnvFileDefines should not match OTHER")
	}
}

func TestSetEnvVarReplacesInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".rf.env")
	initial := "# my env\nexport ANTHROPIC_API_KEY=old\nOTHER=keep\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SetEnvVar(path, "ANTHROPIC_API_KEY", "new"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := "# my env\nexport ANTHROPIC_API_KEY=new\nOTHER=keep\n"
	if string(data) != want {
		t.Fatalf("got %q, want %q", data, want)
	}
}

func TestSetEnvVarRemoves(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".rf.env")
	initial := "A=1\nANTHROPIC_API_KEY=sk\nB=2\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SetEnvVar(path, "ANTHROPIC_API_KEY", ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "A=1\nB=2\n" {
		t.Fatalf("unexpected content: %q", data)
	}
	if EnvFileDefines(path, "ANTHROPIC_API_KEY") {
		t.Fatal("assignment should be gone")
	}
}

func TestSetEnvVarNoFalsePrefixMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".rf.env")
	initial := "ANTHROPIC_API_KEY_BACKUP=x\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	if EnvFileDefines(path, "ANTHROPIC_API_KEY") {
		t.Fatal("prefix-similar name must not match")
	}
	if err := SetEnvVar(path, "ANTHROPIC_API_KEY", "sk"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := "ANTHROPIC_API_KEY_BACKUP=x\nANTHROPIC_API_KEY=sk\n"
	if string(data) != want {
		t.Fatalf("got %q, want %q", data, want)
	}
}
