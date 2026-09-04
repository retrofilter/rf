package cmd

import (
	"strings"
	"testing"

	"github.com/retrofilter/rf/docs"
	"github.com/retrofilter/rf/eval"
	"github.com/retrofilter/rf/llm"
	"github.com/stretchr/testify/require"
)

func TestManTopics(t *testing.T) {
	slugs := topicSlugs()
	require.NotEmpty(t, slugs)
	for _, slug := range slugs {
		require.Contains(t, topicDocs, slug, "guide section %q has no topicDocs line", slug)
		page, err := manPage(slug, false)
		require.NoError(t, err)
		require.Contains(t, page, strings.ToUpper(slug)+"(7)")
		require.Contains(t, page, "NAME")
	}
	for slug := range topicDocs {
		require.Contains(t, slugs, slug, "topicDocs entry %q matches no guide section", slug)
	}

	// Contains-matching: a fragment opens its topic.
	page, err := manPage("claude", false)
	require.NoError(t, err)
	require.Contains(t, page, "CLAUDE-CODE(7)")

	_, err = manPage("no-such-page", false)
	require.ErrorContains(t, err, "topics: modes, ")
}

func TestPromptNamesTopics(t *testing.T) {
	prompt := llm.SystemPrompt()
	require.Contains(t, prompt, `(help "topic")`)
	for _, slug := range topicSlugs() {
		require.Contains(t, prompt, slug, "topic %q is not named in llm/prompt.txt", slug)
	}
}

func TestManCommandPage(t *testing.T) {
	_, err := generateHandbook() // populate the full registry
	require.NoError(t, err)

	page, err := manPage("history", false)
	require.NoError(t, err)
	require.Contains(t, page, "HISTORY(1)")
	require.Contains(t, page, "NAME")
	require.Contains(t, page, "SYNOPSIS")
	require.Contains(t, page, "(history [pattern]")
	require.Contains(t, page, "OPTIONS")
	require.Contains(t, page, "-d, --dir PATH")
	require.Contains(t, page, "-h, --help")
}

func TestManOverviewListsEveryPage(t *testing.T) {
	_, err := generateHandbook()
	require.NoError(t, err)

	page := manOverview(false)
	require.True(t, strings.HasPrefix(page, "RF(1)"))
	for _, heading := range []string{"NAME", "DESCRIPTION", "TOPICS", "COMMANDS", "SEE ALSO"} {
		require.Contains(t, page, "\n"+heading+"\n")
	}
	for _, slug := range topicSlugs() {
		require.Contains(t, page, slug+"(7)")
	}
	for _, name := range eval.CommandWords() {
		require.Contains(t, page, name+"(1)", "overview misses command %q", name)
	}
	for _, name := range stageOnlyWords() {
		require.Contains(t, page, name+"(1)", "overview misses stage %q", name)
	}
}

func TestGuideCoversRegistry(t *testing.T) {
	_, err := generateHandbook()
	require.NoError(t, err)
	exempt := map[string]bool{
		// covered by their family's sentence, not by name
		"create-project": true, "create-tree": true, "delete-tree": true, "delete-task": true,
		"delete-edge": true, "delete-graph": true, "unregister-project": true, "unalias": true,
		"remove-path": true, "chunk": true,
	}
	var missing []string
	for _, name := range eval.CommandWords() {
		if exempt[name] || strings.Contains(docs.Guide, "`"+name) || strings.Contains(docs.Guide, "("+name) {
			continue
		}
		missing = append(missing, name)
	}
	require.Empty(t, missing, "command words the guide never mentions")
}

func TestManRendering(t *testing.T) {
	m := newMan(true)
	m.width = 40
	m.markdown("Some `styled` text that should wrap onto more than one line at this width.\n\n```\ncode line stays untouched\n```")
	out := m.String()
	require.Contains(t, out, "\x1b[1mstyled\x1b[0m")
	require.Contains(t, out, manCodeIndent+"code line stays untouched")
	for _, line := range strings.Split(out, "\n") {
		require.LessOrEqual(t, visibleWidth(line), 40, "line overflows: %q", line)
	}
}
