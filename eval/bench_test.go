package eval

import (
	"fmt"
	"testing"
)

func BenchmarkListPipeline(b *testing.B) {
	ev := NewEvaluator()
	env := ev.globalEnv
	rows := make([]Value, 10000)
	for i := range rows {
		rows[i] = Dictionary{
			"name": String(fmt.Sprintf("file-%d.txt", i)),
			"size": Number((i * 7919) % 100000),
		}
	}
	env.Set("rows", rows)
	exprs, err := ParseAll(`(length (map (lambda (r) (get "name" r)) (sort-by "size" (where "size" ">" 50000 rows))))`)
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	want, err := ev.EvalAll(exprs, env)
	if err != nil {
		b.Fatalf("eval: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := ev.EvalAll(exprs, env)
		if err != nil {
			b.Fatalf("eval: %v", err)
		}
		if !deepEqual(got, want) {
			b.Fatalf("pipeline result changed: got %v, want %v", got, want)
		}
	}
}
