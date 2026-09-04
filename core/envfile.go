package core

import (
	"os"
	"strings"
)

func envAssignPrefix(line, name string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	prefix := line[:len(line)-len(trimmed)]
	if rest, ok := strings.CutPrefix(trimmed, "export "); ok {
		prefix += "export "
		trimmed = strings.TrimLeft(rest, " \t")
		prefix += rest[:len(rest)-len(trimmed)]
	}
	if strings.HasPrefix(trimmed, name+"=") {
		return prefix, true
	}
	return "", false
}

// EnvFileDefines reports whether the env file at path assigns name.
func EnvFileDefines(path, name string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if _, ok := envAssignPrefix(line, name); ok {
			return true
		}
	}
	return false
}

// SetEnvVar sets name=value in the env file at path, replacing an existing
// assignment in place (keeping any `export ` spelling) and appending
// otherwise.
func SetEnvVar(path, name, value string) error {
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	var out []string
	replaced := false
	for _, line := range lines {
		prefix, ok := envAssignPrefix(line, name)
		if !ok {
			out = append(out, line)
			continue
		}
		if value != "" && !replaced {
			out = append(out, prefix+name+"="+value)
			replaced = true
		}
		// removal (or a duplicate assignment): drop the line
	}
	if value != "" && !replaced {
		out = append(out, name+"="+value)
	}

	content := strings.Join(out, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0600)
}
