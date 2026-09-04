package eval

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchBuiltin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("X-Custom", "yes")
			fmt.Fprint(w, "hello body")
		case "/big":
			fmt.Fprint(w, strings.Repeat("x", 100))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ev := NewEvaluator()
	env := ev.globalEnv
	fetch := func(expr string) (Value, error) { return evalExpr(expr, ev, env) }

	got, err := fetch(fmt.Sprintf("(fetch %q)", srv.URL+"/ok"))
	if err != nil || got != String("hello body\n") {
		t.Errorf("(fetch /ok) = %v, %v; want body text", got, err)
	}

	// :full returns {:status :body :headers} and never errors on status
	got, err = fetch(fmt.Sprintf("(fetch %q :full)", srv.URL+"/ok"))
	if err != nil {
		t.Fatalf("(fetch :full): %v", err)
	}
	dict, ok := got.(Dictionary)
	if !ok || dict["status"] != Integer(200) || dict["body"] != String("hello body") {
		t.Errorf("(fetch :full) = %v, want status 200 + body", got)
	}
	if headers, ok := dict["headers"].(Dictionary); !ok || headers["X-Custom"] != String("yes") {
		t.Errorf("(fetch :full) headers = %v, want X-Custom yes", dict["headers"])
	}

	// non-2xx errors without :full, is data with it
	if _, err := fetch(fmt.Sprintf("(fetch %q)", srv.URL+"/missing")); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("(fetch /missing) error = %v, want 404", err)
	}
	got, err = fetch(fmt.Sprintf("(fetch %q :full)", srv.URL+"/missing"))
	if err != nil {
		t.Fatalf("(fetch /missing :full): %v", err)
	}
	if dict, ok := got.(Dictionary); !ok || dict["status"] != Integer(404) {
		t.Errorf("(fetch /missing :full) = %v, want status 404", got)
	}

	// only http(s) urls, and only the :full option
	for _, expr := range []string{
		`(fetch "file:///etc/passwd")`,
		`(fetch "notaurl")`,
		`(fetch)`,
		fmt.Sprintf("(fetch %q :nope)", srv.URL),
		"(fetch 42)",
	} {
		if _, err := fetch(expr); err == nil {
			t.Errorf("%s should error", expr)
		}
	}

	// the size cap rejects oversized bodies
	origCap := fetchCap
	fetchCap = 10
	defer func() { fetchCap = origCap }()
	if _, err := fetch(fmt.Sprintf("(fetch %q)", srv.URL+"/big")); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("(fetch /big) error = %v, want size-cap error", err)
	}
	fetchCap = origCap

	// approval-gated like sh: outbound requests can be denied
	var asked string
	ev.SetApprover(func(action string) bool { asked = action; return false })
	if _, err := fetch(fmt.Sprintf("(fetch %q)", srv.URL+"/ok")); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("gated fetch error = %v, want denial", err)
	}
	if !strings.Contains(asked, "fetch") || !strings.Contains(asked, srv.URL) {
		t.Errorf("approver saw %q, want the fetch url", asked)
	}
	ev.SetApprover(nil)
}
