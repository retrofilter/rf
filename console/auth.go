package console

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"
	"time"
)

const (
	sessionIdle      = 14 * 24 * time.Hour
	sessionMaxAge    = 30 * 24 * time.Hour
	nonceTTL         = 2 * time.Minute
	tokenRecheck     = 5 * time.Second
	sessionSaveEvery = time.Minute
)

type webSession struct {
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
}

type authState struct {
	mu sync.Mutex

	tokenPath    string
	token        string
	tokenChecked time.Time

	sessions     map[string]*webSession
	sessionsPath string
	dirty        bool
	saved        time.Time

	nonces map[string]time.Time

	now func() time.Time // test seam
}

func newAuthState(token, tokenPath, sessionsPath string) *authState {
	a := &authState{
		token:        token,
		tokenPath:    tokenPath,
		sessions:     map[string]*webSession{},
		sessionsPath: sessionsPath,
		nonces:       map[string]time.Time{},
		now:          time.Now,
	}
	a.loadSessions()
	return a
}

func (a *authState) currentToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tokenPath == "" {
		return a.token
	}
	if now := a.now(); now.Sub(a.tokenChecked) >= tokenRecheck {
		a.tokenChecked = now
		if tok, err := loadTokenAt(a.tokenPath, false); err == nil {
			a.token = tok
		}
	}
	return a.token
}

func (a *authState) tokenOK(candidate string) bool {
	tok := a.currentToken()
	if tok == "" || candidate == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(tok)) == 1
}

func (a *authState) mintSession() string {
	secret := newSecret()
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.sessions[hashSecret(secret)] = &webSession{Created: now, LastSeen: now}
	a.dirty = true
	a.saveSessionsLocked(true)
	return secret
}

func (a *authState) sessionOK(secret string) bool {
	if secret == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := hashSecret(secret)
	s, ok := a.sessions[key]
	if !ok {
		return false
	}
	now := a.now()
	if now.Sub(s.Created) > sessionMaxAge || now.Sub(s.LastSeen) > sessionIdle {
		delete(a.sessions, key)
		a.dirty = true
		a.saveSessionsLocked(true)
		return false
	}
	s.LastSeen = now
	a.dirty = true
	a.saveSessionsLocked(false)
	return true
}

func (a *authState) mintNonce() string {
	n := newSecret()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nonces[n] = a.now().Add(nonceTTL)
	return n
}

func (a *authState) takeNonce(candidate string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for n, exp := range a.nonces {
		if now.After(exp) {
			delete(a.nonces, n)
		}
	}
	if _, ok := a.nonces[candidate]; !ok {
		return false
	}
	delete(a.nonces, candidate)
	return true
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func (a *authState) loadSessions() {
	if a.sessionsPath == "" {
		return
	}
	b, err := os.ReadFile(a.sessionsPath)
	if err != nil {
		return
	}
	stored := map[string]*webSession{}
	if json.Unmarshal(b, &stored) != nil {
		return
	}
	now := a.now()
	for key, s := range stored {
		if s == nil || len(key) != 64 {
			continue
		}
		if now.Sub(s.Created) > sessionMaxAge || now.Sub(s.LastSeen) > sessionIdle {
			continue
		}
		a.sessions[key] = s
	}
}

func (a *authState) saveSessionsLocked(force bool) {
	if a.sessionsPath == "" || !a.dirty {
		return
	}
	now := a.now()
	if !force && now.Sub(a.saved) < sessionSaveEvery {
		return
	}
	b, err := json.Marshal(a.sessions)
	if err != nil {
		return
	}
	if os.WriteFile(a.sessionsPath, append(b, '\n'), 0600) == nil {
		a.dirty = false
		a.saved = now
	}
}
