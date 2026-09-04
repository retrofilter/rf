package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func clockAt(a *authState) *time.Time {
	now := time.Now()
	a.now = func() time.Time { return now }
	return &now
}

func TestSessionSlidesAndExpires(t *testing.T) {
	a := newAuthState("tok", "", "")
	now := clockAt(a)

	secret := a.mintSession()
	require.True(t, a.sessionOK(secret))
	require.False(t, a.sessionOK("not-a-session"))

	for i := 0; i < 2; i++ {
		*now = now.Add(sessionIdle - time.Hour)
		require.True(t, a.sessionOK(secret), "visit %d", i)
	}

	// Going quiet longer than the idle window signs the browser out.
	fresh := a.mintSession()
	*now = now.Add(sessionIdle + time.Minute)
	require.False(t, a.sessionOK(fresh))
	require.False(t, a.sessionOK(fresh), "expired session stays gone")
}

func TestSessionHardCapBeatsConstantUse(t *testing.T) {
	a := newAuthState("tok", "", "")
	now := clockAt(a)

	secret := a.mintSession()
	deadline := now.Add(sessionMaxAge + time.Hour)
	for now.Before(deadline) {
		*now = now.Add(time.Hour)
		a.sessionOK(secret)
	}
	require.False(t, a.sessionOK(secret))
}

func TestSessionsPersistHashedAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "websessions.json")
	a := newAuthState("tok", "", path)
	secret := a.mintSession()

	// The file holds hashes, never the secret, and only the secret signs in.
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(b), secret)

	reborn := newAuthState("tok", "", path)
	require.True(t, reborn.sessionOK(secret))
	require.False(t, reborn.sessionOK(hashSecret(secret)), "the stored hash must not work as a cookie")

	// A corrupt file means an empty session set, never a crash.
	require.NoError(t, os.WriteFile(path, []byte("{broken"), 0600))
	third := newAuthState("tok", "", path)
	require.False(t, third.sessionOK(secret))
}

func TestNonceSingleUseAndTTL(t *testing.T) {
	a := newAuthState("tok", "", "")
	now := clockAt(a)

	n := a.mintNonce()
	require.True(t, a.takeNonce(n))
	require.False(t, a.takeNonce(n), "a nonce spends exactly once")

	late := a.mintNonce()
	*now = now.Add(nonceTTL + time.Second)
	require.False(t, a.takeNonce(late))
	require.Empty(t, a.nonces, "expired nonces are purged")

	// A nonce never doubles as the token.
	require.False(t, a.tokenOK(a.mintNonce()))
}

func TestCurrentTokenFollowsFileAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webtoken")
	first, err := loadTokenAt(path, false)
	require.NoError(t, err)

	a := newAuthState("", path, "")
	now := clockAt(a)
	require.True(t, a.tokenOK(first))

	second, err := loadTokenAt(path, true)
	require.NoError(t, err)
	require.True(t, a.tokenOK(first), "inside the throttle the old read stands")
	*now = now.Add(tokenRecheck)
	require.False(t, a.tokenOK(first))
	require.True(t, a.tokenOK(second))

	// An aged-out file rotates on the server's own read.
	old := time.Now().Add(-tokenMaxAge - time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))
	*now = now.Add(tokenRecheck)
	require.False(t, a.tokenOK(second))
	rotated, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, a.tokenOK(strings.TrimSpace(string(rotated))))
}
