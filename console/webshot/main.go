// webshot is a development tool, not a test: it boots a scripted console in-
// process, drives headless Chrome through the real flows, and saves a PNG per
// state to tmp/webshots/.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/retrofilter/rf/console"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
	"github.com/retrofilter/rf/models/schema"

	"net/http/httptest"
)

const token = "webshot-webshot-webshot-webshot"

const overviewGraphName = "knowledge-base"

func main() {
	out := flag.String("out", "tmp/webshots", "directory for the PNGs")
	spawn := flag.String("cmd", "sh -i", "command spawned per session (default keeps the tool off the real rf config/db)")
	flag.Parse()

	if err := run(*out, *spawn); err != nil {
		fmt.Fprintln(os.Stderr, "webshot:", err)
		os.Exit(1)
	}
}

func run(out, spawnCmd string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	ts, err := bootServer(spawnCmd)
	if err != nil {
		return err
	}
	defer ts.Close()

	actx, acancel := chromedp.NewExecAllocator(context.Background(), chromedp.DefaultExecAllocatorOptions[:]...)
	defer acancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, tcancel := context.WithTimeout(ctx, 60*time.Second)
	defer tcancel()

	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1280, 800),
		chromedp.Navigate(ts.URL+"/?token="+token),
	); err != nil {
		return fmt.Errorf("boot: %w (is Chrome installed?)", err)
	}

	for _, sc := range scenarios {
		if err := sc.steps(ctx); err != nil {
			return fmt.Errorf("scenario %q: %w", sc.name, err)
		}
		if err := shoot(ctx, filepath.Join(out, sc.name+".png")); err != nil {
			return fmt.Errorf("scenario %q: %w", sc.name, err)
		}
	}
	return nil
}

func bootServer(spawnCmd string) (*httptest.Server, error) {
	tmp, err := os.MkdirTemp("", "webshot")
	if err != nil {
		return nil, err
	}
	db, err := schema.OpenDB(filepath.Join(tmp, "webshot.db"))
	if err != nil {
		return nil, err
	}
	if err := schema.CreateTables(db); err != nil {
		return nil, err
	}
	gs := core.NewGraphStore(db)
	if _, err := gs.CreateGraph(overviewGraphName); err != nil {
		return nil, err
	}
	cg, err := gs.GetGraph(overviewGraphName)
	if err != nil {
		return nil, err
	}
	seed(cg, tmp)

	s := console.NewServer(console.Config{Token: token, SpawnCommand: spawnCmd, Store: gs})
	return httptest.NewServer(s.Handler()), nil
}

func seed(cg *core.Graph, dir string) {
	retro := seedNode(cg, "project", map[string]any{"name": "retrofilter", "path": dir, "kind": "dir"})
	bench := seedNode(cg, "project", map[string]any{"name": "benchmarks", "path": dir, "kind": "dir"})
	for _, seed := range []struct {
		text    string
		project uint32
	}{
		{"rename a session from the rail", retro},
		{"overview keyboard paging", retro},
		{"cap open tasks behind a more row", retro},
		{"replay cursor probes on attach", retro},
		{"light theme for spawned shells", retro},
		{"jupyter-style token auth", retro},
		{"ring buffer replay on refresh", retro},
		{"rerun BRIGHT with new prompts", bench},
	} {
		task := seedNode(cg, "task", map[string]any{"text": seed.text, "status": "open"})
		seedEdge(cg, "for", task, seed.project)
	}
	for text, project := range map[string]uint32{
		"switch sessions to direct PTYs": retro,
		"webshot capability":             retro,
		"score Haiku rerank on BRIGHT":   bench,
	} {
		task := seedNode(cg, "task", map[string]any{"text": text, "status": "done"})
		seedEdge(cg, "for", task, project)
	}
	for text, project := range map[string]uint32{
		"sessions are direct child PTYs of rf console": retro,
		"BRIGHT is the retrieval benchmark":            bench,
	} {
		fact := seedNode(cg, "fact", map[string]any{"content": text})
		seedEdge(cg, "about", fact, project)
	}
}

func seedNode(cg *core.Graph, nodeType string, props map[string]any) uint32 {
	props["type"] = nodeType
	raw, _ := json.Marshal(props)
	id, err := cg.InsertNode(context.Background(), &models.Node{
		GraphID:    cg.ID,
		Type:       &nodeType,
		Properties: (*json.RawMessage)(&raw),
	})
	if err != nil {
		panic(err)
	}
	return id
}

func seedEdge(cg *core.Graph, edgeType string, source, target uint32) {
	raw, _ := json.Marshal(map[string]any{"type": edgeType})
	if _, err := cg.InsertEdge(context.Background(), &models.Edge{
		GraphID:    cg.ID,
		Source:     source,
		Target:     target,
		Type:       &edgeType,
		Properties: (*json.RawMessage)(&raw),
	}); err != nil {
		panic(err)
	}
}

func visible(sel string) chromedp.Action {
	return chromedp.WaitVisible(sel, chromedp.ByQuery)
}

func settle() chromedp.Action {
	return chromedp.Sleep(300 * time.Millisecond)
}

var scenarios = []struct {
	name  string
	steps func(context.Context) error
}{
	{
		name: "overview",
		steps: func(ctx context.Context) error {
			return chromedp.Run(ctx,
				visible("#overview-view"),
				visible(".ov-row"),
				settle(),
			)
		},
	},
	{
		name: "overview-expanded",
		steps: func(ctx context.Context) error {
			return chromedp.Run(ctx,
				chromedp.Click(".ov-more", chromedp.ByQuery),
				chromedp.WaitNotPresent(".ov-more", chromedp.ByQuery),
				settle(),
			)
		},
	},
	{
		name: "graph",
		steps: func(ctx context.Context) error {
			return chromedp.Run(ctx,
				chromedp.Click("#graph-open", chromedp.ByQuery),
				visible("#graph-view"),
				visible(".gl-chip"),
				chromedp.Sleep(1500*time.Millisecond),
			)
		},
	},
	{
		name: "graph-node",
		steps: func(ctx context.Context) error {
			return chromedp.Run(ctx,
				chromedp.Evaluate(`RFGraph.select(RFGraph.ids('project')[0])`, nil),
				visible("#graph-side"),
				settle(),
			)
		},
	},
	{
		name: "new-session",
		steps: func(ctx context.Context) error {
			return chromedp.Run(ctx,
				chromedp.Click("#tab-new", chromedp.ByQuery),
				visible("#new-view"),
				settle(),
			)
		},
	},
	{
		name: "terminal",
		steps: func(ctx context.Context) error {
			return chromedp.Run(ctx,
				chromedp.SendKeys("#nv-name", "alpha\r", chromedp.ByQuery),
				visible("#term-host"),
				chromedp.WaitVisible(`//div[contains(@class,"tab") and contains(@class,"active") and normalize-space(text())="alpha"]`),
				settle(),
			)
		},
	},
	{
		name: "tab-switch",
		steps: func(ctx context.Context) error {
			return chromedp.Run(ctx,
				chromedp.Click("#tab-new", chromedp.ByQuery),
				visible("#new-view"),
				chromedp.SendKeys("#nv-name", "beta\r", chromedp.ByQuery),
				chromedp.WaitVisible(`//div[contains(@class,"tab") and contains(@class,"active") and normalize-space(text())="beta"]`),
				chromedp.Click(".tab-overview", chromedp.ByQuery),
				visible("#overview-view"),
				chromedp.Click(`//div[contains(@class,"tab") and normalize-space(text())="alpha"]`),
				visible("#term-host"),
				chromedp.WaitVisible(`//div[contains(@class,"tab") and contains(@class,"active") and normalize-space(text())="alpha"]`),
				settle(),
			)
		},
	},
	{
		name: "tab-close",
		steps: func(ctx context.Context) error {
			return chromedp.Run(ctx,
				chromedp.Click(`//div[contains(@class,"tab") and normalize-space(text())="beta"]//span[contains(@class,"tab-x")]`),
				chromedp.WaitNotPresent(`//div[contains(@class,"tab") and normalize-space(text())="beta"]`),
				settle(),
			)
		},
	},
}

func shoot(ctx context.Context, path string) error {
	var png []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&png)); err != nil {
		return err
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}
