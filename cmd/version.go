package cmd

import (
	"runtime"
	"runtime/debug"
	"strings"
)

var version string

// Version reports what a bug report needs: the tag (or module version), the
// commit it was built from, whether the tree was dirty, and the platform.
func Version() string {
	tag := version
	commit, modified := "", false
	if info, ok := debug.ReadBuildInfo(); ok {
		if tag == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			tag = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				commit = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	if tag == "" {
		tag = "devel"
	}
	var b strings.Builder
	b.WriteString(tag)
	if commit != "" {
		if len(commit) > 12 {
			commit = commit[:12]
		}
		b.WriteString(" (" + commit)
		if modified {
			b.WriteString("-dirty")
		}
		b.WriteString(")")
	}
	b.WriteString(" " + runtime.GOOS + "/" + runtime.GOARCH + " " + runtime.Version())
	return b.String()
}
