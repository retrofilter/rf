package console

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/retrofilter/rf/core"
)

const tokenMaxAge = 7 * 24 * time.Hour

func tokenPath() (string, error) {
	return core.WebTokenPath()
}

// LoadToken returns the persistent web token, generating it when absent and
// rotating it when asked or when the file is older than tokenMaxAge.
func LoadToken(rotate bool) (string, error) {
	path, err := tokenPath()
	if err != nil {
		return "", err
	}
	return loadTokenAt(path, rotate)
}

func loadTokenAt(path string, rotate bool) (string, error) {
	if !rotate {
		if b, err := os.ReadFile(path); err == nil {
			tok := strings.TrimSpace(string(b))
			if len(tok) >= 32 && !tokenExpired(path) {
				return tok, nil
			}
		}
	}
	tok := newSecret()
	if err := os.WriteFile(path, []byte(tok+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return tok, nil
}

func tokenExpired(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > tokenMaxAge
}

func newSecret() string {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(raw)
}
