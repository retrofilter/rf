package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/retrofilter/rf/console"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/llm"
	"github.com/retrofilter/rf/models"
)

const (
	configBlockBegin = ";; start rf config - use `configure` to regenerate"
	configBlockEnd   = ";; end rf config"
)

func configBlock(modelForm, workingDir string, commands []string) string {
	var b strings.Builder
	b.WriteString(configBlockBegin + "\n")
	b.WriteString(modelForm)
	if workingDir != "" {
		b.WriteString("(define agent-allow-working-dir :" + workingDir + ")\n")
	}
	b.WriteString("(define agent-allow-commands '(")
	for i, cmd := range commands {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(fmt.Sprintf("%q", cmd))
	}
	b.WriteString("))\n")
	b.WriteString(configBlockEnd + "\n")
	return b.String()
}

func modelDefineForm(provider, model, baseURL string, thinking bool) string {
	var b strings.Builder
	b.WriteString("(define default-model\n")
	b.WriteString(fmt.Sprintf("  {:provider %q\n", provider))
	b.WriteString(fmt.Sprintf("   :id %q", model))
	if baseURL != "" && baseURL != models.DefaultBaseURL(provider) {
		b.WriteString(fmt.Sprintf("\n   :base-url %q", baseURL))
	}
	if provider != models.ProviderOllama {
		b.WriteString("\n   :api-key " + apiKeyForm(models.APIKeyEnv(provider)))
	}
	if thinking {
		b.WriteString("\n   :thinking #t")
	}
	b.WriteString("})\n")
	return b.String()
}

func liveModelForm(chatInst *llm.Chat) string {
	v, err := chatInst.Env.Lookup("default-model")
	if err != nil {
		return ""
	}
	switch d := v.(type) {
	case eval.String:
		return fmt.Sprintf("(define default-model %q)\n", string(d))
	case eval.Dictionary:
		var entries []string
		provider, _, _ := dictStringValue(d, "provider")
		for _, key := range []string{"provider", "id", "base-url", "api-key", "thinking", "effort", "max-tokens", "context", "caching"} {
			val, ok := d[key]
			if !ok {
				continue
			}
			if key == "api-key" {
				if name, found := apiKeyEnvName(val, provider); found {
					entries = append(entries, ":api-key "+apiKeyForm(name))
				}
				continue
			}
			if lit := schemeScalar(val); lit != "" {
				entries = append(entries, ":"+key+" "+lit)
			}
		}
		if len(entries) == 0 {
			return ""
		}
		return "(define default-model\n  {" + strings.Join(entries, "\n   ") + "})\n"
	}
	return ""
}

func apiKeyForm(envName string) string {
	return fmt.Sprintf("(env %q)", envName)
}

func dictStringValue(d eval.Dictionary, key string) (s string, ok, isString bool) {
	v, ok := d[key]
	if !ok {
		return "", false, false
	}
	str, isString := v.(eval.String)
	return string(str), true, isString
}

func apiKeyEnvName(val eval.Value, provider string) (string, bool) {
	derived := models.APIKeyEnv(provider)
	switch key := val.(type) {
	case bool:
		return derived, !key && provider != ""
	case eval.String:
		if provider != "" && os.Getenv(derived) == string(key) {
			return derived, true
		}
		for _, kv := range os.Environ() {
			if name, v, _ := strings.Cut(kv, "="); v == string(key) && name != "" {
				return name, true
			}
		}
	}
	return "", false
}

func schemeScalar(v eval.Value) string {
	switch t := v.(type) {
	case eval.String:
		return fmt.Sprintf("%q", string(t))
	case bool:
		if t {
			return "#t"
		}
		return "#f"
	case eval.Integer:
		return fmt.Sprintf("%d", int64(t))
	case eval.Number:
		if t == eval.Number(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", float64(t))
	}
	return ""
}

type setupChoices struct {
	provider string
	model    string
	baseURL  string
	thinking bool
	hooks    bool
}

func writePreludeBlock(path, blockBegin, blockEnd, block string) (created bool, err error) {
	src, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return false, readErr
	}
	existing := string(src)
	if existing != "" {
		if _, err := eval.ParseAll(existing); err != nil {
			return false, fmt.Errorf("%s does not parse (%v) — fix it, then run configure", path, err)
		}
	}
	var out string
	begin := strings.Index(existing, blockBegin)
	end := strings.Index(existing, blockEnd)
	switch {
	case begin >= 0 && end > begin:
		tail := existing[end+len(blockEnd):]
		tail = strings.TrimPrefix(tail, "\n")
		out = existing[:begin] + block + tail
	case existing == "":
		out = block
	default:
		out = strings.TrimRight(existing, "\n") + "\n\n" + block
	}
	return readErr != nil, os.WriteFile(path, []byte(out), 0644)
}

var anthropicModels = []pickRow{
	{models.DefaultAnthropicModel, "default — $3 / $15 per Mtok"},
	{"claude-sonnet-5", "current sonnet — $3 / $15"},
	{"claude-opus-5", "stronger, slower — $5 / $25"},
	{"claude-haiku-4-5", "fast and cheap — $1 / $5"},
	{"other", "type a model id"},
}

var allowCommandCandidates = []struct {
	cmd     string
	note    string
	checked bool
}{
	{"git status", "working tree state", true},
	{"git diff", "uncommitted changes", true},
	{"git log", "history", true},
	{"go test", "go test suites", false},
	{"npm test", "npm test scripts", false},
	{"make test", "make test targets", false},
}

func defaultAllowCommands() []string {
	var out []string
	for _, c := range allowCommandCandidates {
		if c.checked {
			out = append(out, c.cmd)
		}
	}
	return out
}

const defaultWorkingDirGrant = "read"

var systemPathDirs = map[string]bool{
	"/usr/local/bin": true, "/usr/local/sbin": true,
	"/usr/bin": true, "/usr/sbin": true,
	"/bin": true, "/sbin": true,
}

func extraPathDirs() []string {
	home, _ := os.UserHomeDir()
	seen := map[string]bool{}
	var out []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		clean := filepath.Clean(dir)
		if !filepath.IsAbs(clean) || systemPathDirs[clean] || seen[clean] {
			continue
		}
		seen[clean] = true
		if home != "" && strings.HasPrefix(clean, home+string(os.PathSeparator)) {
			clean = "~" + strings.TrimPrefix(clean, home)
		}
		out = append(out, clean)
	}
	return out
}

func starterPrelude() string {
	var b strings.Builder
	b.WriteString(";; ~/.rf.scm — rf's prelude, evaluated at startup; plain Scheme (see `help prelude`)\n\n")
	if dirs := extraPathDirs(); len(dirs) > 0 {
		b.WriteString("(add-path")
		for _, d := range dirs {
			b.WriteString(fmt.Sprintf(" %q", d))
		}
		b.WriteString(")\n")
	}
	b.WriteString(`(cond-expand
  (bsd  (alias ls "ls -G"))            ; stock macOS/BSD ls
  (else (alias ls "ls --color=auto"))) ; GNU ls
`)
	return b.String()
}

func runSetupWizard(chatInst *llm.Chat, firstRun bool) {
	var c setupChoices
	if firstRun {
		printSplash()
		fmt.Println("Welcome. rf is one prompt with two modes: " + colorGreen + "command" + colorReset +
			" (a shell — unix commands, pipelines, Scheme in parens) and " + colorCyan + "agent" + colorReset +
			" (a chat with a model that writes Scheme against the same session).")
		fmt.Println("Ctrl+Space or Shift+Tab switches. This wizard sets up the model; every choice is")
		fmt.Println("written to " + colorCyan + "~/.rf.scm" + colorReset + " as plain Scheme you can edit.")
		fmt.Println(colorGray + "esc skips the rest at any point; configure runs this again" + colorReset)
		fmt.Println()
	}

	cfg := chatInst.Config()
	done := func() { finishSetup(c, chatInst, firstRun) }

	providerRows := []pickRow{
		{models.ProviderAnthropic, "claude models (api key required)"},
		{models.ProviderOllama, "local models via an ollama server"},
		{"other", "any anthropic-compatible endpoint — a name, a url, a key"},
		{"skip", "no model for now — the shell works without one; configure adds it later"},
	}
	start := 0
	if cfg.Provider == models.ProviderOllama {
		start = 1
	}
	i, ok := pick("model provider", providerRows, start)
	if !ok {
		done()
		return
	}
	switch providerRows[i].name {
	case models.ProviderAnthropic:
		c.provider = models.ProviderAnthropic
		// 2a. Key — straight to ~/.rf.env, the database never stores it.
		promptProviderKey(c.provider)
		start := 0
		modelDefault := models.DefaultAnthropicModel
		if cfg.Provider == models.ProviderAnthropic && cfg.Model != "" {
			modelDefault = cfg.Model
			start = len(anthropicModels) - 1
		}
		for j, row := range anthropicModels {
			if row.name == cfg.Model {
				start = j
			}
		}
		i, ok := pick("model", anthropicModels, start)
		if !ok {
			done()
			return
		}
		c.model = anthropicModels[i].name
		if c.model == "other" {
			c.model = promptLine("model id", modelDefault)
		}
		// 4. Thinking.
		i, ok = pick("extended thinking", []pickRow{
			{"off", "cheaper, faster"},
			{"on", "the model reasons before answering — :thinking #t in default-model"},
		}, 0)
		if !ok {
			done()
			return
		}
		c.thinking = i == 1
	case models.ProviderOllama:
		c.provider = models.ProviderOllama
		url := cfg.BaseURL
		if cfg.Provider != models.ProviderOllama || url == "" {
			url = models.DefaultOllamaURL
		}
		c.baseURL = promptLine("ollama url", url)
		// 3b. Model — from the server's own list when it answers.
		names := ollamaModels(c.baseURL)
		if len(names) == 0 {
			fmt.Println(colorGray + "no ollama server answered at " + c.baseURL + " — type the model to use; `ollama pull <model>` fetches it" + colorReset)
			c.model = promptLine("model", models.DefaultModel)
		} else {
			rows := make([]pickRow, len(names))
			start := 0
			for j, name := range names {
				rows[j] = pickRow{name: name}
				if name == cfg.Model {
					start = j
				}
			}
			i, ok := pick("model", rows, start)
			if !ok {
				done()
				return
			}
			c.model = rows[i].name
		}
	case "other":
		nameDefault, urlDefault, idDefault := "", "", ""
		if cfg.Provider != models.ProviderAnthropic && cfg.Provider != models.ProviderOllama {
			nameDefault, urlDefault, idDefault = cfg.Provider, cfg.BaseURL, cfg.Model
		}
		c.provider = strings.TrimSpace(promptLine("provider name (e.g. deepseek)", nameDefault))
		if c.provider == "" {
			done()
			return
		}
		c.baseURL = strings.TrimSpace(promptLine("base url (the /v1/messages root)", urlDefault))
		promptProviderKey(c.provider)
		c.model = promptLine("model id", idDefault)
	}

	// 5. Claude Code hooks — recommended, benefit-forward, yes first.
	fmt.Println()
	fmt.Println("Install Claude hooks (recommended). The Claude hooks allow for seamless")
	fmt.Println("integration with rf including per-project task awareness, chat history")
	fmt.Println("search, and session limits in the console.")
	fmt.Println(colorGray + "writes ~/.claude/settings.json hooks, a statusline, and the /task skill; bare `rf hook` shows what's installed" + colorReset)
	i, ok = pick("install the claude hooks", []pickRow{
		{"yes", "hooks + statusline + /task skill"},
		{"no", "leave ~/.claude alone"},
	}, 0)
	if !ok {
		done()
		return
	}
	c.hooks = i == 0
	done()
}

func promptProviderKey(provider string) {
	envName := models.APIKeyEnv(provider)
	fmt.Println(colorGray + "the key is stored in ~/.rf.env as " + envName + "; enter keeps the current value" + colorReset)
	current := os.Getenv(envName)
	if key := promptKey(current); key != current {
		if err := saveAPIKey(envName, key); err != nil {
			fmt.Println(colorRed+"error:"+colorReset, err)
		}
	}
}

func finishSetup(c setupChoices, chatInst *llm.Chat, firstRun bool) {
	fmt.Println()
	path, pathErr := core.PreludePath()
	existedBefore := false
	if pathErr == nil {
		_, statErr := os.Stat(path)
		existedBefore = statErr == nil
	}
	if pathErr == nil && !existedBefore {
		if err := os.WriteFile(path, []byte(starterPrelude()), 0644); err != nil {
			fmt.Println(colorRed+"error:"+colorReset, err)
		}
	}

	modelForm := liveModelForm(chatInst)
	if c.provider != "" && c.model != "" {
		modelForm = modelDefineForm(c.provider, c.model, c.baseURL, c.thinking)
		fmt.Printf("configured: provider=%s model=%s\n", c.provider, c.model)
		if os.Getenv(models.APIKeyEnv(c.provider)) == "" && c.provider != models.ProviderOllama {
			fmt.Println(colorRed+"warning:"+colorReset, "no API key set — run configure again or add", models.APIKeyEnv(c.provider), "to ~/.rf.env")
		}
	}
	workingDir, commands := resolveGrants(chatInst)
	block := configBlock(modelForm, workingDir, commands)
	if pathErr == nil {
		if _, err := writePreludeBlock(path, configBlockBegin, configBlockEnd, block); err != nil {
			fmt.Println(colorRed+"error:"+colorReset, err)
		} else {
			verb := "updated"
			if !existedBefore {
				verb = "created"
			}
			fmt.Println(verb + " " + colorCyan + "~/" + core.PreludeName + colorReset + ":")
			fmt.Print(colorGray + block + colorReset)
		}
	}

	if c.hooks {
		if path, err := console.InstallClaudeHooks(); err != nil {
			fmt.Println(colorRed+"error:"+colorReset, "installing claude code hooks:", err)
		} else if skill, err := console.InstallClaudeTaskSkill(); err != nil {
			fmt.Println(colorRed+"error:"+colorReset, "installing the /task skill:", err)
		} else {
			fmt.Printf("claude code hooks installed in %s, /task skill in %s\n", path, skill)
		}
	}

	if firstRun {
		fmt.Println()
		fmt.Println("try:  " + colorGreen + "help" + colorReset + "            the manual — every topic and command as man pages")
		fmt.Println("      " + colorGreen + "help pipelines" + colorReset + "  one topic; `help history` one command's page")
		fmt.Println("      " + colorCyan + "shift+tab" + colorReset + "       then ask: what is in this directory?")
		fmt.Println()
	}
}

func resolveGrants(chatInst *llm.Chat) (workingDir string, commands []string) {
	workingDir, bound := currentWorkingDirGrant(chatInst)
	if !bound {
		workingDir = defaultWorkingDirGrant
	}
	commands, bound = currentAllowCommands(chatInst)
	if !bound {
		commands = defaultAllowCommands()
	}
	return workingDir, commands
}

func resolveGrantsWith(chatInst *llm.Chat, commands []string) (workingDir string, _ []string) {
	workingDir, bound := currentWorkingDirGrant(chatInst)
	if !bound {
		workingDir = defaultWorkingDirGrant
	}
	return workingDir, commands
}

func configureAllowedCommands(chatInst *llm.Chat) {
	current, _ := currentAllowCommands(chatInst)
	rows := make([]pickRow, 0, len(allowCommandCandidates)+len(current))
	checked := make([]bool, 0, cap(rows))
	seen := map[string]bool{}
	for _, cand := range allowCommandCandidates {
		rows = append(rows, pickRow{name: cand.cmd, note: cand.note})
		checked = append(checked, slices.Contains(current, cand.cmd))
		seen[cand.cmd] = true
	}
	for _, cmd := range current {
		if !seen[cmd] {
			rows = append(rows, pickRow{name: cmd, note: "from ~/.rf.scm"})
			checked = append(checked, true)
		}
	}
	state, ok := pickMulti("run without asking", rows, checked)
	if !ok {
		fmt.Println("cancelled — allowlist unchanged")
		return
	}
	var commands []string
	for i, on := range state {
		if on {
			commands = append(commands, rows[i].name)
		}
	}
	path, err := core.PreludePath()
	if err != nil {
		fmt.Println(colorRed+"error:"+colorReset, err)
		return
	}
	workingDir, _ := resolveGrantsWith(chatInst, commands)
	block := configBlock(liveModelForm(chatInst), workingDir, commands)
	if _, err := writePreludeBlock(path, configBlockBegin, configBlockEnd, block); err != nil {
		fmt.Println(colorRed+"error:"+colorReset, "allowlist not saved:", err)
		return
	}
	fmt.Println("updated " + colorCyan + "~/" + core.PreludeName + colorReset + ":")
	fmt.Print(colorGray + block + colorReset)
}

func currentAllowCommands(chatInst *llm.Chat) (commands []string, bound bool) {
	v, err := chatInst.Env.Lookup("agent-allow-commands")
	if err != nil {
		return nil, false
	}
	list, ok := v.([]eval.Value)
	if !ok {
		return nil, false
	}
	for _, item := range list {
		if s, ok := item.(eval.String); ok {
			commands = append(commands, string(s))
		}
	}
	return commands, true
}

func currentWorkingDirGrant(chatInst *llm.Chat) (grant string, bound bool) {
	v, err := chatInst.Env.Lookup("agent-allow-working-dir")
	if err != nil {
		return "", false
	}
	if kw, ok := v.(eval.Keyword); ok && (kw == "read" || kw == "write") {
		return string(kw), true
	}
	return "", false
}

func saveAPIKey(envName, key string) error {
	envPath, err := core.EnvPath()
	if err != nil {
		return err
	}
	if err := core.SetEnvVar(envPath, envName, key); err != nil {
		return fmt.Errorf("failed to update ~/.rf.env: %w", err)
	}
	if key == "" {
		return os.Unsetenv(envName)
	}
	return os.Setenv(envName, key)
}

func ollamaModels(base string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/tags", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil
	}
	names := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	sort.Strings(names)
	return names
}

func firstRunPending(dbExisted bool) bool {
	if dbExisted || os.Getenv("RF_SKIP_ONBOARDING") != "" {
		return false
	}
	path, err := core.PreludePath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}
