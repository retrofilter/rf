package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
	"go.yaml.in/yaml/v3"
)

const frontMatterFence = "---"

var noteReservedKeys = map[string]bool{"type": true, "text": true, "session": true}

func noteSlug(name string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func noteByName(cg *core.Graph, name string) (*models.Node, error) {
	nodes, err := cg.GetNodesByAttributes(context.Background(), map[string]interface{}{"type": "note", "name": name})
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	return nodes[0], nil
}

func noteProjectName(cg *core.Graph, id uint32) string {
	edges, err := cg.GetNodeEdges(context.Background(), id)
	if err != nil {
		return ""
	}
	for _, e := range edges {
		if e.Source == id && e.Type != nil && *e.Type == "for" {
			if p, err := cg.GetNode(context.Background(), e.Target); err == nil {
				name, _ := p.FormattedProperties()["name"].(string)
				return name
			}
		}
	}
	return ""
}

func splitFrontMatter(doc string) (map[string]interface{}, string, error) {
	doc = strings.ReplaceAll(doc, "\r\n", "\n")
	if !strings.HasPrefix(doc, frontMatterFence+"\n") && doc != frontMatterFence {
		return nil, doc, nil
	}
	rest := strings.TrimPrefix(doc, frontMatterFence)
	rest = strings.TrimPrefix(rest, "\n")
	lines := strings.Split(rest, "\n")
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == frontMatterFence {
			fm := map[string]interface{}{}
			if err := yaml.Unmarshal([]byte(strings.Join(lines[:i], "\n")), &fm); err != nil {
				return nil, "", fmt.Errorf("front matter: %w", err)
			}
			body := strings.Join(lines[i+1:], "\n")
			return fm, strings.TrimPrefix(body, "\n"), nil
		}
	}
	return nil, "", errors.New("front matter: opening --- without a closing ---")
}

func renderFrontMatter(name, project string, extras map[string]interface{}) string {
	var sb strings.Builder
	sb.WriteString(frontMatterFence + "\n")
	line := func(k string, v interface{}) {
		out, err := yaml.Marshal(map[string]interface{}{k: v})
		if err != nil {
			out = []byte(fmt.Sprintf("%s: %v\n", k, v))
		}
		sb.Write(out)
	}
	line("name", name)
	if project != "" {
		line("project", project)
	}
	keys := make([]string, 0, len(extras))
	for k := range extras {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		line(k, extras[k])
	}
	sb.WriteString(frontMatterFence + "\n")
	return sb.String()
}

func noteExtras(props map[string]interface{}) map[string]interface{} {
	extras := map[string]interface{}{}
	for k, v := range props {
		if noteReservedKeys[k] || k == "name" {
			continue
		}
		extras[k] = v
	}
	return extras
}

func renderNoteDoc(cg *core.Graph, node *models.Node) string {
	props := node.FormattedProperties()
	name, _ := props["name"].(string)
	body, _ := props["text"].(string)
	doc := renderFrontMatter(name, noteProjectName(cg, node.ID), noteExtras(props))
	if body != "" {
		doc += "\n" + body
	}
	if !strings.HasSuffix(doc, "\n") {
		doc += "\n"
	}
	return doc
}

func noteTitle(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
		if runes := []rune(line); len(runes) > 60 {
			return string(runes[:59]) + "…"
		}
		return line
	}
	return ""
}

func cwdProjectNode(cg *core.Graph) (uint32, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return 0, nil
	}
	name, hub, ok := FindProject(cwd)
	if !ok {
		return 0, nil
	}
	return taskProjectNode(cg, name, hub)
}

func namedProjectNode(cg *core.Graph, name string) (uint32, error) {
	if cwd, err := os.Getwd(); err == nil {
		if cwdName, hub, ok := FindProject(cwd); ok && cwdName == name {
			return taskProjectNode(cg, name, hub)
		}
	}
	return taskProjectNode(cg, name, "")
}

type noteSave struct {
	existing *models.Node
	slug     string
	doc      string
	project  *string // explicit --project; nil leaves it to the document
}

func saveNote(cg *core.Graph, ev *Evaluator, in noteSave) (Value, error) {
	fm, body, err := splitFrontMatter(in.doc)
	if err != nil {
		return nil, err
	}
	name := in.slug
	if v, ok := fm["name"]; ok {
		s, _ := v.(string)
		if name = noteSlug(s); name == "" {
			return nil, errors.New("front matter: name is empty")
		}
	}
	renamed := in.existing != nil && name != in.slug
	if in.existing == nil || renamed {
		if clash, err := noteByName(cg, name); err != nil {
			return nil, err
		} else if clash != nil {
			return nil, fmt.Errorf("note %q already exists", name)
		}
	}

	projectID := uint32(0)
	switch {
	case in.project != nil:
		if *in.project != "" {
			if projectID, err = namedProjectNode(cg, *in.project); err != nil {
				return nil, err
			}
		}
	case fm != nil && hasKey(fm, "project"):
		if pname, _ := fm["project"].(string); pname != "" {
			if projectID, err = namedProjectNode(cg, pname); err != nil {
				return nil, err
			}
		}
	case in.existing != nil:
		if pname := noteProjectName(cg, in.existing.ID); pname != "" {
			projectID, _ = taskProjectNode(cg, pname, "")
		}
	default:
		if projectID, err = cwdProjectNode(cg); err != nil {
			return nil, err
		}
	}

	props := map[string]interface{}{"type": "note", "name": name, "text": strings.Trim(body, "\n")}
	if in.existing != nil {
		if fm == nil {
			for k, v := range noteExtras(in.existing.FormattedProperties()) {
				props[k] = v
			}
		}
		if s, ok := in.existing.FormattedProperties()["session"].(string); ok && s != "" {
			props["session"] = s
		}
	} else if ev.sessionID != "" {
		props["session"] = ev.sessionID
	}
	for k, v := range fm {
		if k == "name" || k == "project" {
			continue
		}
		if noteReservedKeys[k] {
			return nil, fmt.Errorf("front matter: %q is reserved", k)
		}
		props[k] = v
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return nil, err
	}

	var id uint32
	if in.existing != nil {
		id = in.existing.ID
		in.existing.Properties = (*json.RawMessage)(&propsJSON)
		if _, err := cg.UpdateNode(context.Background(), in.existing); err != nil {
			return nil, err
		}
		edges, err := cg.GetNodeEdges(context.Background(), id)
		if err != nil {
			return nil, err
		}
		for _, e := range edges {
			if e.Source == id && e.Type != nil && *e.Type == "for" {
				if err := cg.DeleteEdge(context.Background(), e.ID); err != nil {
					return nil, err
				}
			}
		}
	} else {
		nodeType := "note"
		if id, err = cg.InsertNode(context.Background(), &models.Node{
			GraphID:    cg.ID,
			Type:       &nodeType,
			Properties: (*json.RawMessage)(&propsJSON),
		}); err != nil {
			return nil, fmt.Errorf("note failed: %w", err)
		}
	}
	if projectID != 0 {
		if err := insertTypedEdge(cg, "for", id, projectID, nil); err != nil {
			return nil, err
		}
	}
	unresolved, err := syncLinks(cg, id, body)
	if err != nil {
		return nil, err
	}
	if in.existing == nil || renamed {
		if err := relinkMentions(cg, Ref{Kind: "note", Key: name}); err != nil {
			return nil, err
		}
	}
	result := Dictionary{"id": Integer(id), "name": String(name), "ref": String("note:" + name)}
	if len(unresolved) > 0 {
		refs := make([]Value, len(unresolved))
		for i, r := range unresolved {
			refs[i] = String(r.String())
		}
		result["unresolved"] = refs
	}
	return result, nil
}

func hasKey(m map[string]interface{}, k string) bool {
	_, ok := m[k]
	return ok
}

// NoteNames returns the completion source for `note <tab>`: every note's
// slug, sorted.
func NoteNames() []string {
	cg := registryGraph()
	if cg == nil {
		return nil
	}
	nodes, err := cg.NodesByType("note", 0)
	if err != nil {
		return nil
	}
	var names []string
	for _, node := range nodes {
		if name, _ := node.FormattedProperties()["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func noteBuiltins(env *Environment, ev *Evaluator) {
	Register("note", "show a named markdown note, or open $EDITOR to create it", CommandMeta{
		Command: true, MinArgs: 0, MaxArgs: 1, Usage: "name",
		Options: []Option{
			{Long: "edit", Short: "e", Kind: OptionBool, Doc: "open the note in $EDITOR (a missing note opens there without the flag)"},
			{Long: "text", Short: "t", Kind: OptionString, Placeholder: "MARKDOWN", Doc: "set the note's document without an editor (a body, or ---front matter--- plus body)"},
			{Long: "project", Short: "p", Kind: OptionString, Placeholder: "NAME", Doc: "file under the named project (\"\" unfiles; default: the cwd's on creation)"},
			{Long: "delete", Short: "d", Kind: OptionBool, Doc: "delete the note outright (permanent)"},
		}})
	env.Set("note", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("note", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, errors.New("note expects a name: (note \"slug\"), (note \"slug\" {:edit #t}), (note \"slug\" {:text MD}), (note \"slug\" {:delete #t})")
		}
		raw, ok := asString(pos[0])
		if !ok {
			return nil, errors.New("note expects a string name")
		}
		slug := noteSlug(raw)
		if slug == "" {
			return nil, fmt.Errorf("note: %q makes an empty name", raw)
		}
		_, hasText := opts["text"]
		_, hasProject := opts["project"]
		modes := 0
		for _, on := range []bool{OptBool(opts, "edit"), hasText, OptBool(opts, "delete")} {
			if on {
				modes++
			}
		}
		if modes > 1 {
			return nil, errors.New("note --edit, --text and --delete are exclusive")
		}
		cg, err := registryGraphWrite()
		if err != nil {
			return nil, err
		}
		existing, err := noteByName(cg, slug)
		if err != nil {
			return nil, err
		}
		var project *string
		if hasProject {
			p := OptString(opts, "project", "")
			project = &p
		}
		edit := func() (Value, error) {
			if ev.Caller() != CallerUser {
				if existing == nil {
					return nil, fmt.Errorf("no note %q — create it with (note %q {:text MD})", slug, slug)
				}
				return nil, errors.New("note --edit opens the user's editor; set the document with :text instead")
			}
			current := ""
			if existing != nil {
				current = renderNoteDoc(cg, existing)
			} else {
				pname := ""
				if project != nil {
					pname = *project
				} else if rp, ok := currentProject(); ok {
					pname = rp.Name
				}
				current = renderFrontMatter(slug, pname, nil) + "\n"
			}
			edited, err := editInEditor(ev, current)
			if err != nil {
				return nil, err
			}
			if edited == "" || edited == strings.TrimSpace(current) && existing == nil {
				return nil, errors.New("note unchanged: nothing written")
			}
			return saveNote(cg, ev, noteSave{existing: existing, slug: slug, doc: edited, project: project})
		}
		switch {
		case OptBool(opts, "delete"):
			if existing == nil {
				return nil, fmt.Errorf("no note %q", slug)
			}
			return true, cg.DeleteNode(context.Background(), existing.ID)
		case OptBool(opts, "edit") || existing == nil && !hasText && !hasProject:
			return edit()
		case hasText:
			return saveNote(cg, ev, noteSave{existing: existing, slug: slug, doc: OptString(opts, "text", ""), project: project})
		case hasProject:
			if existing == nil {
				return nil, fmt.Errorf("no note %q (create it with (note %q) or {:text MD})", slug, slug)
			}
			return saveNote(cg, ev, noteSave{existing: existing, slug: slug, doc: renderNoteDoc(cg, existing), project: project})
		}
		return streamFromReader(strings.NewReader(renderNoteDoc(cg, existing)), nil), nil
	}))

	Register("notes", "list notes as rows, by default scoped to the current project", CommandMeta{
		Command: true,
		Options: []Option{
			{Long: "all", Short: "a", Kind: OptionBool, Doc: "every project (adds a project column)"},
			{Long: "project", Short: "p", Kind: OptionString, Placeholder: "NAME", Doc: "the named project's notes"},
			{Long: "limit", Short: "n", Kind: OptionInt, Placeholder: "N", Doc: "most recently edited N notes"},
		}})
	env.Set("notes", BuiltinFunc(func(args []Value, env *Environment) (Value, error) {
		pos, opts, err := ParseOptions("notes", args)
		if err != nil {
			return nil, err
		}
		if len(pos) != 0 {
			return nil, errors.New("notes expects no arguments: (notes [{:all :project NAME :limit N}])")
		}
		cg := registryGraph()
		if cg == nil {
			return []Value{}, nil
		}
		q := models.NoteQuery{Limit: OptInt(opts, "limit", 0)}
		all := OptBool(opts, "all")
		if name := OptString(opts, "project", ""); name != "" {
			q.Project = name
		} else if !all {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			if name, _, ok := FindProject(cwd); ok {
				q.Project = name
			} else {
				all = true
			}
		}
		rows, err := cg.SearchNotes(q)
		if err != nil {
			return nil, err
		}
		result := make([]Value, 0, len(rows))
		for _, row := range rows {
			props := row.FormattedProperties()
			name, _ := props["name"].(string)
			body, _ := props["text"].(string)
			dict := Dictionary{
				"id":    Integer(row.ID),
				"name":  String(name),
				"title": String(noteTitle(body)),
				"age":   String(humanAge(time.Since(row.UpdatedAt))),
			}
			if all {
				dict["project"] = String(row.Project)
			}
			result = append(result, dict)
		}
		return result, nil
	}))
}
