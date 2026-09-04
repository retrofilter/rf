package eval

import (
	"strings"
	"testing"
	"time"
)

func TestJobLifecycle(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		got, err := evalSeq(t, ev,
			`(define j (job "echo hi"))`,
			`(job {:wait j})`)
		if err != nil {
			t.Fatal(err)
		}
		res, ok := got.(Dictionary)
		if !ok {
			t.Fatalf("job :wait: expected the :full dictionary, got %#v", got)
		}
		if res["stdout"] != String("hi") || res["exit"] != Integer(0) {
			t.Errorf("job :wait result: %v", res)
		}

		got, err = evalSeq(t, ev, `(job {:status (get "job" j)})`)
		if err != nil {
			t.Fatal(err)
		}
		status := got.(Dictionary)
		if status["running"] != false || status["exit"] != Integer(0) || status["state"] != String("done") {
			t.Errorf("status after exit: %v", status)
		}

		// A second wait returns the same captured result.
		if got, err = evalSeq(t, ev, `(job {:wait j})`); err != nil || got.(Dictionary)["stdout"] != String("hi") {
			t.Errorf("re-wait: %v %v", got, err)
		}

		if _, err := evalSeq(t, ev, `(define slow (job "sleep 30"))`); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if _, err := evalSeq(t, ev, `(job {:wait slow :timeout 0.2})`); err == nil || !strings.Contains(err.Error(), "still running") {
			t.Errorf("timed-out wait: %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Error("job :wait timeout did not return promptly")
		}
		got, err = evalSeq(t, ev, `(job {:status slow})`)
		if err != nil || got.(Dictionary)["running"] != true {
			t.Fatalf("job should survive a wait timeout: %v %v", got, err)
		}
		got, err = evalSeq(t, ev, `(job {:kill slow})`)
		if err != nil {
			t.Fatal(err)
		}
		if got.(Dictionary)["running"] != false {
			t.Errorf("job :kill should leave the job finished: %v", got)
		}

		// The registry lists both, ordered by id.
		rows := JobRows()
		if len(rows) < 2 {
			t.Fatalf("JobRows: %v", rows)
		}

		if _, err := evalSeq(t, ev, `(job {:status 999999})`); err == nil || !strings.Contains(err.Error(), "no job") {
			t.Errorf("unknown job: %v", err)
		}
		if _, err := evalSeq(t, ev, `(job {:status 1 :kill 1})`); err == nil || !strings.Contains(err.Error(), "one of") {
			t.Errorf("combined modes: %v", err)
		}
		if _, err := evalSeq(t, ev, `(job "echo x" {:kill 1})`); err == nil || !strings.Contains(err.Error(), "takes no command") {
			t.Errorf("mode with command: %v", err)
		}
	})
}

func TestJobScheduled(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		got, err := evalSeq(t, ev, `(define s (job "echo later" {:in "150ms"}))`, `(job {:status s})`)
		if err != nil {
			t.Fatal(err)
		}
		status := got.(Dictionary)
		if status["state"] != String("scheduled") || status["running"] != true {
			t.Errorf("pending schedule: %v", status)
		}
		if _, ok := status["next"]; !ok {
			t.Errorf("scheduled status should carry :next: %v", status)
		}
		got, err = evalSeq(t, ev, `(job {:wait s})`)
		if err != nil || got.(Dictionary)["stdout"] != String("later") {
			t.Fatalf("scheduled job result: %v %v", got, err)
		}

		// Killed before the fire time: canceled, and a wait errors.
		if _, err := evalSeq(t, ev, `(define c (job "echo never" {:in "1h"}))`); err != nil {
			t.Fatal(err)
		}
		got, err = evalSeq(t, ev, `(job {:kill c})`)
		if err != nil || got.(Dictionary)["state"] != String("canceled") {
			t.Fatalf("killed schedule: %v %v", got, err)
		}
		if _, err := evalSeq(t, ev, `(job {:wait c})`); err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Errorf("wait on canceled schedule: %v", err)
		}

		for raw, want := range map[string]time.Duration{
			"1d": 24 * time.Hour, "2w": 14 * 24 * time.Hour,
			"minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour, "week": 7 * 24 * time.Hour,
			"90s": 90 * time.Second, "1h30m": 90 * time.Minute,
		} {
			if d, err := ParseSpan("job", "in", raw); err != nil || d != want {
				t.Errorf("ParseSpan(%q) = %v %v, want %v", raw, d, err, want)
			}
		}
		if _, err := ParseSpan("job", "in", "soonish"); err == nil {
			t.Error("junk span should error")
		}
	})
}

func TestJobRecurring(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		if _, err := evalSeq(t, ev, `(define r (job "echo tick" {:in "50ms" :every "1s"}))`); err != nil {
			t.Fatal(err)
		}
		got, err := evalSeq(t, ev, `(job {:wait r})`)
		if err != nil || got.(Dictionary)["stdout"] != String("tick") {
			t.Fatalf("first recurring run: %v %v", got, err)
		}
		got, err = evalSeq(t, ev, `(job {:status r})`)
		if err != nil {
			t.Fatal(err)
		}
		status := got.(Dictionary)
		if status["every"] != String("1s") || status["running"] != true {
			t.Errorf("recurring status: %v", status)
		}
		if runs, ok := status["runs"].(Integer); !ok || runs < 1 {
			t.Errorf("recurring status runs: %v", status)
		}
		got, err = evalSeq(t, ev, `(job {:kill r})`)
		if err != nil || got.(Dictionary)["running"] != false {
			t.Fatalf("kill recurring: %v %v", got, err)
		}

		if _, err := evalSeq(t, ev, `(job "echo x" {:every "10ms"})`); err == nil || !strings.Contains(err.Error(), "at least 1s") {
			t.Errorf("interval floor: %v", err)
		}
	})
}

func TestJobOutput(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		if _, err := evalSeq(t, ev, `(define j (job "echo peek-marker && echo err-marker 1>&2 && sleep 30"))`); err != nil {
			t.Fatal(err)
		}
		// The running job's output appears without joining it.
		deadline := time.Now().Add(5 * time.Second)
		for {
			got, err := evalSeq(t, ev, `(lines (job {:output j}))`)
			if err != nil {
				t.Fatal(err)
			}
			if list, ok := got.([]Value); ok && len(list) > 0 {
				if list[0] != String("peek-marker") {
					t.Fatalf("peek: %v", got)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("no output surfaced from the running job")
			}
			time.Sleep(50 * time.Millisecond)
		}
		got, err := evalSeq(t, ev, `(job {:status j})`)
		if err != nil || got.(Dictionary)["running"] != true {
			t.Fatalf("peek must not join the job: %v %v", got, err)
		}
		if got, err = evalSeq(t, ev, `(lines (job {:output j :stderr #t}))`); err != nil {
			t.Fatal(err)
		}
		if list := got.([]Value); len(list) != 1 || list[0] != String("err-marker") {
			t.Errorf("stderr peek: %v", got)
		}
		if _, err := evalSeq(t, ev, `(job {:kill j})`); err != nil {
			t.Fatal(err)
		}
		// The buffers survive the job; :stderr pairs only with :output.
		if got, err = evalSeq(t, ev, `(lines (job {:output j}))`); err != nil || got.([]Value)[0] != String("peek-marker") {
			t.Errorf("peek after exit: %v %v", got, err)
		}
		if _, err := evalSeq(t, ev, `(job {:wait j :stderr #t})`); err == nil || !strings.Contains(err.Error(), ":output") {
			t.Errorf("stderr outside output: %v", err)
		}

		// A scheduled job that hasn't fired has no output yet.
		if _, err := evalSeq(t, ev, `(define s (job "echo never" {:in "1h"}))`); err != nil {
			t.Fatal(err)
		}
		if _, err := evalSeq(t, ev, `(job {:output s})`); err == nil || !strings.Contains(err.Error(), "not started") {
			t.Errorf("peek before start: %v", err)
		}
		if _, err := evalSeq(t, ev, `(job {:kill s})`); err != nil {
			t.Fatal(err)
		}
	})
}

func TestJobGated(t *testing.T) {
	withTempDir(t, func(dir string, ev *Evaluator, env *Environment) {
		var asked []string
		ev.SetApprover(func(action string) bool {
			asked = append(asked, action)
			return false
		})
		if _, err := evalSeq(t, ev, `(job "echo nope")`); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("denied job should error, got %v", err)
		}
		if _, err := evalSeq(t, ev, `(job "echo nope" {:every "day"})`); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("denied recurring job should error, got %v", err)
		}
		if len(asked) != 2 || !strings.Contains(asked[0], `job "echo nope"`) || !strings.Contains(asked[1], `--every day`) {
			t.Errorf("approver saw %v", asked)
		}
	})
}

func TestParseAt(t *testing.T) {
	loc := time.FixedZone("test", 3600)
	now := time.Date(2026, 3, 1, 10, 30, 0, 0, loc)
	at, err := ParseAt("session", "11:00", now)
	if err != nil || !at.Equal(time.Date(2026, 3, 1, 11, 0, 0, 0, loc)) {
		t.Errorf("11:00 → %v %v, want today 11:00", at, err)
	}
	at, err = ParseAt("session", "09:00", now)
	if err != nil || !at.Equal(time.Date(2026, 3, 2, 9, 0, 0, 0, loc)) {
		t.Errorf("09:00 → %v %v, want tomorrow 09:00", at, err)
	}
	at, err = ParseAt("session", "2026-03-05 08:15", now)
	if err != nil || !at.Equal(time.Date(2026, 3, 5, 8, 15, 0, 0, loc)) {
		t.Errorf("absolute → %v %v", at, err)
	}
	at, err = ParseAt("session", "2026-03-05T08:15", now)
	if err != nil || !at.Equal(time.Date(2026, 3, 5, 8, 15, 0, 0, loc)) {
		t.Errorf("T-joined → %v %v", at, err)
	}
	at, err = ParseAt("session", "2026-03-05", now)
	if err != nil || !at.Equal(time.Date(2026, 3, 5, 0, 0, 0, 0, loc)) {
		t.Errorf("date only → %v %v", at, err)
	}
	if _, err := ParseAt("session", "2026-02-01 08:15", now); err == nil || !strings.Contains(err.Error(), "past") {
		t.Errorf("past absolute should error: %v", err)
	}
	if _, err := ParseAt("session", "noonish", now); err == nil || !strings.Contains(err.Error(), "--at expects") {
		t.Errorf("junk should error: %v", err)
	}
}
