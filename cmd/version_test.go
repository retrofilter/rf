package cmd

import (
	"runtime"
	"strings"
	"testing"
)

func TestVersionAlwaysIdentifies(t *testing.T) {
	v := Version()
	if !strings.Contains(v, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("Version() = %q, want platform", v)
	}
	if !strings.Contains(v, runtime.Version()) {
		t.Fatalf("Version() = %q, want Go version", v)
	}
	if strings.HasPrefix(v, " ") {
		t.Fatalf("Version() = %q, empty tag", v)
	}

	// An injected tag wins over whatever the build info says.
	defer func(old string) { version = old }(version)
	version = "v9.9.9"
	if got := Version(); !strings.HasPrefix(got, "v9.9.9 ") {
		t.Fatalf("Version() with tag = %q", got)
	}
}
