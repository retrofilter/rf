package console

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/retrofilter/rf/models"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

const overviewGraphName = "knowledge-base"

// OverviewTask is one startable task row.
type OverviewTask struct {
	ID   uint32
	Text string
}

// OverviewProject groups a project's open tasks under its header row, with
// its most recently completed tasks (capped at overviewDoneCap) shown
// underneath. Text is the project's markdown notes rendered to HTML.
type OverviewProject struct {
	ID    uint32
	Name  string
	Path  string
	Text  string
	Tasks []OverviewTask
	Done  []OverviewTask
}

const overviewDoneCap = 2

const overviewTaskCap = 3

func (s *Server) overview() []OverviewProject {
	if s.store == nil {
		return nil
	}
	cg, err := s.store.GetGraph(overviewGraphName)
	if err != nil {
		return nil
	}
	nodes, err := cg.NodesByType("project", 0)
	if err != nil {
		return nil
	}
	byName := map[string]*OverviewProject{}
	projects := []*OverviewProject{}
	for _, node := range nodes {
		props := node.FormattedProperties()
		name, _ := props["name"].(string)
		path, _ := props["path"].(string)
		text, _ := props["text"].(string)
		if name == "" {
			continue
		}
		p := &OverviewProject{ID: node.ID, Name: name, Path: path, Text: renderMarkdown(text)}
		byName[name] = p
		projects = append(projects, p)
	}
	rows, err := cg.SearchTasks(models.TaskQuery{Status: "open"})
	if err != nil {
		rows = nil
	}
	unfiled := &OverviewProject{}
	for _, row := range rows {
		props := row.FormattedProperties()
		text, _ := props["text"].(string)
		task := OverviewTask{ID: row.ID, Text: text}
		if p, ok := byName[row.Project]; ok {
			p.Tasks = append(p.Tasks, task)
		} else {
			unfiled.Tasks = append(unfiled.Tasks, task)
		}
	}
	done, err := cg.SearchTasks(models.TaskQuery{Status: "done"})
	if err != nil {
		done = nil
	}
	sort.SliceStable(done, func(i, j int) bool {
		if !done[i].UpdatedAt.Equal(done[j].UpdatedAt) {
			return done[i].UpdatedAt.After(done[j].UpdatedAt)
		}
		return done[i].ID > done[j].ID
	})
	for _, row := range done {
		p, ok := byName[row.Project]
		if !ok {
			p = unfiled
		}
		if len(p.Done) < overviewDoneCap {
			props := row.FormattedProperties()
			text, _ := props["text"].(string)
			p.Done = append(p.Done, OverviewTask{ID: row.ID, Text: text})
		}
	}
	out := make([]OverviewProject, 0, len(projects)+1)
	for _, p := range projects {
		out = append(out, *p)
	}
	if len(unfiled.Tasks) > 0 || len(unfiled.Done) > 0 {
		out = append(out, *unfiled)
	}
	return out
}

var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

func renderMarkdown(src string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(src), &buf); err != nil {
		return ""
	}
	return buf.String()
}

type startSpec struct {
	name, dir, initial string
}

func (s *Server) startSpecFor(kind string, id uint32) (startSpec, error) {
	if s.store == nil {
		return startSpec{}, fmt.Errorf("no database available")
	}
	cg, err := s.store.GetGraph(overviewGraphName)
	if err != nil {
		return startSpec{}, err
	}
	ctx := context.Background()
	node, err := cg.GetNode(ctx, id)
	if err != nil {
		return startSpec{}, fmt.Errorf("no such %s: %d", kind, id)
	}
	props := node.FormattedProperties()
	switch {
	case kind == "project" && node.Type != nil && *node.Type == "project":
		name, _ := props["name"].(string)
		path, _ := props["path"].(string)
		return startSpec{name: sessionSlug(name, id), dir: path}, nil
	case kind == "task" && node.Type != nil && *node.Type == "task":
		text, _ := props["text"].(string)
		spec := startSpec{name: sessionSlug(text, id), initial: "claude " + shellQuote(text)}
		if neighbors, err := cg.GetNeighbors(ctx, id); err == nil {
			for _, n := range neighbors {
				if n.Type != nil && *n.Type == "project" {
					if path, ok := n.FormattedProperties()["path"].(string); ok {
						spec.dir = path
					}
					break
				}
			}
		}
		return spec, nil
	}
	return startSpec{}, fmt.Errorf("node %d is not a %s", id, kind)
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// SessionSlug derives a session name from a task text, project name, or
// command: lowercased, non-name runs collapsed to one dash, harness words
// (claude, agent) dropped from the front, capped; "" when nothing survives.
func SessionSlug(text string) string {
	words := strings.Fields(text)
	for len(words) > 1 {
		if head := slugWord(words[0]); head != "claude" && head != "agent" {
			break
		}
		words = words[1:]
	}
	slug := slugWord(strings.Join(words, " "))
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	return slug
}

func slugWord(text string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(text), "-"), "-")
}

func sessionSlug(text string, id uint32) string {
	if slug := SessionSlug(text); slug != "" {
		return slug
	}
	return fmt.Sprintf("task-%d", id)
}
