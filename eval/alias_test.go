package eval

import (
	"strings"
	"testing"
)

func TestAliasBuiltins(t *testing.T) {
	ev := NewEvaluator()
	env := ev.globalEnv

	// define and look up: the name is a bare symbol (special form)
	if _, err := evalExpr(`(alias ll "ls -la")`, ev, env); err != nil {
		t.Fatalf("alias: %v", err)
	}
	if exp, ok := ev.LookupAlias("ll"); !ok || exp != "ls -la" {
		t.Errorf("LookupAlias(ll) = %q, %v; want \"ls -la\", true", exp, ok)
	}

	if _, err := evalExpr(`(alias "ll" "ls :all")`, ev, env); err != nil {
		t.Fatalf("alias redefine: %v", err)
	}
	if exp, _ := ev.LookupAlias("ll"); exp != "ls :all" {
		t.Errorf("redefined LookupAlias(ll) = %q, want \"ls :all\"", exp)
	}

	// aliases lists sorted rows
	if _, err := evalExpr(`(alias e "nvim")`, ev, env); err != nil {
		t.Fatalf("alias: %v", err)
	}
	got, err := evalExpr(`(aliases)`, ev, env)
	if err != nil {
		t.Fatalf("aliases: %v", err)
	}
	rows, ok := got.([]Value)
	if !ok || len(rows) != 2 {
		t.Fatalf("(aliases) = %v, want 2 rows", got)
	}
	first, ok := rows[0].(Dictionary)
	if !ok || first["name"] != String("e") || first["expansion"] != String("nvim") {
		t.Errorf("first alias row = %v, want e -> nvim", rows[0])
	}

	// unalias removes (bare symbol, unevaluated); unknown names error
	if _, err := evalExpr(`(unalias e)`, ev, env); err != nil {
		t.Fatalf("unalias: %v", err)
	}
	if _, ok := ev.LookupAlias("e"); ok {
		t.Error("e should be gone after unalias")
	}
	if _, err := evalExpr(`(unalias e)`, ev, env); err == nil {
		t.Error("unalias of unknown name should error")
	}

	// validation: names must be plain command words, expansions command text
	for _, expr := range []string{
		`(alias "" "x")`,
		`(alias "a b" "x")`,
		`(alias "a|b" "x")`,
		`(alias 5 "x")`,
		`(alias x "")`,
		`(alias x "(ls)")`,
		`(alias x "!ls")`,
		`(alias x)`,
	} {
		if _, err := evalExpr(expr, ev, env); err == nil {
			t.Errorf("%s should error", expr)
		}
	}

	// alias/unalias are user-only; aliases (the listing) is not
	ev.SetCaller(CallerAssistant)
	if _, err := evalExpr(`(alias x "ls")`, ev, env); err == nil || !strings.Contains(err.Error(), "only be run by the user") {
		t.Errorf("assistant (alias ...) error = %v, want RequireUser rejection", err)
	}
	if _, err := evalExpr(`(unalias ll)`, ev, env); err == nil || !strings.Contains(err.Error(), "only be run by the user") {
		t.Errorf("assistant (unalias ...) error = %v, want RequireUser rejection", err)
	}
	if _, err := evalExpr(`(aliases)`, ev, env); err != nil {
		t.Errorf("assistant (aliases) should be allowed, got %v", err)
	}
	ev.SetCaller(CallerUser)
}
