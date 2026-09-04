package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models"
)

const scheduleTick = 20 * time.Second

// Schedule is one scheduled session as stored in the graph.
type Schedule struct {
	ID      uint32
	Name    string        // the session name (attach-or-create key)
	Dir     string        // working directory; "" spawns in $HOME
	Command string        // typed into the fresh shell; "" opens a plain shell
	Every   string        // recurring cadence as spelled ("day", "4h"); "" = one-shot
	Period  time.Duration // Every parsed; 0 for one-shot
	Next    time.Time     // pending fire time
	Last    time.Time     // last fire; zero before the first
}

const scheduleNodeType = "schedule"

func scheduleFromNode(node *models.Node) (Schedule, bool) {
	props := node.FormattedProperties()
	str := func(key string) string {
		s, _ := props[key].(string)
		return s
	}
	sc := Schedule{ID: node.ID, Name: str("name"), Dir: str("dir"), Command: str("command"), Every: str("every")}
	if sc.Name == "" || !nameRe.MatchString(sc.Name) {
		return sc, false
	}
	next, err := time.Parse(time.RFC3339, str("next"))
	if err != nil {
		return sc, false
	}
	sc.Next = next
	if last, err := time.Parse(time.RFC3339, str("last")); err == nil {
		sc.Last = last
	}
	if p, err := time.ParseDuration(str("period")); err == nil && p > 0 {
		sc.Period = p
	}
	return sc, true
}

func (sc Schedule) props() map[string]interface{} {
	props := map[string]interface{}{
		"type":    scheduleNodeType,
		"name":    sc.Name,
		"dir":     sc.Dir,
		"command": sc.Command,
		"every":   sc.Every,
		"period":  "",
		"next":    sc.Next.Format(time.RFC3339),
		"last":    "",
	}
	if sc.Period > 0 {
		props["period"] = sc.Period.String()
	}
	if !sc.Last.IsZero() {
		props["last"] = sc.Last.Format(time.RFC3339)
	}
	return props
}

// ListSchedules reads every well-formed schedule node, soonest first.
func ListSchedules(cg *core.Graph) ([]Schedule, error) {
	nodes, err := cg.NodesByType(scheduleNodeType, 0)
	if err != nil {
		return nil, err
	}
	var out []Schedule
	for _, node := range nodes {
		if sc, ok := scheduleFromNode(node); ok {
			out = append(out, sc)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Next.Before(out[j].Next) })
	return out, nil
}

// FindSchedule looks a schedule up by name; nil when absent.
func FindSchedule(cg *core.Graph, name string) (*Schedule, error) {
	nodes, err := cg.GetNodesByAttributes(context.Background(), map[string]interface{}{"type": scheduleNodeType, "name": name})
	if err != nil {
		return nil, err
	}
	for _, node := range nodes {
		if sc, ok := scheduleFromNode(node); ok {
			return &sc, nil
		}
	}
	return nil, nil
}

// PutSchedule writes a schedule, replacing any existing one of the same
// name — re-running `session review --every day` re-times it rather
// than doubling it. Returns the node id.
func PutSchedule(cg *core.Graph, sc Schedule) (uint32, error) {
	if !nameRe.MatchString(sc.Name) {
		return 0, errors.New("session name must match [a-zA-Z0-9_-]+")
	}
	if sc.Next.IsZero() {
		return 0, errors.New("schedule needs a next run")
	}
	ctx := context.Background()
	existing, err := cg.GetNodesByAttributes(ctx, map[string]interface{}{"type": scheduleNodeType, "name": sc.Name})
	if err != nil {
		return 0, err
	}
	propsJSON, err := json.Marshal(sc.props())
	if err != nil {
		return 0, err
	}
	nodeType := scheduleNodeType
	if len(existing) > 0 {
		node := existing[0]
		node.Properties = (*json.RawMessage)(&propsJSON)
		if _, err := cg.UpdateNode(ctx, node); err != nil {
			return 0, err
		}
		for _, dup := range existing[1:] {
			_ = cg.DeleteNode(ctx, dup.ID)
		}
		return node.ID, nil
	}
	return cg.InsertNode(ctx, &models.Node{GraphID: cg.ID, Type: &nodeType, Properties: (*json.RawMessage)(&propsJSON)})
}

// DeleteSchedule removes the schedule named name; false when there was
// none.
func DeleteSchedule(cg *core.Graph, name string) (bool, error) {
	ctx := context.Background()
	nodes, err := cg.GetNodesByAttributes(ctx, map[string]interface{}{"type": scheduleNodeType, "name": name})
	if err != nil {
		return false, err
	}
	for _, node := range nodes {
		if err := cg.DeleteNode(ctx, node.ID); err != nil {
			return false, err
		}
	}
	return len(nodes) > 0, nil
}

func (sc Schedule) advance(now time.Time) time.Time {
	next := sc.Next
	if sc.Period <= 0 {
		return next
	}
	for !next.After(now) {
		next = next.Add(sc.Period)
	}
	return next
}

func (s *Server) scheduleGraph() *core.Graph {
	if s.store == nil {
		return nil
	}
	cg, err := s.store.GetGraph(overviewGraphName)
	if err != nil {
		return nil
	}
	return cg
}

func (s *Server) tickSchedules(now time.Time) []string {
	cg := s.scheduleGraph()
	if cg == nil {
		return nil
	}
	schedules, err := ListSchedules(cg)
	if err != nil {
		return nil
	}
	var fired []string
	for _, sc := range schedules {
		if sc.Next.After(now) {
			continue
		}
		if name, err := s.spawnNumbered(sc); err == nil {
			fired = append(fired, name)
		}
		if sc.Period <= 0 {
			_ = cg.DeleteNode(context.Background(), sc.ID)
			continue
		}
		sc.Last = now
		sc.Next = sc.advance(now)
		_, _ = PutSchedule(cg, sc)
	}
	if len(fired) > 0 {
		s.reg.Invalidate()
	}
	return fired
}

func (s *Server) spawnNumbered(sc Schedule) (string, error) {
	name := sc.Name
	for n := 2; ; n++ {
		_, err := s.mgr.Spawn(name, sc.Dir, sc.Command)
		if !errors.Is(err, ErrNameTaken) {
			return name, err
		}
		name = fmt.Sprintf("%s-%d", sc.Name, n)
	}
}

func (s *Server) scheduleLoop() {
	for now := range time.Tick(scheduleTick) {
		s.tickSchedules(now)
	}
}

func (s *Server) scheduledRows() []Session {
	cg := s.scheduleGraph()
	if cg == nil {
		return nil
	}
	schedules, err := ListSchedules(cg)
	if err != nil {
		return nil
	}
	rows := make([]Session, 0, len(schedules))
	for _, sc := range schedules {
		rows = append(rows, Session{
			ID:      fmt.Sprintf("sched-%d", sc.ID),
			Name:    sc.Name,
			Dir:     sc.Dir,
			Status:  StatusScheduled,
			Command: sc.Command,
			Every:   sc.Every,
			Next:    sc.Next.Unix(),
		})
	}
	return rows
}

func fmtUntil(at, now time.Time) string {
	d := at.Sub(now)
	unit := func(n int64, word string) string {
		if n == 1 {
			return fmt.Sprintf("In 1 %s", word)
		}
		return fmt.Sprintf("In %d %ss", n, word)
	}
	switch {
	case d <= 0:
		return "Now"
	case d < time.Minute:
		return unit(int64((d+time.Second-1)/time.Second), "second")
	case d < time.Hour:
		return unit(int64(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return unit(int64(d/time.Hour), "hour")
	}
	return unit(int64(d/(24*time.Hour)), "day")
}

func scheduledDetail(s Session) string {
	if strings.TrimSpace(s.Command) == "" {
		return "shell"
	}
	return s.Command
}
