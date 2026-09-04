package llm

import (
	"encoding/json"
	"fmt"

	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/logger"
	"github.com/retrofilter/rf/models"
)

const (
	hookBeforeTool = "before-tool-hook"
	hookAfterTool  = "after-tool-hook"
	hookAfterTurn  = "after-turn-hook"
)

func (c *Chat) lookupHook(name string) eval.Value {
	v, err := c.Env.Lookup(name)
	if err != nil {
		return nil
	}
	if _, ok := v.(eval.BuiltinFunc); ok || eval.IsUserFunction(v) {
		return v
	}
	return nil
}

func (c *Chat) callHook(name string, payload eval.Dictionary) (eval.Value, bool) {
	fn := c.lookupHook(name)
	if fn == nil {
		return nil, false
	}
	prevApprover := c.Eval.Approver()
	if c.Approve != nil {
		c.Eval.SetApprover(c.Approve)
	}
	prevCaller := c.Eval.Caller()
	c.Eval.SetCaller(eval.CallerAssistant)
	defer func() {
		c.Eval.SetApprover(prevApprover)
		c.Eval.SetCaller(prevCaller)
	}()

	result, err := c.Eval.Apply(fn, []eval.Value{payload}, c.Env)
	if err != nil {
		c.notice(fmt.Sprintf("%s error: %v", name, err))
		return nil, false
	}
	return result, true
}

func (c *Chat) notice(text string) {
	logger.Debug().Str("notice", text).Msg("Hook notice")
	c.emit(EventNotice, &models.Message{
		Role:    models.MessageRoleAssistant,
		Type:    models.MessageTypeText,
		Content: text,
	})
}

func schemeValue(v interface{}) eval.Value {
	switch t := v.(type) {
	case string:
		return eval.String(t)
	case float64:
		return eval.Number(t)
	case bool:
		return t
	case []interface{}:
		out := make([]eval.Value, len(t))
		for i, e := range t {
			out[i] = schemeValue(e)
		}
		return out
	case map[string]interface{}:
		out := eval.Dictionary{}
		for k, e := range t {
			out[k] = schemeValue(e)
		}
		return out
	default:
		return nil
	}
}

func hookToolPayload(msg *models.Message) eval.Dictionary {
	input := eval.Dictionary{}
	if isFileTool(msg.FunctionName) {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(msg.Code), &parsed); err == nil {
			for k, v := range parsed {
				input[k] = schemeValue(v)
			}
		}
	} else {
		input["code"] = eval.String(msg.Code)
	}
	return eval.Dictionary{"tool": eval.String(msg.FunctionName), "input": input}
}

func (c *Chat) runBeforeToolHook(msg *models.Message) bool {
	result, ok := c.callHook(hookBeforeTool, hookToolPayload(msg))
	if !ok {
		return true
	}
	switch v := result.(type) {
	case bool:
		if !v {
			msg.ErrorMessage = "blocked by before-tool-hook"
			return false
		}
	case eval.String:
		msg.ErrorMessage = "blocked by before-tool-hook: " + string(v)
		return false
	}
	return true
}

func (c *Chat) runAfterToolHook(msg *models.Message) {
	payload := hookToolPayload(msg)
	payload["result"] = eval.String(msg.Result)
	// Like (env "NAME"): false means "none", a string is the error.
	if msg.ErrorMessage != "" {
		payload["error"] = eval.String(msg.ErrorMessage)
	} else {
		payload["error"] = false
	}
	result, ok := c.callHook(hookAfterTool, payload)
	if !ok {
		return
	}
	if s, isStr := result.(eval.String); isStr && msg.ErrorMessage == "" {
		msg.Result = string(s)
	}
}

func (c *Chat) runAfterTurnHook(finalText string) {
	c.callHook(hookAfterTurn, eval.Dictionary{
		"text":  eval.String(finalText),
		"usage": c.usageDict(),
	})
}
