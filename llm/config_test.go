package llm

import (
	"strings"
	"testing"

	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/models"
)

func TestResolveConfigDict(t *testing.T) {
	base := Config{Provider: models.ProviderOllama, Model: "base-model"}
	chat := NewChatForScript(base)

	if cfg := chat.Config(); cfg.Provider != models.ProviderOllama || cfg.Model != "base-model" || cfg.BaseURL != models.DefaultOllamaURL {
		t.Fatalf("no binding: want the normalized base config, got %+v", cfg)
	}

	chat.Env.Set("default-model", eval.Dictionary{"provider": eval.String(models.ProviderAnthropic)})
	cfg := chat.Config()
	if cfg.Provider != models.ProviderAnthropic || cfg.Model != models.DefaultAnthropicModel || !cfg.Caching {
		t.Fatalf("provider dict: want anthropic + its defaults, got %+v", cfg)
	}
	client, ok := chat.Model().(*Client)
	if !ok || client.BaseURL != models.DefaultAnthropicURL {
		t.Fatalf("provider change should swap the endpoint client, got %#v", chat.Model())
	}

	// A bare string is shorthand for {:id "..."}.
	chat.Env.Set("default-model", eval.String("granite-test"))
	cfg = chat.Config()
	if cfg.Provider != models.ProviderOllama || cfg.Model != "granite-test" {
		t.Fatalf("string binding: want the base provider + that id, got %+v", cfg)
	}

	chat.Env.Set("default-model", eval.Dictionary{
		"provider":   eval.String("deepseek"),
		"id":         eval.String("deepseek-chat"),
		"base-url":   eval.String("http://elsewhere:1234"),
		"thinking":   eval.Integer(2048),
		"effort":     eval.String("medium"),
		"max-tokens": eval.Integer(9000),
		"context":    eval.Integer(48000),
		"caching":    true,
	})
	cfg = chat.Config()
	if cfg.Provider != "deepseek" || cfg.Model != "deepseek-chat" || cfg.BaseURL != "http://elsewhere:1234" {
		t.Fatalf("custom provider dict: got %+v", cfg)
	}
	if cfg.Thinking != 2048 || cfg.Effort != "medium" || cfg.MaxTokens != 9000 || cfg.Context != 48000 || !cfg.Caching {
		t.Fatalf("capability knobs: got %+v", cfg)
	}
	if client, ok = chat.Model().(*Client); !ok || client.BaseURL != "http://elsewhere:1234" {
		t.Fatalf(":base-url should reach the client, got %#v", chat.Model())
	}
	if th := chat.thinkingConfig(); th == nil || th.Type != "enabled" || th.BudgetTokens != 2048 {
		t.Fatalf("thinking budget: got %+v", th)
	}
	if oc := chat.outputConfig(); oc == nil || oc.Effort != "medium" {
		t.Fatalf("effort: got %+v", oc)
	}
	if got := chat.maxTokens(); got != 9000 {
		t.Fatalf("max-tokens: got %d", got)
	}
	if got := chat.contextWindow(); got != 48000 {
		t.Fatalf("context window: got %d", got)
	}

	// :thinking #t is adaptive; absent is off.
	chat.Env.Set("default-model", eval.Dictionary{"thinking": true})
	chat.Config()
	if th := chat.thinkingConfig(); th == nil || th.Type != "adaptive" {
		t.Fatalf("adaptive thinking: got %+v", th)
	}
	chat.Env.Set("default-model", eval.Dictionary{})
	chat.Config()
	if th := chat.thinkingConfig(); th != nil {
		t.Fatalf("thinking must default off, got %+v", th)
	}
}

func TestResolveConfigErrors(t *testing.T) {
	chat := NewChatForScript(Config{Provider: models.ProviderOllama, Model: "m"})
	cases := []struct {
		value eval.Value
		want  string
	}{
		{eval.Dictionary{"ctx": eval.Integer(1)}, "unknown key :ctx"},
		{eval.Dictionary{"provider": eval.String("deepseek")}, "set :base-url"},
		{eval.Dictionary{"provider": eval.String("deepseek"), "base-url": eval.String("http://x")}, "set :id"},
		{eval.Dictionary{"effort": eval.String("extreme")}, ":effort must be"},
		{eval.Dictionary{"api-key": eval.Integer(1)}, ":api-key must be a string"},
		{eval.Dictionary{"caching": eval.String("yes")}, ":caching must be"},
		{eval.Integer(3), "must be a dict or a model-id string"},
	}
	for _, tc := range cases {
		chat.Env.Set("default-model", tc.value)
		chat.Config()
		if chat.configErr == nil || !strings.Contains(chat.configErr.Error(), tc.want) {
			t.Errorf("default-model %v: want error containing %q, got %v", tc.value, tc.want, chat.configErr)
		}
	}
	// A valid binding clears the error.
	chat.Env.Set("default-model", eval.String("m2"))
	chat.Config()
	if chat.configErr != nil {
		t.Fatalf("valid binding should clear configErr, got %v", chat.configErr)
	}
}

func TestNormalizeConfigCredentials(t *testing.T) {
	t.Setenv("RF_ANTHROPIC_API_KEY", "sk-anthropic")
	t.Setenv("RF_OLLAMA_API_KEY", "")

	if cfg := normalizeConfig(Config{}); cfg.Provider != models.ProviderOllama || cfg.APIKey != "" {
		t.Fatalf("an anthropic key must not select anthropic: %+v", cfg)
	}
	cfg := normalizeConfig(Config{Model: "claude-sonnet-4-6"})
	if cfg.Provider != models.ProviderAnthropic || cfg.APIKey != "sk-anthropic" || cfg.BaseURL != models.DefaultAnthropicURL {
		t.Fatalf("claude id should mean anthropic + its key: %+v", cfg)
	}
	if cfg := normalizeConfig(Config{Provider: models.ProviderAnthropic, APIKey: "sk-explicit"}); cfg.APIKey != "sk-explicit" {
		t.Fatalf("explicit key overridden: %+v", cfg)
	}

	chat := NewChatForScript(Config{})
	chat.Env.Set("default-model", eval.Dictionary{"provider": eval.String(models.ProviderAnthropic), "api-key": eval.String("sk-dict")})
	if cfg := chat.Config(); cfg.APIKey != "sk-dict" {
		t.Fatalf(":api-key string should win: %+v", cfg)
	}
	chat.Env.Set("default-model", eval.Dictionary{"provider": eval.String(models.ProviderAnthropic), "api-key": false})
	if cfg := chat.Config(); cfg.APIKey != "sk-anthropic" {
		t.Fatalf(":api-key #f should fall to the derived env var: %+v", cfg)
	}
	chat.Env.Set("default-model", eval.String("claude-sonnet-4-6"))
	if cfg := chat.Config(); cfg.Provider != models.ProviderAnthropic || cfg.APIKey != "sk-anthropic" {
		t.Fatalf("bare claude id binding: %+v", cfg)
	}
	t.Setenv("RF_ANTHROPIC_API_KEY", "sk-rotated")
	if cfg := chat.Config(); cfg.APIKey != "sk-rotated" {
		t.Fatalf("key should be read live: %+v", cfg)
	}
	t.Setenv("RF_ANTHROPIC_API_KEY", "")
	if cfg := chat.Config(); cfg.APIKey != "" {
		t.Fatalf("a removed key should stop being sent: %+v", cfg)
	}
}
