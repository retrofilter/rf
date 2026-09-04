package models

import "strings"

// Provider names and model defaults.
const (
	ProviderOllama    = "ollama"
	ProviderAnthropic = "anthropic"

	DefaultOllamaURL      = "http://localhost:11434"
	DefaultAnthropicURL   = "https://api.anthropic.com"
	DefaultModel          = "granite4:3b"
	DefaultAnthropicModel = "claude-sonnet-4-6"
)

// DefaultModelForProvider returns the default model id for a known
// provider; "" for providers rf has no defaults for.
func DefaultModelForProvider(provider string) string {
	switch provider {
	case ProviderAnthropic:
		return DefaultAnthropicModel
	case ProviderOllama:
		return DefaultModel
	}
	return ""
}

// DefaultBaseURL returns the builtin Messages-API endpoint root for a
// known provider; "" for providers the config must locate via :base-url.
func DefaultBaseURL(provider string) string {
	switch provider {
	case ProviderAnthropic:
		return DefaultAnthropicURL
	case ProviderOllama:
		return DefaultOllamaURL
	}
	return ""
}

// APIKeyEnv derives the credential env var for a provider name: RF_ prefix,
// uppercased, non-alphanumerics folded to '_', suffixed _API_KEY —
// "anthropic" → RF_ANTHROPIC_API_KEY, "deepseek" → RF_DEEPSEEK_API_KEY.
func APIKeyEnv(provider string) string {
	var b strings.Builder
	b.WriteString("RF_")
	for _, r := range strings.ToUpper(provider) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String() + "_API_KEY"
}

// ProviderForModel infers a provider from a model id alone, for a default-
// model binding that names only the id: Claude ids belong to Anthropic;
// anything else falls to the caller's default.
func ProviderForModel(id string) string {
	if strings.HasPrefix(id, "claude-") {
		return ProviderAnthropic
	}
	return ""
}
