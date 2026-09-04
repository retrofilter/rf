package models

// Message is one step of an in-session chat transcript: user text, an
// assistant text reply, or an executed scheme tool call.
type Message struct {
	Role    string // "user", "assistant", or "system"
	Type    string // "text" or "tool"
	Content string
	// Tool-specific fields (only used when Type == "tool")
	FunctionName string
	Code         string
	Result       string
	ErrorMessage string
	// ToolCallID threads provider tool_use ids through a single turn's
	// agentic loop.
	ToolCallID string
}

const (
	MessageRoleUser      = "user"
	MessageRoleAssistant = "assistant"
	MessageRoleSystem    = "system"

	MessageTypeText = "text"
	MessageTypeTool = "tool"
)
