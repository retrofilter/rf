package console

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := NewServer(Config{
		Token:        "sekrit-sekrit-sekrit-sekrit-sekrit",
		SpawnCommand: "cat",
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func noRedirect(jar http.CookieJar) *http.Client {
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestAuthRejectsWithoutCredentials(t *testing.T) {
	_, ts := testServer(t)
	for _, path := range []string{"/ui/poll", "/static/style.css", "/api/attach/x"} {
		res, err := http.Get(ts.URL + path)
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, http.StatusUnauthorized, res.StatusCode, path)
	}
	client := noRedirect(nil)
	for _, path := range []string{"/", "/?token=wrong"} {
		res, err := client.Get(ts.URL + path)
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, http.StatusSeeOther, res.StatusCode, path)
		require.Equal(t, "/login", res.Header.Get("Location"), path)
		require.Empty(t, res.Cookies(), path)
	}
}

func TestLoginPageServesFormAndSignsIn(t *testing.T) {
	s, ts := testServer(t)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}

	// Unauthenticated root lands on the sign-in form.
	res, err := client.Get(ts.URL + "/")
	require.NoError(t, err)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	require.Equal(t, "/login", res.Request.URL.Path)
	require.Contains(t, string(body), `action="/login"`)
	// The pre-auth page leaks nothing dynamic.
	require.NotContains(t, string(body), whoami())

	// A wrong token re-renders the form with a generic error.
	res, err = client.PostForm(ts.URL+"/login", url.Values{"token": {"wrong"}})
	require.NoError(t, err)
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	require.Contains(t, string(body), "invalid token")
	u, _ := url.Parse(ts.URL)
	require.Empty(t, jar.Cookies(u))

	// The right token mints a session; the cookie then carries auth alone.
	res, err = client.PostForm(ts.URL+"/login", url.Values{"token": {s.cfg.Token}})
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, "/", res.Request.URL.Path)
	require.NotEmpty(t, jar.Cookies(u))
	res, err = client.Get(ts.URL + "/ui/poll")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)

	// The cookie is a session secret, never the token itself.
	for _, c := range jar.Cookies(u) {
		require.NotEqual(t, s.cfg.Token, c.Value)
	}

	// A signed-in browser visiting /login goes straight to the console.
	res, err = client.Get(ts.URL + "/login")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, "/", res.Request.URL.Path)
}

func TestSessionCookieSecureFollowsForwardedProto(t *testing.T) {
	s, ts := testServer(t)

	sessionCookie := func(res *http.Response) *http.Cookie {
		for _, c := range res.Cookies() {
			if c.Name == cookieName {
				return c
			}
		}
		return nil
	}

	login := func(forwardedProto string) *http.Cookie {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/login", strings.NewReader(url.Values{"token": {s.cfg.Token}}.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if forwardedProto != "" {
			req.Header.Set("X-Forwarded-Proto", forwardedProto)
		}
		res, err := noRedirect(nil).Do(req)
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, http.StatusSeeOther, res.StatusCode)
		c := sessionCookie(res)
		require.NotNil(t, c)
		return c
	}

	require.False(t, login("").Secure)
	require.True(t, login("https").Secure)
}

func TestAuthTokenTradesForSessionAndCleanURL(t *testing.T) {
	s, ts := testServer(t)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}

	res, err := client.Get(ts.URL + "/?token=" + s.cfg.Token)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	// The redirect stripped the token from the final URL.
	require.Equal(t, "", res.Request.URL.Query().Get("token"))
	require.Equal(t, "/", res.Request.URL.Path)

	// The session cookie now carries auth on its own.
	u, _ := url.Parse(ts.URL)
	require.NotEmpty(t, jar.Cookies(u))
	res, err = client.Get(ts.URL + "/ui/poll")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
}

func TestAuthTokenStripsOnDeepLinks(t *testing.T) {
	s, ts := testServer(t)
	client := noRedirect(nil)

	res, err := client.Get(ts.URL + "/ui/overview?token=" + s.cfg.Token)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusSeeOther, res.StatusCode)
	require.Equal(t, "/ui/overview", res.Header.Get("Location"))
	require.NotEmpty(t, res.Cookies())
}

func TestLaunchNonceSignsInOnce(t *testing.T) {
	s, ts := testServer(t)
	nonce := s.MintLaunchNonce()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}
	res, err := client.Get(ts.URL + "/?token=" + nonce)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "/", res.Request.URL.Path)
	res, err = client.Get(ts.URL + "/ui/poll")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)

	// Spent: a second browser replaying the launch URL is refused.
	replay := noRedirect(nil)
	res, err = replay.Get(ts.URL + "/?token=" + nonce)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, "/login", res.Header.Get("Location"))
	require.Empty(t, res.Cookies())
}

func TestStaticAssetsRevalidateByETag(t *testing.T) {
	s, ts := testServer(t)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}
	res, err := client.Get(ts.URL + "/?token=" + s.cfg.Token)
	require.NoError(t, err)
	res.Body.Close()

	res, err = client.Get(ts.URL + "/static/style.css")
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "no-cache", res.Header.Get("Cache-Control"))
	tag := res.Header.Get("ETag")
	require.NotEmpty(t, tag)

	req, err := http.NewRequest("GET", ts.URL+"/static/style.css", nil)
	require.NoError(t, err)
	req.Header.Set("If-None-Match", tag)
	res, err = client.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusNotModified, res.StatusCode)
}

func TestHookEndpointRequiresQueryToken(t *testing.T) {
	s, ts := testServer(t)

	res, err := http.Post(ts.URL+"/api/hook?session=x", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)

	body := `{"hook_event_name":"Notification","transcript_path":""}`
	res, err = http.Post(ts.URL+"/api/hook?session=x&token="+s.cfg.Token, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusNoContent, res.StatusCode)
	require.Equal(t, StatusBlocked, s.reg.hooks["x"].Status)
}

func TestHookEndpointTracksClaudeSession(t *testing.T) {
	s, ts := testServer(t)
	_, err := s.mgr.Spawn("alpha", "", "")
	require.NoError(t, err)
	claudeOf := func() string {
		return s.mgr.Get(s.mgr.FindByName("alpha")).snapshot().ClaudeID
	}
	post := func(query, body string) {
		res, err := http.Post(ts.URL+"/api/hook?session=alpha&token="+s.cfg.Token+query, "application/json", strings.NewReader(body))
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, http.StatusNoContent, res.StatusCode)
	}

	post("", `{"hook_event_name":"SessionStart","session_id":"aaaa-1111-bbbb"}`)
	require.Equal(t, "aaaa-1111-bbbb", claudeOf())

	// The query-parameter spelling (the Console client, client.go) works too.
	post("&event=PreToolUse&claude=cccc-2222-dddd", "{}")
	require.Equal(t, "cccc-2222-dddd", claudeOf())

	// Events without an id leave the tracked one alone.
	post("", `{"hook_event_name":"Stop"}`)
	require.Equal(t, "cccc-2222-dddd", claudeOf())

	post("", `{"hook_event_name":"SessionEnd","session_id":"cccc-2222-dddd"}`)
	require.Empty(t, claudeOf())
}

func TestEmptyTokenNeverMatches(t *testing.T) {
	s := NewServer(Config{Token: ""})
	require.False(t, s.auth.tokenOK(""))
	require.False(t, s.auth.tokenOK("anything"))
}
