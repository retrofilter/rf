package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/llm"
	"github.com/stretchr/testify/require"
)

func TestModelDefineForm(t *testing.T) {
	form := modelDefineForm("anthropic", "claude-sonnet-4-6", "", false)
	require.Contains(t, form, `{:provider "anthropic"`)
	require.Contains(t, form, `:id "claude-sonnet-4-6"`)
	require.NotContains(t, form, ":base-url")
	require.NotContains(t, form, ":thinking")
	require.Contains(t, form, `:api-key (env "RF_ANTHROPIC_API_KEY")`)

	// The default ollama URL is equally quiet, and ollama has no key.
	require.NotContains(t, modelDefineForm("ollama", "granite4:3b", "http://localhost:11434", false), ":base-url")
	require.NotContains(t, modelDefineForm("ollama", "granite4:3b", "", false), ":api-key")
	require.Contains(t, modelDefineForm("ollama", "granite4:3b", "http://box:11434", false), `:base-url "http://box:11434"`)

	form = modelDefineForm("deepseek", "deepseek-chat", "https://api.deepseek.com/anthropic", true)
	require.Contains(t, form, `{:provider "deepseek"`)
	require.Contains(t, form, `:base-url "https://api.deepseek.com/anthropic"`)
	require.Contains(t, form, `:api-key (env "RF_DEEPSEEK_API_KEY")`)
	require.Contains(t, form, ":thinking #t")

	// Every rendered form parses — it is the prelude's content.
	_, err := eval.ParseAll(form)
	require.NoError(t, err)
}

func TestConfigBlock(t *testing.T) {
	// One block: model + grants between two terse markers, no comments.
	block := configBlock(modelDefineForm("ollama", "granite4:3b", "", false), "read", []string{"git status", "go test"})
	require.True(t, strings.HasPrefix(block, configBlockBegin+"\n"))
	require.True(t, strings.HasSuffix(block, configBlockEnd+"\n"))
	require.Contains(t, block, "(define default-model")
	require.Contains(t, block, "(define agent-allow-working-dir :read)")
	require.Contains(t, block, `(define agent-allow-commands '("git status" "go test"))`)
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, ";;") {
			require.Contains(t, []string{configBlockBegin, configBlockEnd}, line, "no inner comments")
		}
	}
	_, err := eval.ParseAll(block)
	require.NoError(t, err)

	// No model configured: the model define is simply absent.
	require.NotContains(t, configBlock("", "read", nil), "default-model")
}

func TestWritePreludeBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".rf.scm")
	block := configBlock("", "read", defaultAllowCommands())

	created, err := writePreludeBlock(path, configBlockBegin, configBlockEnd, block)
	require.NoError(t, err)
	require.True(t, created)
	got, _ := os.ReadFile(path)
	require.Equal(t, block, string(got), "a new prelude is exactly the block")

	own := "(add-path \"~/bin\")\n(define x 1)\n"
	require.NoError(t, os.WriteFile(path, []byte(own+"\n"+block+"(define y 2)\n"), 0644))
	next := configBlock("", "write", []string{"git diff"})
	created, err = writePreludeBlock(path, configBlockBegin, configBlockEnd, next)
	require.NoError(t, err)
	require.False(t, created)
	got, _ = os.ReadFile(path)
	require.Equal(t, own+"\n"+next+"(define y 2)\n", string(got))
	require.Equal(t, 1, strings.Count(string(got), configBlockBegin))

	// No block yet: append after a blank line.
	require.NoError(t, os.WriteFile(path, []byte(own), 0644))
	_, err = writePreludeBlock(path, configBlockBegin, configBlockEnd, block)
	require.NoError(t, err)
	got, _ = os.ReadFile(path)
	require.Equal(t, own+"\n"+block, string(got))

	// A prelude that doesn't parse is left alone.
	require.NoError(t, os.WriteFile(path, []byte("(define broken"), 0644))
	_, err = writePreludeBlock(path, configBlockBegin, configBlockEnd, block)
	require.Error(t, err)
	got, _ = os.ReadFile(path)
	require.Equal(t, "(define broken", string(got))
}

func TestFirstRunPending(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RF_SKIP_ONBOARDING", "")
	require.True(t, firstRunPending(false), "fresh db and no prelude")
	require.False(t, firstRunPending(true), "an existing db is never a first run")
	t.Setenv("RF_SKIP_ONBOARDING", "1")
	require.False(t, firstRunPending(false), "harness opt-out")
	t.Setenv("RF_SKIP_ONBOARDING", "")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".rf.scm"), nil, 0644))
	require.False(t, firstRunPending(false), "the prelude is the onboarded marker")
}

func TestStarterPrelude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", strings.Join([]string{
		"/usr/bin", "/bin", "/usr/local/bin", // system entries stay out
		home + "/go/bin",   // home-prefixed shortens to ~
		"/opt/tools/bin",   // non-system entry carries over
		"/opt/tools/bin",   // duplicates collapse
		"relative/bin", "", // non-absolute entries are dropped
	}, string(os.PathListSeparator)))

	seed := starterPrelude()
	require.Contains(t, seed, `(add-path "~/go/bin" "/opt/tools/bin")`)
	require.NotContains(t, seed, "/usr/bin")
	require.Contains(t, seed, "(cond-expand")

	forms, err := eval.ParseAll(seed)
	require.NoError(t, err)
	ev := eval.NewEvaluator()
	_, err = ev.EvalAll(forms, ev.GlobalEnv())
	require.NoError(t, err)
	exp, ok := ev.LookupAlias("ls")
	require.True(t, ok)
	if runtime.GOOS == "darwin" {
		require.Equal(t, "ls -G", exp)
	} else {
		require.Equal(t, "ls --color=auto", exp)
	}
	path := filepath.Join(home, ".rf.scm")
	require.NoError(t, os.WriteFile(path, []byte(seed), 0644))
	created, err := writePreludeBlock(path, configBlockBegin, configBlockEnd, configBlock("", "read", defaultAllowCommands()))
	require.NoError(t, err)
	require.False(t, created)
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(src), ";; ~/.rf.scm"))
	require.Less(t, strings.Index(string(src), "(alias ls"), strings.Index(string(src), configBlockBegin))

	// An empty PATH still seeds the alias, with no add-path form.
	t.Setenv("PATH", "/usr/bin")
	require.NotContains(t, starterPrelude(), "add-path")
}

func TestLiveModelFormAPIKey(t *testing.T) {
	t.Setenv("RF_ANTHROPIC_API_KEY", "sk-derived")
	t.Setenv("MY_OTHER_KEY", "sk-other")
	chat := llm.NewChatForScript(llm.Config{})
	set := func(key eval.Value) {
		chat.Env.Set("default-model", eval.Dictionary{"provider": eval.String("anthropic"), "id": eval.String("m"), "api-key": key})
	}
	set(eval.String("sk-derived"))
	require.Contains(t, liveModelForm(chat), `:api-key (env "RF_ANTHROPIC_API_KEY")`)
	set(eval.String("sk-other"))
	require.Contains(t, liveModelForm(chat), `:api-key (env "MY_OTHER_KEY")`)
	set(false)
	require.Contains(t, liveModelForm(chat), `:api-key (env "RF_ANTHROPIC_API_KEY")`)
	set(eval.String("sk-nowhere"))
	form := liveModelForm(chat)
	require.NotContains(t, form, "api-key")
	require.NotContains(t, form, "sk-nowhere")
}
