package llm

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/logger"
	"github.com/retrofilter/rf/models"
)

//go:embed prompt.txt
var promptFS embed.FS

const (
	// DefaultMaxTokens caps output tokens per response in the chat loop.
	// Sized for Claude models (Sonnet 4.6 allows 64k out); default-model's
	// :max-tokens overrides it (config-as-data, like default-graph).
	DefaultMaxTokens = 32000

	plainMaxTokens = 8192

	maxToolIterations = 24

	toolResultCap = 50 * 1024
)

// Config is the resolved model configuration for a Chat — the Go shape
// of the `default-model` prelude dict (see resolveConfig): identity and
// endpoint, plus the capability knobs the request builder reads.
type Config struct {
	Provider string // "anthropic", "ollama", or any anthropic-compatible name
	Model    string // model id (:id)
	BaseURL  string // Messages-API root (:base-url; known providers have defaults)
	APIKey   string // :api-key, else $RF_<PROVIDER>_API_KEY; sent as x-api-key when set

	Thinking  int    // 0 off; ThinkingAdaptive for :thinking #t; >0 a token budget
	Effort    string // output_config effort (:effort); "" leaves the API default
	MaxTokens int    // per-response output cap (:max-tokens); 0 → DefaultMaxTokens
	Context   int    // context window (:context); 0 → table / Ollama probe
	Caching   bool   // attach cache_control breakpoints (:caching)
}

// ThinkingAdaptive is Config.Thinking's spelling of `:thinking #t`:
// adaptive thinking with summarized display.
const ThinkingAdaptive = -1

func normalizeConfig(cfg Config) Config {
	if cfg.Provider == "" {
		cfg.Provider = models.ProviderForModel(cfg.Model)
		if cfg.Provider == "" {
			cfg.Provider = models.ProviderOllama
		}
	}
	if cfg.Model == "" {
		cfg.Model = models.DefaultModelForProvider(cfg.Provider)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = models.DefaultBaseURL(cfg.Provider)
	}
	if cfg.Provider == models.ProviderAnthropic {
		cfg.Caching = true
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv(models.APIKeyEnv(cfg.Provider))
	}
	return cfg
}

// Event identifies a step of the agentic loop as delivered to OnEvent. Retry
// and notice events are display-only: never part of the transcript, never
// sent to the model.
type Event int

const (
	EventText Event = iota
	EventToolCall
	EventToolResult
	EventSteer
	EventRetry
	EventNotice

	// EventThinking carries a completed (summarized) thinking block.
	// Display-only, like retry and notice: never part of the transcript,
	// never persisted — the in-flight conversation keeps the real blocks.
	EventThinking
)

type Chat struct {
	model      Model
	base       Config
	config     Config
	configErr  error
	modelUnset bool
	script     bool
	ollamaCtx  map[string]int
	Eval       *eval.Evaluator
	Env        *eval.Environment
	tools      []Tool

	// OnEvent, when set, receives each step of the agentic loop as it happens —
	// EventToolCall fires before the code runs, EventToolResult after (same
	// *Message, now with Result/ErrorMessage), EventText for assistant text.
	OnEvent func(Event, *models.Message)

	// OnDelta, when set, receives assistant text and thinking verbatim as they
	// stream.
	OnDelta func(DeltaKind, string)

	// Approve, when set, gates destructive builtins (rm, mv, sh) for the
	// duration of each tool call the model makes. Code the user types
	// directly is not affected.
	Approve eval.ApprovalFunc

	grantResolved bool

	steerMu sync.Mutex
	steers  []string

	usageMu       sync.Mutex
	usage         UsageTotals
	contextTokens int
}

// Steer queues text to inject as a user message before the next agentic-loop
// iteration of the running turn. Safe to call from any goroutine.
func (c *Chat) Steer(text string) {
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	c.steers = append(c.steers, text)
}

// TakeSteers drains the steering queue. Message consumes it each iteration;
// callers use it after a turn ends to recover lines that arrived too late.
func (c *Chat) TakeSteers() []string {
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	steers := c.steers
	c.steers = nil
	return steers
}

func (c *Chat) hasSteers() bool {
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	return len(c.steers) > 0
}

func (c *Chat) emit(event Event, msg *models.Message) {
	if c.OnEvent != nil {
		c.OnEvent(event, msg)
	}
}

type PromptOptions struct {
	Graph string
}

var evalSchemeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"code": {
			"type": "string",
			"description": "Scheme code to evaluate."
		}
	},
	"required": ["code"]
}`)

// NewChat creates a Chat from the defaults alone (a local Ollama, the
// provider's credential read from the environment); the prelude's
// default-model dict overlays it per request once it is defined.
func NewChat(gs *core.GraphStore) (*Chat, error) {
	return NewChatWithConfig(gs, Config{})
}

// NewChatWithConfig creates a Chat for the given provider configuration.
// Both providers speak the Anthropic Messages API: Anthropic natively,
// Ollama through its compatibility endpoint.
func NewChatWithConfig(gs *core.GraphStore, cfg Config) (*Chat, error) {
	return newChat(gs, cfg, NewClientForConfig(cfg)), nil
}

// NewChatForScript creates a database-free Chat for `rf script.scm`: no graph
// store or sessions, but the function-shaped LLM builtins (llm, llm-map,
// classify, rerank) work.
func NewChatForScript(cfg Config) *Chat {
	c := newChat(nil, cfg, NewClientForConfig(cfg))
	c.script = true
	return c
}

func newChat(gs *core.GraphStore, cfg Config, model Model) *Chat {
	var evalr *eval.Evaluator
	if gs == nil {
		evalr = eval.NewEvaluator()
	} else {
		evalr = eval.NewEvaluatorWithEnvironment(gs.DB(), gs)
	}

	c := &Chat{
		model:  model,
		base:   cfg,
		config: normalizeConfig(cfg),
		Eval:   evalr,
		Env:    evalr.GlobalEnv(),
		tools: append([]Tool{{
			Name:        "scheme",
			Description: "Evaluate Scheme code using the database context and return the result as a string.",
			InputSchema: evalSchemeSchema,
		}}, fileTools()...),
	}
	c.registerLLMBuiltin()
	c.registerMapBuiltins()
	c.registerRerankBuiltin()
	c.registerUsageBuiltin()
	return c
}

func (c *Chat) Model() Model {
	return c.model
}

func (c *Chat) Config() Config {
	return c.resolveConfig()
}

func (c *Chat) resolveConfig() Config {
	if c.Env == nil {
		// A bare Chat (tests) has no environment to consult.
		return c.config
	}
	cfg := c.base
	var caching *bool
	c.configErr = nil
	bound := false
	if v, err := c.Env.Lookup("default-model"); err == nil {
		bound = true
		caching, c.configErr = overlayModelDict(&cfg, v)
	}
	cfg = normalizeConfig(cfg)
	if caching != nil {
		cfg.Caching = *caching
	}
	c.modelUnset = !bound
	if c.configErr == nil {
		switch {
		case cfg.BaseURL == "":
			c.configErr = fmt.Errorf("default-model: provider %q has no builtin endpoint — set :base-url", cfg.Provider)
		case cfg.Model == "":
			c.configErr = fmt.Errorf("default-model: provider %q has no default model — set :id", cfg.Provider)
		}
	}
	if cfg != c.config {
		if cfg.BaseURL != c.config.BaseURL || cfg.APIKey != c.config.APIKey {
			c.model = NewClientForConfig(cfg)
		}
		c.config = cfg
	}
	return cfg
}

func overlayModelDict(cfg *Config, v eval.Value) (caching *bool, err error) {
	switch d := v.(type) {
	case eval.String:
		cfg.Model = string(d)
		return nil, nil
	case eval.Dictionary:
		if p, ok, e := dictString(d, "provider"); e != nil {
			return nil, e
		} else if ok && p != cfg.Provider {
			cfg.Provider, cfg.Model, cfg.BaseURL, cfg.APIKey = p, "", "", ""
		}
		if s, ok, e := dictString(d, "id"); e != nil {
			return nil, e
		} else if ok {
			cfg.Model = s
		}
		if s, ok, e := dictString(d, "base-url"); e != nil {
			return nil, e
		} else if ok {
			cfg.BaseURL = s
		}
		if k, ok := d["api-key"]; ok {
			switch key := k.(type) {
			case eval.String:
				cfg.APIKey = string(key)
			case bool:
				if key {
					return nil, fmt.Errorf("default-model: :api-key must be a string — usually (env \"RF_<PROVIDER>_API_KEY\")")
				}
			default:
				return nil, fmt.Errorf("default-model: :api-key must be a string — usually (env \"RF_<PROVIDER>_API_KEY\")")
			}
		}
		if s, ok, e := dictString(d, "effort"); e != nil {
			return nil, e
		} else if ok {
			if !effortLevels[s] {
				return nil, fmt.Errorf("default-model: :effort must be low, medium, high, xhigh, or max, not %q", s)
			}
			cfg.Effort = s
		}
		if n, ok, e := dictInt(d, "max-tokens"); e != nil {
			return nil, e
		} else if ok {
			cfg.MaxTokens = n
		}
		if n, ok, e := dictInt(d, "context"); e != nil {
			return nil, e
		} else if ok {
			cfg.Context = n
		}
		if t, ok := d["thinking"]; ok {
			switch b := t.(type) {
			case bool:
				cfg.Thinking = 0
				if b {
					cfg.Thinking = ThinkingAdaptive
				}
			case eval.Integer:
				cfg.Thinking = int(b)
			case eval.Number:
				cfg.Thinking = int(b)
			default:
				return nil, fmt.Errorf("default-model: :thinking must be #t, #f, or a token budget")
			}
		}
		if v, ok := d["caching"]; ok {
			b, isBool := v.(bool)
			if !isBool {
				return nil, fmt.Errorf("default-model: :caching must be #t or #f")
			}
			caching = &b
		}
		for key := range d {
			switch key {
			case "provider", "id", "base-url", "api-key", "thinking", "effort", "max-tokens", "context", "caching":
			default:
				return caching, fmt.Errorf("default-model: unknown key :%s (keys: :provider :id :base-url :api-key :thinking :effort :max-tokens :context :caching)", key)
			}
		}
		return caching, nil
	}
	return nil, fmt.Errorf("default-model must be a dict or a model-id string")
}

func dictString(d eval.Dictionary, key string) (s string, ok bool, err error) {
	v, present := d[key]
	if !present {
		return "", false, nil
	}
	str, isStr := v.(eval.String)
	if !isStr {
		return "", false, fmt.Errorf("default-model: :%s must be a string", key)
	}
	return string(str), true, nil
}

func dictInt(d eval.Dictionary, key string) (n int, ok bool, err error) {
	v, present := d[key]
	if !present {
		return 0, false, nil
	}
	switch num := v.(type) {
	case eval.Integer:
		n = int(num)
	case eval.Number:
		n = int(num)
	default:
		return 0, false, fmt.Errorf("default-model: :%s must be a number", key)
	}
	if n <= 0 {
		return 0, false, fmt.Errorf("default-model: :%s must be positive", key)
	}
	return n, true, nil
}

func (c *Chat) Tools() []Tool {
	return c.tools
}

func (c *Chat) getSystemPrompt() (string, error) {
	data, err := promptFS.ReadFile("prompt.txt")
	if err != nil {
		return "", fmt.Errorf("failed to read prompt file: %w", err)
	}
	return string(data), nil
}

// SystemPrompt is the embedded base system prompt (prompt.txt) before the
// per-turn AGENTS.md context.
func SystemPrompt() string {
	data, _ := promptFS.ReadFile("prompt.txt")
	return string(data)
}

func appendBlocks(messages []Message, role string, blocks ...ContentBlock) []Message {
	if n := len(messages); n > 0 && messages[n-1].Role == role {
		messages[n-1].Content = append(messages[n-1].Content, blocks...)
		return messages
	}
	return append(messages, Message{Role: role, Content: blocks})
}

func appendText(messages []Message, role, text string) []Message {
	return appendBlocks(messages, role, ContentBlock{Type: "text", Text: text})
}

func (c *Chat) maxTokens() int {
	if c.config.MaxTokens > 0 {
		return c.config.MaxTokens
	}
	return DefaultMaxTokens
}

var effortLevels = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

func (c *Chat) thinkingConfig() *Thinking {
	switch t := c.config.Thinking; {
	case t == ThinkingAdaptive:
		return &Thinking{Type: "adaptive", Display: "summarized"}
	case t > 0:
		return &Thinking{Type: "enabled", BudgetTokens: t}
	}
	return nil
}

func (c *Chat) outputConfig() *OutputConfig {
	if c.config.Effort == "" {
		return nil
	}
	return &OutputConfig{Effort: c.config.Effort}
}

// Message sends a user message to the LLM and runs the agentic loop: any
// scheme tool calls are executed and their results fed back to the model
// until it responds with text (or the iteration cap is reached).
func (c *Chat) Message(ctx context.Context, message string, history []*models.Message, additionalPrompt string) ([]*models.Message, error) {
	c.resolveConfig()
	c.grantWorkingDir()
	systemPrompt, err := c.getSystemPrompt()
	if err != nil {
		return nil, fmt.Errorf("failed to get system prompt: %w", err)
	}

	if cwd, err := os.Getwd(); err == nil {
		if agents := agentsContext(cwd); agents != "" {
			systemPrompt += "\n\n" + agents
		}
	}

	if additionalPrompt != "" {
		systemPrompt += "\n\n" + additionalPrompt
	}

	logger.Debug().Str("systemPrompt", systemPrompt).Msg("Prompt")

	var messages []Message
	for i, msg := range history {
		switch msg.Role {
		case models.MessageRoleUser:
			if msg.Type == models.MessageTypeText {
				messages = appendText(messages, "user", msg.Content)
			}
		case models.MessageRoleAssistant:
			switch msg.Type {
			case models.MessageTypeText:
				messages = appendText(messages, "assistant", msg.Content)
			case models.MessageTypeTool:
				id := msg.ToolCallID
				if id == "" {
					id = fmt.Sprintf("toolu_replay_%d", i)
				}
				name := msg.FunctionName
				if name == "" {
					name = "scheme"
				}
				var input json.RawMessage
				if isFileTool(name) && json.Valid([]byte(msg.Code)) {
					input = json.RawMessage(msg.Code)
				} else {
					input, _ = json.Marshal(map[string]string{"code": msg.Code})
				}
				resultText := msg.Result
				isError := false
				if msg.ErrorMessage != "" {
					resultText = "Error: " + msg.ErrorMessage
					isError = true
				}
				messages = appendBlocks(messages, "assistant", ContentBlock{
					Type: "tool_use", ID: id, Name: name, Input: input,
				})
				messages = appendBlocks(messages, "user", ContentBlock{
					Type: "tool_result", ToolUseID: id, Content: resultText, IsError: isError,
				})
			}
		}
	}
	messages = appendText(messages, "user", message)

	var results []*models.Message

	budget := maxToolIterations
	for {
		// Inject any steering lines typed while the turn was running.
		if steers := c.TakeSteers(); len(steers) > 0 {
			for _, steer := range steers {
				messages = appendText(messages, "user", steer)
				msg := &models.Message{
					Role:    models.MessageRoleUser,
					Type:    models.MessageTypeText,
					Content: steer,
				}
				results = append(results, msg)
				c.emit(EventSteer, msg)
			}
			budget = maxToolIterations
		}
		if budget == 0 {
			break
		}
		budget--

		if c.config.Caching {
			markCacheBreakpoint(messages)
		}
		req := &Request{
			Model:        c.config.Model,
			MaxTokens:    c.maxTokens(),
			System:       systemPrompt,
			Messages:     messages,
			Tools:        c.tools,
			Thinking:     c.thinkingConfig(),
			OutputConfig: c.outputConfig(),
		}
		resp, err := c.messages(ctx, req, c.OnDelta, true)
		if err != nil {
			return results, fmt.Errorf("failed to generate content: %w", err)
		}
		c.recordUsage(req, resp, true)

		messages = append(messages, Message{Role: "assistant", Content: resp.Content})

		var toolResults []ContentBlock
		for _, block := range resp.Content {
			switch block.Type {
			case "thinking":
				if block.Thinking == nil || strings.TrimSpace(*block.Thinking) == "" {
					continue
				}
				c.emit(EventThinking, &models.Message{
					Role:    models.MessageRoleAssistant,
					Type:    models.MessageTypeText,
					Content: *block.Thinking,
				})
			case "text":
				if block.Text == "" {
					continue
				}
				logger.Debug().Str("content", block.Text).Msg("Received text content")
				msg := &models.Message{
					Role:    models.MessageRoleAssistant,
					Type:    models.MessageTypeText,
					Content: block.Text,
				}
				results = append(results, msg)
				c.emit(EventText, msg)
			case "tool_use":
				logger.Debug().Str("function", block.Name).RawJSON("input", block.Input).Msg("Received tool call")
				msg := toolMessageFromCall(block.ID, block.Name, block.Input)
				if msg == nil {
					continue
				}
				results = append(results, msg)
				c.emit(EventToolCall, msg)
				c.executeToolCall(msg)
				c.emit(EventToolResult, msg)

				resultText := msg.Result
				isError := false
				if msg.ErrorMessage != "" {
					resultText = "Error: " + msg.ErrorMessage
					isError = true
				}
				toolResults = append(toolResults, ContentBlock{
					Type:      "tool_result",
					ToolUseID: block.ID,
					Content:   resultText,
					IsError:   isError,
				})
			}
		}

		if len(toolResults) > 0 {
			messages = append(messages, Message{Role: "user", Content: toolResults})
			continue
		}
		if !c.hasSteers() {
			break
		}
	}

	var finalText string
	for i := len(results) - 1; i >= 0; i-- {
		if results[i].Role == models.MessageRoleAssistant && results[i].Type == models.MessageTypeText {
			finalText = results[i].Content
			break
		}
	}
	c.runAfterTurnHook(finalText)

	return results, nil
}

func markCacheBreakpoint(messages []Message) {
	for mi := range messages {
		for bi := range messages[mi].Content {
			messages[mi].Content[bi].CacheControl = nil
		}
	}
	if len(messages) == 0 {
		return
	}
	last := &messages[len(messages)-1]
	if len(last.Content) == 0 {
		return
	}
	last.Content[len(last.Content)-1].CacheControl = &CacheControl{Type: "ephemeral"}
}

func toolMessageFromCall(id, name string, input json.RawMessage) *models.Message {
	code := string(input)
	if !isFileTool(name) {
		var args struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			logger.Error().Err(err).Msg("Failed to parse tool call arguments")
			return nil
		}
		code = args.Code
	}
	return &models.Message{
		Role:         models.MessageRoleAssistant,
		Type:         models.MessageTypeTool,
		Content:      code,
		FunctionName: name,
		Code:         code,
		ToolCallID:   id,
	}
}

func (c *Chat) grantWorkingDir() {
	if c.grantResolved {
		return
	}
	c.grantResolved = true
	v, err := c.Env.Lookup("agent-allow-working-dir")
	if err != nil {
		return
	}
	var level string
	switch b := v.(type) {
	case eval.Keyword:
		level = string(b)
	case eval.Symbol:
		level = string(b)
	case eval.String:
		level = string(b)
	default:
		return
	}
	if level != "read" && level != "write" {
		return
	}
	if cwd, err := os.Getwd(); err == nil {
		c.Eval.AllowDir(cwd, level == "write")
	}
}

func (c *Chat) executeToolCall(msg *models.Message) {
	if !c.runBeforeToolHook(msg) {
		c.runAfterToolHook(msg)
		return
	}
	defer c.runAfterToolHook(msg)
	if isFileTool(msg.FunctionName) {
		c.execFileTool(msg)
		return
	}
	if c.Approve != nil {
		prev := c.Eval.Approver()
		c.Eval.SetApprover(c.Approve)
		defer c.Eval.SetApprover(prev)
	}
	prevCaller := c.Eval.Caller()
	c.Eval.SetCaller(eval.CallerAssistant)
	defer c.Eval.SetCaller(prevCaller)

	expr, err := eval.ParseAll(msg.Code)
	if err != nil {
		msg.ErrorMessage = fmt.Sprintf("parse error: %v", err)
		return
	}

	result, err := c.Eval.EvalAll(expr, c.Env)
	if err != nil {
		msg.ErrorMessage = fmt.Sprintf("evaluation error: %v", err)
		return
	}

	rendered, err := eval.PrintValueCapped(result, toolResultCap)
	if err != nil {
		msg.ErrorMessage = fmt.Sprintf("evaluation error: %v", err)
		return
	}
	msg.Result = rendered
}

// ExecuteToolCalls executes any tool calls that have not been executed yet.
// Message already executes tool calls as part of its agentic loop; this
// remains for callers holding tool messages from other sources.
func (c *Chat) ExecuteToolCalls(ctx context.Context, messages []*models.Message) error {
	for _, msg := range messages {
		if msg.Type == models.MessageTypeTool && msg.Result == "" && msg.ErrorMessage == "" {
			c.executeToolCall(msg)
		}
	}
	return nil
}
