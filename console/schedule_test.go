package console

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScheduleStoreUpsertsByName(t *testing.T) {
	_, _, cg := storeServer(t)
	next := time.Now().Add(time.Hour).Truncate(time.Second)
	id, err := PutSchedule(cg, Schedule{Name: "review", Dir: "/tmp", Command: "claude /review", Every: "day", Period: 24 * time.Hour, Next: next})
	require.NoError(t, err)

	later := next.Add(2 * time.Hour)
	id2, err := PutSchedule(cg, Schedule{Name: "review", Dir: "/tmp", Command: "claude /review", Next: later})
	require.NoError(t, err)
	require.Equal(t, id, id2)

	list, err := ListSchedules(cg)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "review", list[0].Name)
	require.True(t, list[0].Next.Equal(later))
	require.Empty(t, list[0].Every, "re-filing without --every makes it one-shot")
	require.Zero(t, list[0].Period)

	found, err := FindSchedule(cg, "review")
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Equal(t, "claude /review", found.Command)

	// Names must be session-shaped; a blank next is refused.
	_, err = PutSchedule(cg, Schedule{Name: "bad name", Next: next})
	require.ErrorContains(t, err, "must match")
	_, err = PutSchedule(cg, Schedule{Name: "ok"})
	require.ErrorContains(t, err, "next run")

	ok, err := DeleteSchedule(cg, "review")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = DeleteSchedule(cg, "review")
	require.NoError(t, err)
	require.False(t, ok, "second delete finds nothing")
	list, err = ListSchedules(cg)
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestTickFiresDueSchedules(t *testing.T) {
	s, _, cg := storeServer(t)
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)

	// Due recurring, due one-shot, and one still an hour out.
	_, err := PutSchedule(cg, Schedule{Name: "review", Dir: dir, Command: "claude /review", Every: "day", Period: 24 * time.Hour, Next: now.Add(-30 * time.Hour)})
	require.NoError(t, err)
	_, err = PutSchedule(cg, Schedule{Name: "once", Dir: dir, Command: "make backup", Next: now.Add(-time.Minute)})
	require.NoError(t, err)
	_, err = PutSchedule(cg, Schedule{Name: "later", Dir: dir, Next: now.Add(time.Hour)})
	require.NoError(t, err)

	fired := s.tickSchedules(now)
	require.ElementsMatch(t, []string{"review", "once"}, fired)
	require.NotEmpty(t, s.mgr.FindByName("review"))
	require.NotEmpty(t, s.mgr.FindByName("once"))
	require.Empty(t, s.mgr.FindByName("later"))

	// The spawned session got its command (cat echoes the typed line).
	ls := s.mgr.Get(s.mgr.FindByName("review"))
	require.Eventually(t, func() bool {
		replay, _, cancel := ls.Subscribe()
		cancel()
		return strings.Contains(string(replay), "claude /review")
	}, 3*time.Second, 25*time.Millisecond)

	list, err := ListSchedules(cg)
	require.NoError(t, err)
	names := map[string]Schedule{}
	for _, sc := range list {
		names[sc.Name] = sc
	}
	require.NotContains(t, names, "once", "a fired one-shot is spent")
	review := names["review"]
	require.True(t, review.Next.Equal(now.Add(18*time.Hour)), "next %v", review.Next)
	require.True(t, review.Last.Equal(now))
	require.Equal(t, "day", review.Every)

	_, err = PutSchedule(cg, Schedule{Name: "review", Dir: dir, Command: "claude /review", Every: "day", Period: 24 * time.Hour, Next: now, Last: now})
	require.NoError(t, err)
	fired = s.tickSchedules(now.Add(time.Second))
	require.Equal(t, []string{"review-2"}, fired, "a name still up gets a numbered run")
	require.Len(t, s.mgr.List(), 3)
	require.NotEmpty(t, s.mgr.FindByName("review-2"))
	found, err := FindSchedule(cg, "review")
	require.NoError(t, err)
	require.True(t, found.Next.After(now.Add(time.Second)))
}

func TestScheduleAdvance(t *testing.T) {
	base := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	sc := Schedule{Next: base, Period: 24 * time.Hour}
	// Fired a minute late: tomorrow 09:00, not 09:01.
	require.Equal(t, base.Add(24*time.Hour), sc.advance(base.Add(time.Minute)))
	// A week down: the next 09:00 after now, still on the grid.
	require.Equal(t, base.Add(8*24*time.Hour), sc.advance(base.Add(7*24*time.Hour+time.Minute)))
	// One-shot never advances.
	require.Equal(t, base, Schedule{Next: base}.advance(base.Add(time.Hour)))
}

func TestFmtUntil(t *testing.T) {
	now := time.Now()
	require.Equal(t, "Now", fmtUntil(now.Add(-time.Hour), now))
	require.Equal(t, "Now", fmtUntil(now, now))
	require.Equal(t, "In 1 second", fmtUntil(now.Add(time.Second), now))
	require.Equal(t, "In 30 seconds", fmtUntil(now.Add(30*time.Second-500*time.Millisecond), now))
	require.Equal(t, "In 59 seconds", fmtUntil(now.Add(59*time.Second), now))
	require.Equal(t, "In 1 minute", fmtUntil(now.Add(90*time.Second), now))
	require.Equal(t, "In 5 minutes", fmtUntil(now.Add(5*time.Minute+10*time.Second), now))
	require.Equal(t, "In 16 hours", fmtUntil(now.Add(16*time.Hour+20*time.Minute), now))
	require.Equal(t, "In 40 hours", fmtUntil(now.Add(40*time.Hour), now))
	require.Equal(t, "In 3 days", fmtUntil(now.Add(3*24*time.Hour+time.Hour), now))
}

func TestScheduledRowsOnRailNotTabs(t *testing.T) {
	s, ts, cg := storeServer(t)
	now := time.Now()
	_, err := PutSchedule(cg, Schedule{Name: "review", Dir: "/home/j/src/x", Command: `claude "/review"`, Every: "day", Period: 24 * time.Hour, Next: now.Add(16*time.Hour + 5*time.Minute)})
	require.NoError(t, err)

	m, err := s.model("", viewTerminal, false)
	require.NoError(t, err)
	require.Empty(t, m.Sessions)
	require.Len(t, m.Scheduled, 1)
	require.Equal(t, StatusScheduled, m.Scheduled[0].Status)

	rail := render(t, railRows(m))
	require.Contains(t, rail, "In 16 hours")
	require.Contains(t, rail, "SCHEDULED")
	require.Contains(t, rail, `claude &#34;/review&#34;`)
	require.Contains(t, rail, "every day")
	require.Contains(t, rail, "~/src/x")
	require.Contains(t, rail, `data-schedule-id="sched-`)
	require.NotContains(t, rail, "data-session-id")

	tabs := render(t, tabItems(m))
	require.NotContains(t, tabs, "review")
	require.Equal(t, 0, m.runningCount())
	require.Contains(t, pollEvents(m), "rf:showoverview")
	require.NotContains(t, pollEvents(m), "rf:select")

	res, err := http.Get(ts.URL + "/api/sessions?token=" + s.cfg.Token)
	require.NoError(t, err)
	defer res.Body.Close()
	var rows []Session
	require.NoError(t, json.NewDecoder(res.Body).Decode(&rows))
	require.Len(t, rows, 1)
	require.Equal(t, "review", rows[0].Name)
	require.Equal(t, StatusScheduled, rows[0].Status)
	require.Equal(t, "day", rows[0].Every)
	require.Equal(t, `claude "/review"`, rows[0].Command)
	require.NotZero(t, rows[0].Next)
}
