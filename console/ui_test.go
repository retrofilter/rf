package console

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/require"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	require.NoError(t, c.Render(context.Background(), &sb))
	return sb.String()
}

func TestSessionRowRendering(t *testing.T) {
	s := Session{
		ID: "s7", Name: "probe", Title: "rf", Dir: "/Users/j/src",
		Status: StatusRunning, Elapsed: 200, Added: 3, Deleted: 1,
	}
	html := render(t, sessionRow(s, false))
	require.Contains(t, html, `data-session-id="s7"`)
	require.Contains(t, html, "probe")
	require.Contains(t, html, "bg-RUNNING")
	require.Contains(t, html, "st-RUNNING")
	require.Contains(t, html, "rf · ~/src")
	require.Contains(t, html, "3m 20s")
	require.Contains(t, html, "+3 −1")
	require.NotContains(t, html, "selected")

	require.Contains(t, render(t, sessionRow(s, true)), "selected")

	// Zero elapsed renders no empty span.
	html = render(t, sessionRow(Session{ID: "s1", Status: StatusIdle}, false))
	require.Contains(t, html, "no changes")
	require.NotContains(t, html, "<span></span>")
}

func TestRailEscapesHostileContent(t *testing.T) {
	s := Session{ID: "s1", Name: `<script>alert(1)</script>`, Title: `<img src=x>`, Status: StatusIdle}
	html := render(t, railRows(viewModel{Sessions: []Session{s}, View: viewTerminal}))
	require.NotContains(t, html, "<script>")
	require.NotContains(t, html, "<img")
	require.Contains(t, html, "&lt;script&gt;")
}

func TestRailAndTabsPendingEntry(t *testing.T) {
	m := viewModel{Sessions: []Session{{ID: "s1", Name: "one", Status: StatusIdle}}, View: viewNew}
	rail := render(t, railRows(m))
	require.Contains(t, rail, "data-pending")
	require.Contains(t, rail, "new session")
	// While naming, no real row is selected even if active is set.
	m.Active = "s1"
	require.NotContains(t, render(t, tabItems(m)), "tab active\" data-session-id")

	m.View = viewTerminal
	tabs := render(t, tabItems(m))
	require.Contains(t, tabs, `data-session-id="s1"`)
	require.Contains(t, tabs, "one")
	require.NotContains(t, tabs, "data-pending")
}

func TestTabsCloseButtonsAndGraphTab(t *testing.T) {
	m := viewModel{Sessions: []Session{{ID: "s1", Name: "one", Status: StatusIdle}}, View: viewTerminal}

	// Session tabs close; the home tab does not; no graph tab until opened.
	tabs := render(t, tabItems(m))
	require.Equal(t, 1, strings.Count(tabs, "tab-x"))
	require.NotContains(t, tabs, "data-graph")

	// An open graph tab renders closable, active only when it has the panel.
	m.GraphOpen = true
	tabs = render(t, tabItems(m))
	require.Contains(t, tabs, "data-graph")
	require.Contains(t, tabs, "graph-view")
	require.Equal(t, 2, strings.Count(tabs, "tab-x"))
	require.NotContains(t, tabs, "tab-graph active")
	m.View = viewGraph
	require.Contains(t, render(t, tabItems(m)), "tab-graph active")

	// The pending naming tab carries a cancel ×.
	m.View = viewNew
	m.GraphOpen = false
	tabs = render(t, tabItems(m))
	require.Contains(t, tabs, "data-pending")
	require.Equal(t, 2, strings.Count(tabs, "tab-x"))
}

func TestUsageBars(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	// No usage: both bars render hidden (so the oob swap can un-hide later).
	html := render(t, usageBarsOOB(viewModel{Now: now}))
	require.Contains(t, html, `id="u-session"`)
	require.Contains(t, html, `id="u-week"`)
	require.Equal(t, 2, strings.Count(html, "hidden"))
	require.Equal(t, 2, strings.Count(html, `hx-swap-oob="true"`))

	u := &Usage{
		Session: UsageWindow{Utilization: 61.7, ResetsAt: now.Add(2*time.Hour + 14*time.Minute)},
		Week:    UsageWindow{Utilization: 12, ResetsAt: now.Add(3*24*time.Hour + 4*time.Hour)},
	}
	html = render(t, usageBars(viewModel{Usage: u, Now: now}))
	require.NotContains(t, html, "hidden")
	require.NotContains(t, html, "hx-swap-oob")
	require.Contains(t, html, "width: 62%")
	require.Contains(t, html, "62%")
	require.Contains(t, html, "2h 14m")
	require.Contains(t, html, "width: 12%")
	require.Contains(t, html, "3d 4h")
}

func TestSysBars(t *testing.T) {
	// No sample: the bar renders hidden (so the oob swap can un-hide later).
	html := render(t, sysBarsOOB(viewModel{}))
	require.Contains(t, html, `id="sys-mem"`)
	require.Equal(t, 1, strings.Count(html, "hidden"))
	require.Equal(t, 1, strings.Count(html, `hx-swap-oob="true"`))

	// The fill is percent; the label is used GB to two significant figures.
	html = render(t, sysBars(viewModel{Sys: &SysStat{Mem: 61.7, UsedKB: 20 * 1024 * 1024}}))
	require.NotContains(t, html, "hidden")
	require.NotContains(t, html, "hx-swap-oob")
	require.Contains(t, html, "Mem")
	require.Contains(t, html, "width: 62%")
	require.Contains(t, html, `<span class="pct">20G</span>`)

	// Under 10G the label keeps one decimal; small values stay in G, never MB.
	html = render(t, sysBars(viewModel{Sys: &SysStat{Mem: 4.2, UsedKB: 512 * 1024}}))
	require.Contains(t, html, "0.5G")
}

func TestSysBarsPlacement(t *testing.T) {
	html := render(t, page(viewModel{View: viewTerminal, Whoami: "j@h", Now: time.Now()}))
	mem := strings.Index(html, `id="sys-mem"`)
	running := strings.Index(html, `id="running-count"`)
	require.Greater(t, running, 0)
	require.Greater(t, mem, running)
}

func TestPollFragmentOOBChrome(t *testing.T) {
	m := viewModel{
		Sessions: []Session{
			{ID: "s1", Name: "one", Status: StatusRunning, Harness: "claude"},
			{ID: "s2", Name: "two", Status: StatusIdle},
		},
		Active: "s1",
		View:   viewTerminal,
		Now:    time.Now(),
	}
	html := render(t, pollFragment(m))
	// Main swap: the rail rows, active one selected.
	require.Contains(t, html, `data-session-id="s1"`)
	require.Contains(t, html, "selected")
	// The chrome rides out-of-band.
	require.Contains(t, html, `id="tab-list" style="display: contents" hx-swap-oob="innerHTML"`)
	for _, oob := range []string{`id="session-count"`, `id="running-count"`, `id="ts-harness"`} {
		require.Contains(t, html, oob)
	}
	require.Contains(t, html, ">1 Running<")
	require.Contains(t, html, ">2</span>")
	require.Contains(t, html, ">Claude<")
}

func TestPageSkeleton(t *testing.T) {
	m := viewModel{
		Sessions: []Session{{ID: "s1", Name: "one", Status: StatusIdle}},
		View:     viewTerminal,
		Whoami:   "john@mac",
		Now:      time.Now(),
	}
	html := render(t, page(m))
	require.Contains(t, html, "<!doctype html>")
	// The htmx wiring: the poll loop and the spawn form.
	require.Contains(t, html, "/static/vendor/htmx.min.js")
	require.Contains(t, html, `hx-get="/ui/poll"`)
	require.Contains(t, html, `hx-trigger="load, every 2s, rf:refresh from:body"`)
	require.Contains(t, html, `hx-post="/ui/sessions"`)
	require.Contains(t, html, `hx-sync="this:drop"`)
	// The xterm mount and the initial server-rendered state.
	require.Contains(t, html, `id="term-mount"`)
	require.Contains(t, html, "john@mac")
	require.Contains(t, html, `data-session-id="s1"`)
	require.Contains(t, html, `id="theme-toggle"`)
	require.Contains(t, html, `class="icon-sun"`)
	require.Contains(t, html, `class="icon-moon"`)
	require.Contains(t, html, `localStorage.getItem('rf-theme')`)
}
