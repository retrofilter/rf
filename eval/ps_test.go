package eval

import (
	"os"
	"testing"
)

const psFixture = "" +
	"    1   0.0  0.1    0 ??      10-03:21:44 /sbin/launchd\n" +
	"  337  12.5  3.2  501 ??      2-00:43:23  /Applications/Some App.app/Contents/MacOS/Some App\n" +
	"  512   0.0  0.1    0 ?       03:21       [kworker/0:1]\n" +
	"  700   0.3  0.2  501 ttys004 43:00       tmux new-session -A -s main\n" +
	"  900  12.5  1.0  501 ttys004 01:02:03    /usr/bin/tail -f log.txt\n" +
	"  950   0.1  0.2  501 ttys009 00:45       -zsh\n" +
	"garbage line without any numbers in it at all\n" +
	"\n"

func TestParsePS(t *testing.T) {
	procs := parsePS(psFixture)
	if len(procs) != 6 {
		t.Fatalf("expected 6 procs, got %d: %v", len(procs), procs)
	}
	// Detached tty spellings (?? BSD, ? GNU) normalize to "".
	if procs[0].tty != "" || procs[2].tty != "" {
		t.Errorf("?? and ? should parse as no tty: %v, %v", procs[0], procs[2])
	}
	if procs[3].tty != "ttys004" {
		t.Errorf("expected tty ttys004, got %v", procs[3])
	}
	if procs[1].name != "/Applications/Some App.app/Contents/MacOS/Some App" {
		t.Errorf("expected full path name, got %q", procs[1].name)
	}
	if procs[3].name != "tmux new-session -A -s main" {
		t.Errorf("expected arguments kept, got %q", procs[3].name)
	}
	// etime parses across its shapes: dd-hh:mm:ss, hh:mm:ss, mm:ss.
	if procs[0].elapsed != 10*86400+3*3600+21*60+44 {
		t.Errorf("dd-hh:mm:ss elapsed: got %d", procs[0].elapsed)
	}
	if procs[4].elapsed != 3723 {
		t.Errorf("hh:mm:ss elapsed: got %d", procs[4].elapsed)
	}
	if procs[5].elapsed != 45 {
		t.Errorf("mm:ss elapsed: got %d", procs[5].elapsed)
	}
}

func TestPSTime(t *testing.T) {
	cases := []struct {
		secs int64
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{105, "105s"},                // under 2m seconds still tell the story
		{8*60 + 30, "8m30s"},         // young minutes keep seconds
		{43*60 + 23, "43m"},          // older minutes drop them
		{3723, "1h2m"},               // young hours keep minutes
		{17*3600 + 40*60, "17h"},     // older hours drop them
		{2*86400 + 43*60 + 23, "2d"}, // days never show minutes
		{4*86400 + 20*3600, "4d20h"},
		{12*86400 + 5*3600, "12d"},
	}
	for _, c := range cases {
		if got := psTime(c.secs); got != c.want {
			t.Errorf("psTime(%d) = %q, want %q", c.secs, got, c.want)
		}
	}
	if _, ok := psElapsed("bogus"); ok {
		t.Error("psElapsed should reject non-etime text")
	}
	if _, ok := psElapsed("45"); ok {
		t.Error("psElapsed should reject a bare seconds field (etime is at least mm:ss)")
	}
}

func TestPSRows(t *testing.T) {
	procs := parsePS(psFixture)

	all := psRows(procs, func(psProc) bool { return true })
	if len(all) != 6 {
		t.Fatalf("expected 6 rows, got %d", len(all))
	}
	// Busiest first: the two 12.5% cpu rows lead, higher mem breaking the tie.
	if first := all[0].(Dictionary); first["pid"] != Integer(337) {
		t.Errorf("expected pid 337 first (cpu desc, mem desc), got %v", first)
	}
	if second := all[1].(Dictionary); second["pid"] != Integer(900) {
		t.Errorf("expected pid 900 second, got %v", second)
	}
	for _, r := range all {
		row := r.(Dictionary)
		for _, key := range []string{"pid", "name", "cpu", "mem", "time"} {
			if _, ok := row[key]; !ok {
				t.Errorf("row missing %s: %v", key, row)
			}
		}
		if _, ok := row["uid"]; ok {
			t.Errorf("uid/tty are filters, not columns: %v", row)
		}
	}
	// :time renders a human age, not the raw etime.
	if first := all[0].(Dictionary); first["time"] != String("2d") {
		t.Errorf("expected time 2d, got %v", first["time"])
	}

	// The :user tier — every process the uid owns, any terminal or none.
	user := psRows(procs, func(p psProc) bool { return p.uid == 501 })
	if len(user) != 4 {
		t.Errorf("expected 4 rows for uid 501, got %d: %v", len(user), user)
	}

	term := psRows(procs, func(p psProc) bool { return p.uid == 501 && p.tty != "" })
	if len(term) != 3 {
		t.Fatalf("expected 3 terminal rows for uid 501, got %d: %v", len(term), term)
	}
	for _, r := range term {
		pid := r.(Dictionary)["pid"]
		if pid != Integer(700) && pid != Integer(900) && pid != Integer(950) {
			t.Errorf("unexpected process in the terminal tier: %v", r)
		}
	}
}

func TestPSBuiltin(t *testing.T) {
	ev := NewEvaluator()
	got, err := evalExpr("(ps)", ev, ev.globalEnv)
	if err != nil {
		t.Fatalf("(ps): %v", err)
	}
	rows, ok := got.([]Value)
	if !ok || len(rows) == 0 {
		t.Fatalf("(ps) should return a non-empty list of rows, got %v", got)
	}
	self := Integer(os.Getpid())
	found := false
	for _, r := range rows {
		row, isDict := r.(Dictionary)
		if !isDict {
			t.Fatalf("(ps) row is not a dictionary: %v", r)
		}
		if row["pid"] == self {
			found = true
		}
	}
	if !found {
		t.Errorf("(ps) rows should include the test process (pid %d)", os.Getpid())
	}

	user, err := evalExpr("(ps :user)", ev, ev.globalEnv)
	if err != nil {
		t.Fatalf("(ps :user): %v", err)
	}
	all, err := evalExpr("(ps :all)", ev, ev.globalEnv)
	if err != nil {
		t.Fatalf("(ps :all): %v", err)
	}
	for name, v := range map[string]Value{"(ps :user)": user, "(ps :all)": all} {
		found := false
		for _, r := range v.([]Value) {
			if r.(Dictionary)["pid"] == self {
				found = true
			}
		}
		if !found {
			t.Errorf("%s rows should include the test process (pid %d)", name, os.Getpid())
		}
	}

	if _, err := evalExpr("(ps 1)", ev, ev.globalEnv); err == nil {
		t.Error("(ps 1) should error: options are :user and :all")
	}
	if _, err := evalExpr("(ps :aux)", ev, ev.globalEnv); err == nil {
		t.Error("(ps :aux) should error: options are :user and :all")
	}
}
