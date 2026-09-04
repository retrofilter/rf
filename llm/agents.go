package llm

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/retrofilter/rf/eval"
)

const (
	agentsFileName = "AGENTS.md"

	globalAgentsName = ".retrofilter.md"

	agentsFileCap = 32 * 1024
)

const agentsHeader = "The user provides the following project instructions (AGENTS.md). " +
	"Follow them; when files conflict, the one closest to the working directory takes precedence."

func agentsContext(dir string) string {
	type entry struct{ path, label string }
	var entries []entry
	if home, err := os.UserHomeDir(); err == nil {
		entries = append(entries, entry{filepath.Join(home, globalAgentsName), "~/" + globalAgentsName + " (global)"})
	}
	for _, p := range agentsFilePaths(dir) {
		entries = append(entries, entry{p, displayPath(p)})
	}

	var sections []string
	for _, e := range entries {
		if content, ok := readAgentsFile(e.path); ok {
			sections = append(sections, "Contents of "+e.label+":\n\n"+content)
		}
	}
	if len(sections) == 0 {
		return ""
	}
	return agentsHeader + "\n\n" + strings.Join(sections, "\n\n")
}

func agentsFilePaths(dir string) []string {
	root := contextRoot(dir)
	var paths []string
	for d := dir; ; d = filepath.Dir(d) {
		if p := filepath.Join(d, agentsFileName); isRegular(p) {
			paths = append(paths, p)
		}
		if d == root || d == filepath.Dir(d) {
			break
		}
	}
	for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
		paths[i], paths[j] = paths[j], paths[i]
	}
	return paths
}

func contextRoot(dir string) string {
	if _, hub, ok := eval.FindProject(dir); ok {
		return hub
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			return dir
		}
	}
}

func isRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func readAgentsFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	text := string(data)
	if len(text) > agentsFileCap {
		text = text[:agentsFileCap] + "\n[truncated]"
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	return text, true
}

func displayPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return path
}
