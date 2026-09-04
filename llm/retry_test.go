package llm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/models"
	_ "modernc.org/sqlite"
)

func TestIsRetryableError(t *testing.T) {
	retryable := []error{
		// Structural: HTTP status and envelope type carried on *APIError.
		&APIError{StatusCode: 429, Type: "rate_limit_error", Message: "Number of requests has exceeded your per-minute rate limit"},
		&APIError{StatusCode: 529, Message: ""},
		&APIError{StatusCode: 503, Message: "service unavailable"},
		&APIError{StatusCode: 500, Message: "unhelpful HTML error page"}, // no envelope, status alone decides
		// Mid-stream SSE error events carry a type but no status.
		&APIError{Type: "overloaded_error", Message: "Overloaded"},
		&APIError{Type: "api_error", Message: "An unexpected error has occurred internal to Anthropic's systems"},
		// The truncated-stream sentinel, wrapped the way readStream returns it.
		fmt.Errorf("read stream: %w", errTruncatedStream),
		// Transport failures keep their net/http wording — text fallback.
		errors.New(`Post "http://localhost:11434/v1/messages": dial tcp: connection refused`),
		errors.New(`Post "https://api.anthropic.com/v1/messages": net/http: request canceled (Client.Timeout exceeded while awaiting headers)`),
		errors.New("read stream: unexpected EOF"),
	}
	for _, err := range retryable {
		if !isRetryableError(err) {
			t.Errorf("expected retryable: %q", err)
		}
	}

	nonRetryable := []error{
		&APIError{StatusCode: 401, Type: "authentication_error", Message: "invalid x-api-key"},
		&APIError{StatusCode: 400, Type: "invalid_request_error", Message: "max_tokens: must be greater than 0"},
		&APIError{StatusCode: 403, Type: "permission_error", Message: "your API key does not have permission"},
		&APIError{StatusCode: 404, Type: "not_found_error", Message: "model not found"},
		&APIError{StatusCode: 429, Type: "rate_limit_error", Message: "your credit balance is too low"},
		&APIError{StatusCode: 429, Message: "insufficient_quota: quota exceeded for this billing period"},
		// An unknown 4xx with no recognized wording stays non-retryable.
		&APIError{StatusCode: 418, Message: "teapot"},
		errors.New("parse error: expected 429 tokens, got 503"),
		errors.New("parse error: unexpected token"),
		errors.New("context canceled"),
		fmt.Errorf("read stream: %w", bufio.ErrTooLong),
	}
	for _, err := range nonRetryable {
		if isRetryableError(err) {
			t.Errorf("expected non-retryable: %q", err)
		}
	}

	if isRetryableError(nil) {
		t.Error("nil error must not be retryable")
	}
}

func TestIsContextOverflow(t *testing.T) {
	overflow := []string{
		"invalid_request_error: prompt is too long: 213462 tokens > 200000 maximum",
		`request_too_large: Request exceeds the maximum size`,
		"prompt too long; exceeded max context length by 1234 tokens",       // Ollama
		"the request exceeds the available context size, try increasing it", // llama.cpp
		"context_length_exceeded",
		"token limit exceeded",
	}
	for _, msg := range overflow {
		if !IsContextOverflow(errors.New(msg)) {
			t.Errorf("expected overflow: %q", msg)
		}
		if isRetryableError(errors.New(msg)) {
			t.Errorf("overflow must not be retryable: %q", msg)
		}
	}

	notOverflow := []string{
		"ThrottlingException: Too many tokens, please wait before trying again", // throttling, not overflow
		"rate_limit_error: too many requests",
		"overloaded_error: Overloaded",
		"invalid_request_error: max_tokens: must be greater than 0",
		"parse error: unexpected token",
	}
	for _, msg := range notOverflow {
		if IsContextOverflow(errors.New(msg)) {
			t.Errorf("expected not overflow: %q", msg)
		}
	}

	if IsContextOverflow(nil) {
		t.Error("nil error must not be overflow")
	}
}

type flakyModel struct {
	errs  []error
	resp  *Response
	calls int
}

func (f *flakyModel) Messages(ctx context.Context, req *Request, onDelta func(DeltaKind, string)) (*Response, error) {
	f.calls++
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return nil, err
	}
	return f.resp, nil
}

func newRetryTestChat(t *testing.T, model Model) *Chat {
	t.Helper()
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	prev := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = prev })
	return newChat(core.NewGraphStore(db), Config{Provider: models.ProviderOllama, Model: "fake"}, model)
}

func TestMessageRetriesTransientErrors(t *testing.T) {
	fake := &flakyModel{
		errs: []error{
			&APIError{Type: "overloaded_error", Message: "Overloaded"},
			&APIError{StatusCode: 529},
		},
		resp: textResponse("ok"),
	}
	chat := newRetryTestChat(t, fake)

	var retries []string
	chat.OnEvent = func(event Event, msg *models.Message) {
		if event == EventRetry {
			retries = append(retries, msg.Content)
		}
	}

	replies, err := chat.Message(context.Background(), "hi", nil, "")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if fake.calls != 3 {
		t.Fatalf("expected 3 model calls (2 failures + success), got %d", fake.calls)
	}
	if len(retries) != 2 {
		t.Fatalf("expected 2 EventRetry notices, got %d: %v", len(retries), retries)
	}
	if !strings.Contains(retries[0], "attempt 1/3") || !strings.Contains(retries[0], "overloaded_error") {
		t.Errorf("first retry notice = %q", retries[0])
	}
	if !strings.Contains(retries[1], "attempt 2/3") {
		t.Errorf("second retry notice = %q", retries[1])
	}
	if len(replies) != 1 || replies[0].Content != "ok" {
		t.Fatalf("unexpected replies: %+v", replies)
	}
}

func TestMessageNonRetryableErrorSurfaces(t *testing.T) {
	fake := &flakyModel{errs: []error{errors.New("authentication_error: invalid x-api-key")}}
	chat := newRetryTestChat(t, fake)

	_, err := chat.Message(context.Background(), "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "authentication_error") {
		t.Fatalf("expected authentication error, got %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 model call, got %d", fake.calls)
	}
}

func TestMessageRetryExhaustion(t *testing.T) {
	errs := make([]error, maxRetries+2)
	for i := range errs {
		errs[i] = fmt.Errorf("overloaded_error: Overloaded (%d)", i)
	}
	fake := &flakyModel{errs: errs}
	chat := newRetryTestChat(t, fake)

	_, err := chat.Message(context.Background(), "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "overloaded_error") {
		t.Fatalf("expected overloaded error, got %v", err)
	}
	if fake.calls != maxRetries+1 {
		t.Fatalf("expected %d model calls, got %d", maxRetries+1, fake.calls)
	}
}

type cancelingModel struct {
	cancel context.CancelFunc
	calls  int
}

func (m *cancelingModel) Messages(ctx context.Context, req *Request, onDelta func(DeltaKind, string)) (*Response, error) {
	m.calls++
	m.cancel()
	return nil, errors.New("overloaded_error: Overloaded")
}

func TestMessageRetryCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &cancelingModel{cancel: cancel}
	chat := newRetryTestChat(t, fake)

	_, err := chat.Message(ctx, "hi", nil, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 model call, got %d", fake.calls)
	}
}

func TestMessageRetryCancelDuringBackoff(t *testing.T) {
	fake := &flakyModel{errs: []error{
		errors.New("overloaded_error: Overloaded"),
		errors.New("overloaded_error: Overloaded"),
	}}
	chat := newRetryTestChat(t, fake)
	retryBaseDelay = time.Minute // cancel fires long before the wait ends

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(10*time.Millisecond, cancel)

	start := time.Now()
	_, err := chat.Message(ctx, "hi", nil, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 model call, got %d", fake.calls)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("backoff sleep was not interrupted (took %s)", elapsed)
	}
}

func TestAdviseConfigure(t *testing.T) {
	fake := &flakyModel{errs: []error{errors.New("authentication_error: invalid x-api-key")}}
	chat := newRetryTestChat(t, fake)
	_, err := chat.Message(context.Background(), "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "no model set — run `configure`") {
		t.Fatalf("expected no-model hint, got %v", err)
	}

	fake = &flakyModel{errs: []error{&APIError{StatusCode: 401, Type: "authentication_error", Message: "invalid x-api-key"}}}
	chat = newRetryTestChat(t, fake)
	chat.Env.Set("default-model", eval.String("fake-model"))
	_, err = chat.Message(context.Background(), "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "rejected the API key — run `configure`") {
		t.Fatalf("expected bad-key hint, got %v", err)
	}
	if !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("underlying error must survive the wrap, got %v", err)
	}

	fake = &flakyModel{errs: []error{errors.New("authentication_error: invalid x-api-key")}}
	chat = newRetryTestChat(t, fake)
	chat.script = true
	_, err = chat.Message(context.Background(), "hi", nil, "")
	if err == nil || strings.Contains(err.Error(), "configure") {
		t.Fatalf("script chat must surface the raw error, got %v", err)
	}
}
