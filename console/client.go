package console

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultPort is where rf console listens unless told otherwise.
const DefaultPort = 7433

type Console struct {
	BaseURL string // http://127.0.0.1:<port>
	Token   string
	Session string // this shell's RF_SESSION spawn name; "" outside a console session

	client *http.Client
}

// NewFromEnv builds the client from the process environment and the
// token file. It errors only when no token exists — reaching a console that
// isn't running surfaces per-request instead.
func NewFromEnv() (*Console, error) {
	path, err := tokenPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no console token at %s — run rf console once to create it", path)
	}
	port := DefaultPort
	if p, err := strconv.Atoi(os.Getenv("RF_WEB_PORT")); err == nil && p > 0 {
		port = p
	}
	return &Console{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		Token:   strings.TrimSpace(string(b)),
		Session: os.Getenv("RF_SESSION"),
	}, nil
}

func (c *Console) do(method, path string, query url.Values) ([]byte, error) {
	if c.client == nil {
		c.client = &http.Client{Timeout: 5 * time.Second}
	}
	query.Set("token", c.Token)
	req, err := http.NewRequest(method, c.BaseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("no console at %s — is rf console running?", c.BaseURL)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("console: %s", msg)
	}
	return body, nil
}

// Rename renames this shell's own console session; the identity is the
// spawn-time RF_SESSION, so repeated renames keep working.
func (c *Console) Rename(name string) error {
	if c.Session == "" {
		return fmt.Errorf("not inside a console session (RF_SESSION is unset)")
	}
	_, err := c.do(http.MethodPost, "/api/rename", url.Values{"session": {c.Session}, "name": {name}})
	return err
}

// ReportHook reports one Claude Code hook event for this shell's console
// session — the status feed behind the rail (hooks.go).
func (c *Console) ReportHook(event, claudeID string) error {
	if c.Session == "" {
		return fmt.Errorf("not inside a console session (RF_SESSION is unset)")
	}
	values := url.Values{"session": {c.Session}, "event": {event}}
	if claudeID != "" {
		values.Set("claude", claudeID)
	}
	_, err := c.do(http.MethodPost, "/api/hook", values)
	return err
}

// Sessions returns the console's fleet — the same rows the rail shows: the
// live sessions, then the scheduled ones (StatusScheduled, with Next).
func (c *Console) Sessions() ([]Session, error) {
	body, err := c.do(http.MethodGet, "/api/sessions", url.Values{})
	if err != nil {
		return nil, err
	}
	var sessions []Session
	if err := json.Unmarshal(body, &sessions); err != nil {
		return nil, fmt.Errorf("console: %v", err)
	}
	return sessions, nil
}

// NewSession spawns a console session named name in dir ("" means $HOME),
// with run typed into the fresh shell ("" types nothing). An existing name
// is an error — a shell can't attach, so there is no attach-or-create.
func (c *Console) NewSession(name, dir, run string) error {
	_, err := c.do(http.MethodPost, "/api/sessions", url.Values{"name": {name}, "dir": {dir}, "run": {run}})
	return err
}
