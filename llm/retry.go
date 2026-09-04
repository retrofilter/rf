package llm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/retrofilter/rf/models"
)

const maxRetries = 3

var retryBaseDelay = 2 * time.Second

var nonRetryableError = regexp.MustCompile(`(?i)` + strings.Join([]string{
	"invalid_request",
	"authentication",
	"permission",
	"not_found",
	"insufficient_quota",
	"quota exceeded",
	"billing",
	"credit balance",
}, "|"))

var retryableError = regexp.MustCompile(`(?i)` + strings.Join([]string{
	"overloaded",
	"rate.?limit",
	"too many requests",
	"service.?unavailable",
	"server.?error",
	"internal.?error",
	"api.?error",
	"connection.?(refused|reset|error|lost)",
	"broken pipe",
	"unexpected eof",
	"socket",
	"timed.?out",
	"timeout",
	"no such host",
}, "|"))

var contextOverflowError = regexp.MustCompile(`(?i)` + strings.Join([]string{
	"prompt is too long",                 // Anthropic token overflow
	"request_too_large",                  // Anthropic byte-size overflow (413)
	"prompt too long",                    // Ollama explicit overflow
	"exceeds the available context size", // llama.cpp server
	"greater than the context length",    // LM Studio
	"context.?length.?exceeded",          // generic fallback
	"context window exceeds",             // generic fallback
	"too many tokens",                    // generic fallback
	"token limit exceeded",               // generic fallback
}, "|"))

var notOverflowError = regexp.MustCompile(`(?i)rate.?limit|too many requests|throttl`)

// IsContextOverflow reports whether an error from a provider call means the
// conversation no longer fits the model's context window.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return !notOverflowError.MatchString(msg) && contextOverflowError.MatchString(msg)
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, bufio.ErrTooLong) || IsContextOverflow(err) {
		return false
	}
	if errors.Is(err, errTruncatedStream) {
		return true
	}
	msg := err.Error()
	if nonRetryableError.MatchString(msg) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 408, 429:
			return true
		}
		if apiErr.StatusCode >= 500 {
			return true // server-side, 529 overloaded included
		}
		switch apiErr.Type {
		case "overloaded_error", "api_error", "rate_limit_error", "timeout_error":
			return true // mid-stream SSE error events carry no status
		}
		if apiErr.StatusCode >= 400 {
			return false // remaining 4xx: the request itself is the problem
		}
	}
	return retryableError.MatchString(msg)
}

func (c *Chat) adviseConfigure(err error) error {
	if c.script {
		return err
	}
	if c.modelUnset {
		return fmt.Errorf("no model set — run `configure` (%w)", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
		return fmt.Errorf("%s rejected the API key — run `configure` (%w)", c.config.Provider, err)
	}
	return err
}

func (c *Chat) messages(ctx context.Context, req *Request, onDelta func(DeltaKind, string), announce bool) (*Response, error) {
	if c.configErr != nil {
		return nil, c.configErr
	}
	for attempt := 1; ; attempt++ {
		resp, err := c.model.Messages(ctx, req, onDelta)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil || attempt > maxRetries || !isRetryableError(err) {
			if ctx.Err() == nil {
				err = c.adviseConfigure(err)
			}
			return nil, err
		}
		delay := retryBaseDelay << (attempt - 1)
		if announce {
			c.emit(EventRetry, &models.Message{
				Role:    models.MessageRoleAssistant,
				Type:    models.MessageTypeText,
				Content: fmt.Sprintf("%v — retrying in %s (attempt %d/%d)", err, delay, attempt, maxRetries),
			})
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
