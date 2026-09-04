package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const anthropicVersion = "2023-06-01"

// DeltaKind tags a streamed increment delivered to onDelta: assistant text
// or (summarized) thinking.
type DeltaKind int

const (
	DeltaText DeltaKind = iota
	DeltaThinking
)

// Model is the provider interface: one Messages API call. onDelta, when
// non-nil, receives assistant text and thinking as they stream; the returned
// Response is always the complete accumulated message.
type Model interface {
	Messages(ctx context.Context, req *Request, onDelta func(DeltaKind, string)) (*Response, error)
}

// Request is a Messages API request body. Stream is set by the client.
type Request struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	System       string        `json:"system,omitempty"`
	Messages     []Message     `json:"messages"`
	Tools        []Tool        `json:"tools,omitempty"`
	Thinking     *Thinking     `json:"thinking,omitempty"`
	OutputConfig *OutputConfig `json:"output_config,omitempty"`
	Stream       bool          `json:"stream,omitempty"`
}

// Thinking configures reasoning: adaptive (the model decides when and how
// much to think; Claude 4.6+) or enabled with a token budget (older models).
type Thinking struct {
	Type         string `json:"type"` // "adaptive" or "enabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"` // "summarized" or "omitted"
}

// OutputConfig carries output-level controls; Effort ("low", "medium",
// "high", "xhigh", "max") is soft guidance on how much work — thinking
// included — the model puts into a response.
type OutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// Message is one conversation turn. Content blocks carry text, tool_use
// (assistant) and tool_result (user) parts.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is a tagged union over the Messages API block types. Only the
// fields for the given Type are populated.
type ContentBlock struct {
	Type string `json:"type"` // "text", "tool_use", "tool_result", "thinking", "redacted_thinking"

	// text
	Text string `json:"text,omitempty"`

	// thinking.
	Thinking  *string `json:"thinking,omitempty"`
	Signature string  `json:"signature,omitempty"`

	// redacted_thinking: opaque encrypted reasoning, passed back verbatim.
	Data string `json:"data,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// CacheControl marks a prompt-cache breakpoint (request side only).
	// Anthropic caches the prefix up to the marked block; Ollama ignores it.
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// CacheControl is the Messages API cache_control marker.
type CacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// Tool is a function definition the model may call.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Usage reports token counts for one API call. InputTokens is the uncached
// remainder only: full prompt size = input + cache creation + cache read.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// Response is a complete assistant message.
type Response struct {
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// TextMessage builds a single-text-block message.
func TextMessage(role, text string) Message {
	return Message{Role: role, Content: []ContentBlock{{Type: "text", Text: text}}}
}

// Client talks the Messages API to one provider endpoint.
type Client struct {
	BaseURL string // e.g. https://api.anthropic.com or http://localhost:11434
	APIKey  string // sent as x-api-key when set; Ollama ignores it
	HTTP    *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// APIError is a provider failure carrying StatusCode and Type structurally
// for retry classification; its rendered text keeps the "type: message" and
// "http NNN: body" shapes the overflow classifier and logs read.
type APIError struct {
	StatusCode int    // HTTP status; 0 for a mid-stream SSE error event
	Type       string // error envelope type (e.g. "overloaded_error"), may be empty
	Message    string
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return e.Type + ": " + e.Message
	}
	return fmt.Sprintf("http %d: %s", e.StatusCode, e.Message)
}

var errTruncatedStream = errors.New("connection closed before message_stop (truncated response)")

// Messages performs one API call. With onDelta set the request streams and
// each text or thinking delta is delivered as it arrives; either way the full
// accumulated response is returned.
func (c *Client) Messages(ctx context.Context, req *Request, onDelta func(DeltaKind, string)) (*Response, error) {
	body := *req
	body.Stream = onDelta != nil

	payload, err := json.Marshal(&body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if c.APIKey != "" {
		httpReq.Header.Set("x-api-key", c.APIKey)
	}

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		var apiErr apiError
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, &APIError{StatusCode: resp.StatusCode, Type: apiErr.Error.Type, Message: apiErr.Error.Message}
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}

	if body.Stream {
		return readStream(resp.Body, onDelta)
	}

	var out Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Usage Usage `json:"usage"`
	} `json:"message"`
	ContentBlock ContentBlock `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage Usage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func readStream(r io.Reader, onDelta func(DeltaKind, string)) (*Response, error) {
	out := &Response{}
	inputs := map[int]*strings.Builder{}
	stopped := false

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // event: lines, comments, blank separators
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil, fmt.Errorf("decode stream event: %w", err)
		}

		switch ev.Type {
		case "message_start":
			out.Usage.InputTokens = ev.Message.Usage.InputTokens
			out.Usage.CacheCreationInputTokens = ev.Message.Usage.CacheCreationInputTokens
			out.Usage.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
		case "content_block_start":
			out.Content = append(out.Content, ev.ContentBlock)
		case "content_block_delta":
			i := ev.Index
			if i < 0 || i >= len(out.Content) {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				out.Content[i].Text += ev.Delta.Text
				if onDelta != nil && ev.Delta.Text != "" {
					onDelta(DeltaText, ev.Delta.Text)
				}
			case "thinking_delta":
				if out.Content[i].Thinking == nil {
					out.Content[i].Thinking = new(string)
				}
				*out.Content[i].Thinking += ev.Delta.Thinking
				if onDelta != nil && ev.Delta.Thinking != "" {
					onDelta(DeltaThinking, ev.Delta.Thinking)
				}
			case "signature_delta":
				out.Content[i].Signature += ev.Delta.Signature
			case "input_json_delta":
				sb := inputs[i]
				if sb == nil {
					sb = &strings.Builder{}
					inputs[i] = sb
				}
				sb.WriteString(ev.Delta.PartialJSON)
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				out.StopReason = ev.Delta.StopReason
			}
			if ev.Usage.OutputTokens != 0 {
				out.Usage.OutputTokens = ev.Usage.OutputTokens
			}
		case "message_stop":
			stopped = true
		case "error":
			return nil, &APIError{Type: ev.Error.Type, Message: ev.Error.Message}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	if !stopped {
		return nil, fmt.Errorf("read stream: %w", errTruncatedStream)
	}

	for i, sb := range inputs {
		if sb.Len() > 0 {
			out.Content[i].Input = json.RawMessage(sb.String())
		}
	}
	return out, nil
}

// NewClientForConfig builds the endpoint client for a Config from the
// resolved :base-url and the provider's derived credential; that (base URL,
// key) pair is the whole provider seam.
func NewClientForConfig(cfg Config) *Client {
	cfg = normalizeConfig(cfg)
	return &Client{
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKey,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}
