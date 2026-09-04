package console

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenameEndpoint(t *testing.T) {
	s, ts := testServer(t)
	_, err := s.mgr.Spawn("alpha", "", "")
	require.NoError(t, err)
	_, err = s.mgr.Spawn("beta", "", "")
	require.NoError(t, err)

	post := func(query string) int {
		res, err := http.Post(ts.URL+"/api/rename?"+query, "", nil)
		require.NoError(t, err)
		res.Body.Close()
		return res.StatusCode
	}

	// Token required.
	require.Equal(t, http.StatusUnauthorized, post("session=alpha&name=work"))

	tok := "&token=" + s.cfg.Token
	require.Equal(t, http.StatusNoContent, post("session=alpha&name=work"+tok))
	require.NotEmpty(t, s.mgr.FindByName("work"))
	require.Empty(t, s.mgr.FindByName("alpha"))

	// The spawn name keeps identifying the session — rename again.
	require.Equal(t, http.StatusNoContent, post("session=alpha&name=work2"+tok))
	require.NotEmpty(t, s.mgr.FindByName("work2"))

	// Collisions with another session's name or spawn name refuse.
	require.Equal(t, http.StatusConflict, post("session=alpha&name=beta"+tok))
	require.Equal(t, http.StatusNotFound, post("session=nope&name=x"+tok))
	require.Equal(t, http.StatusBadRequest, post("session=alpha&name=bad name"+tok))
	require.Equal(t, http.StatusBadRequest, post("name=x"+tok))
}

func TestRenameKeepsHookStatus(t *testing.T) {
	s, ts := testServer(t)
	_, err := s.mgr.Spawn("alpha", "", "")
	require.NoError(t, err)
	s.reg.ReportHook("alpha", "Notification")

	res, err := http.Post(ts.URL+"/api/rename?session=alpha&name=work&token="+s.cfg.Token, "", nil)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusNoContent, res.StatusCode)

	rows, err := s.reg.Sessions()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "work", rows[0].Name)
	require.Equal(t, StatusBlocked, rows[0].Status)
}

func TestSpawnNameStaysReserved(t *testing.T) {
	m := NewManager("cat", 0)
	_, err := m.Spawn("alpha", "", "")
	require.NoError(t, err)
	require.NoError(t, m.Rename("alpha", "work"))
	_, err = m.Spawn("alpha", "", "")
	require.ErrorIs(t, err, ErrNameTaken)
	// Renaming another session onto a live spawn name refuses too.
	_, err = m.Spawn("beta", "", "")
	require.NoError(t, err)
	require.ErrorIs(t, m.Rename("beta", "alpha"), ErrNameTaken)
	// Renaming back to your own spawn name is fine.
	require.NoError(t, m.Rename("alpha", "alpha"))
}

func TestSessionsEndpoint(t *testing.T) {
	s, ts := testServer(t)
	_, err := s.mgr.Spawn("alpha", "", "")
	require.NoError(t, err)

	res, err := http.Get(ts.URL + "/api/sessions")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)

	c := &Console{BaseURL: ts.URL, Token: s.cfg.Token}
	rows, err := c.Sessions()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "alpha", rows[0].Name)
}

func TestNewSessionEndpointAndClient(t *testing.T) {
	s, ts := testServer(t)
	c := &Console{BaseURL: ts.URL, Token: s.cfg.Token}

	dir := t.TempDir()
	require.NoError(t, c.NewSession("alpha", dir, ""))
	id := s.mgr.FindByName("alpha")
	require.NotEmpty(t, id)
	require.Equal(t, dir, s.mgr.Get(id).snapshot().Cwd)

	// Not attach-or-create from a shell: an existing name is a conflict.
	err := c.NewSession("alpha", "", "")
	require.ErrorContains(t, err, "already exists")

	require.ErrorContains(t, c.NewSession("bad name", "", ""), "must match")
	require.ErrorContains(t, c.NewSession("gone", dir+"/missing", ""), "no such directory")
}

func TestConsoleClientRename(t *testing.T) {
	s, ts := testServer(t)
	_, err := s.mgr.Spawn("alpha", "", "")
	require.NoError(t, err)

	c := &Console{BaseURL: ts.URL, Token: s.cfg.Token, Session: "alpha"}
	require.NoError(t, c.Rename("work"))
	require.NotEmpty(t, s.mgr.FindByName("work"))

	// Outside a console session there is nothing to rename.
	detached := &Console{BaseURL: ts.URL, Token: s.cfg.Token}
	require.ErrorContains(t, detached.Rename("x"), "not inside a console session")

	// An unreachable console names the failure.
	dead := &Console{BaseURL: "http://127.0.0.1:1", Token: "t", Session: "alpha"}
	err = dead.Rename("x")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "is rf console running"), err.Error())
}
