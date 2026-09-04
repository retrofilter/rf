package eval

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var fetchCap = 5 << 20 // 5 MB

const fetchTimeout = 30 * time.Second

func fetchBuiltins(env *Environment, approval *approvalGate) {
	client := &http.Client{Timeout: fetchTimeout}
	Register("fetch", "fetch an http(s) url, body as a line stream", CommandMeta{
		Command: true, MinArgs: 1, MaxArgs: 1, Usage: "url",
		Options: []Option{{Long: "full", Short: "f", Kind: OptionBool, Doc: "return {:status :body :headers} (body as one string) instead of the line stream"}}})
	env.Set("fetch", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("fetch", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, errors.New("fetch expects a url: (fetch url [{:full #t}])")
		}
		rawURL, ok := pos[0].(String)
		if !ok {
			return nil, errors.New("fetch expects a string url")
		}
		full := OptBool(opts, "full")
		u, err := url.Parse(string(rawURL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("fetch expects an http(s) url, got %q", string(rawURL))
		}
		if err := approval.require(fmt.Sprintf("fetch %q", string(rawURL))); err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("fetch: %v", err)
		}
		req.Header.Set("User-Agent", "retrofilter")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, int64(fetchCap)+1))
		if err != nil {
			return nil, fmt.Errorf("fetch: %v", err)
		}
		if len(body) > fetchCap {
			return nil, fmt.Errorf("fetch: response exceeds %d bytes", fetchCap)
		}
		if full {
			headers := Dictionary{}
			for name := range resp.Header {
				headers[name] = String(resp.Header.Get(name))
			}
			return Dictionary{
				"status":  Integer(resp.StatusCode),
				"body":    String(body),
				"headers": headers,
			}, nil
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("fetch: %s returned %s — use :full to inspect the response", string(rawURL), resp.Status)
		}
		return streamFromReader(bytes.NewReader(body), nil), nil
	}))
}
