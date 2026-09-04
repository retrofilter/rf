package core

import (
	"os"
	"path/filepath"
)

const (
	// PreludeName is the Scheme prelude, evaluated at shell startup.
	PreludeName = ".rf.scm"
	// EnvName holds environment variables loaded at startup.
	EnvName = ".rf.env"
	// StateDirName is the state directory; created 0700 on demand
	// because the web token lives inside it.
	StateDirName = ".rf"

	dbName          = "main.db"
	webTokenName    = "webtoken"
	webStateName    = "state.json"
	webSessionsName = "websessions.json"

	// WebTokenRel is the token's $HOME-relative path with forward
	// slashes, for embedding in shell command lines.
	WebTokenRel = StateDirName + "/" + webTokenName
)

// PreludePath returns ~/.rf.scm.
func PreludePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, PreludeName), nil
}

// EnvPath returns ~/.rf.env.
func EnvPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, EnvName), nil
}

// DBPath returns ~/.rf/main.db, creating ~/.rf if needed.
func DBPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, dbName), nil
}

// WebTokenPath returns ~/.rf/webtoken, creating ~/.rf if needed.
func WebTokenPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, webTokenName), nil
}

// WebSessionsPath returns ~/.rf/websessions.json — the console's signed-in
// browser sessions (hashed) — creating ~/.rf if needed.
func WebSessionsPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, webSessionsName), nil
}

// WebStatePath returns ~/.rf/state.json — the console's fleet state file
// for restart recovery — creating ~/.rf if needed.
func WebStatePath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, webStateName), nil
}

func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, StateDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
