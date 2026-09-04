package eval

import (
	"errors"
	"testing"
	"time"
)

func TestInterruptAbortsRunawayLoop(t *testing.T) {
	ClearInterrupt()
	defer ClearInterrupt()

	ev := NewEvaluator()
	env := ev.globalEnv
	if _, err := evalExpr("(define (f) (f))", ev, env); err != nil {
		t.Fatalf("define: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := evalExpr("(f)", ev, env)
		done <- err
	}()
	time.AfterFunc(50*time.Millisecond, Interrupt)

	select {
	case err := <-done:
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("expected ErrInterrupted, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt did not abort the infinite loop")
	}
}

func TestInterruptStopsSleepMidWait(t *testing.T) {
	ClearInterrupt()
	defer ClearInterrupt()

	ev := NewEvaluator()
	start := time.Now()
	time.AfterFunc(50*time.Millisecond, Interrupt)
	_, err := evalExpr("(sleep 30)", ev, ev.globalEnv)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("sleep ran %v after interrupt — not polling the flag", elapsed)
	}
}

func TestClearInterruptRearmsEvaluation(t *testing.T) {
	defer ClearInterrupt()

	ev := NewEvaluator()
	Interrupt()
	if _, err := evalExpr("(+ 1 2)", ev, ev.globalEnv); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted while flag set, got %v", err)
	}
	ClearInterrupt()
	v, err := evalExpr("(+ 1 2)", ev, ev.globalEnv)
	if err != nil {
		t.Fatalf("eval after ClearInterrupt: %v", err)
	}
	if v != Integer(3) {
		t.Fatalf("expected 3, got %v", v)
	}
}

func TestInterruptStopsStreamDrain(t *testing.T) {
	ClearInterrupt()
	defer ClearInterrupt()

	ev := NewEvaluator()
	env := ev.globalEnv
	done := make(chan error, 1)
	go func() {
		_, err := evalExpr(`(sh "yes")`, ev, env)
		done <- err
	}()
	time.AfterFunc(50*time.Millisecond, Interrupt)

	select {
	case err := <-done:
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("expected ErrInterrupted, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt did not stop the stream drain")
	}
}

func TestInterruptContextCancels(t *testing.T) {
	ClearInterrupt()
	defer ClearInterrupt()

	ctx, stop := InterruptContext(t.Context())
	defer stop()
	select {
	case <-ctx.Done():
		t.Fatal("context cancelled before any interrupt")
	case <-time.After(2 * interruptPoll):
	}
	Interrupt()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("context did not cancel after Interrupt")
	}
}
