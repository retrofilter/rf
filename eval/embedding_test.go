package eval

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/core"
	"github.com/retrofilter/rf/models/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type kwEncoder struct{}

func (kwEncoder) Encode(s string) ([]float32, error) {
	v := []float32{0.05, 0.05, 1e-3}
	low := strings.ToLower(s)
	if strings.Contains(low, "cat") || strings.Contains(low, "feline") {
		v[0] = 1
	}
	if strings.Contains(low, "finance") || strings.Contains(low, "revenue") {
		v[1] = 1
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range v {
		v[i] *= inv
	}
	return v, nil
}

func (k kwEncoder) EncodeMany(ss []string) ([][]float32, error) {
	out := make([][]float32, len(ss))
	for i, s := range ss {
		out[i], _ = k.Encode(s)
	}
	return out, nil
}

func TestNodesEmbeddingHybrid(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlx.Open("sqlite", "file:"+filepath.Join(dir, "test.db")+"?_pragma=foreign_keys(1)")
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, schema.CreateTables(db))
	gs := core.NewGraphStore(db)
	ev := NewEvaluatorWithEnvironment(db, gs)
	env := ev.globalEnv
	installFakeEncoder(t, kwEncoder{})

	mustEval := func(code string) Value {
		v, err := evalGraphExpr(code, ev, env)
		require.NoError(t, err, code)
		return v
	}

	// The arm is on by default; switch it off to pin the FTS-only mode.
	assert.Equal(t, true, mustEval(`graph-embeddings`))
	mustEval(`(set! graph-embeddings #f)`)

	mustEval(`(add-node {:type "note" :text "the cat sat on the mat"})`)
	mustEval(`(add-node {:type "note" :text "quarterly finance revenue report"})`)

	// FTS-only while off, including the prose fallback.
	rows := mustEval(`(nodes "cat?")`).([]Value)
	require.Len(t, rows, 1)

	// A pure vocabulary mismatch finds nothing lexically.
	rows = mustEval(`(nodes "feline")`).([]Value)
	assert.Empty(t, rows)

	mustEval(`(set! graph-embeddings #t)`)
	rows = mustEval(`(nodes "feline")`).([]Value)
	require.NotEmpty(t, rows)
	top := rows[0].(Dictionary)
	props := top["properties"].(Dictionary)
	assert.Contains(t, string(props["text"].(String)), "cat")
	assert.Equal(t, Number(1), top["score"], "hybrid scores normalize to top = 1.0")
	_, marked := top["_match"]
	assert.False(t, marked, "a semantic-only hit carries no highlight mark")

	rows = mustEval(`(nodes "finance")`).([]Value)
	require.NotEmpty(t, rows)
	assert.Equal(t, String("(?i)(finance)"), rows[0].(Dictionary)["_match"])

	matches, err := filepath.Glob(filepath.Join(dir, "cache", "embeddings", "*.emb"))
	require.NoError(t, err)
	assert.NotEmpty(t, matches, "search should have built the cache file beside the db")

	mustEval(`(add-node {:type "note" :text "another cat entirely"})`)
	rows = mustEval(`(nodes "feline")`).([]Value)
	require.GreaterOrEqual(t, len(rows), 2)
	for _, row := range rows[:2] {
		text := string(row.(Dictionary)["properties"].(Dictionary)["text"].(String))
		assert.Contains(t, text, "cat")
	}

	// recall goes through the same arm, type-filtered to memories.
	mustEval(`(remember "the feline slept all day")`)
	rows = mustEval(`(recall "cat")`).([]Value)
	require.NotEmpty(t, rows)
	assert.Contains(t, string(rows[0].(Dictionary)["text"].(String)), "feline")

	// Turning it back off returns to pure FTS.
	mustEval(`(set! graph-embeddings #f)`)
	rows = mustEval(`(nodes "feline" {:type "note"})`).([]Value)
	assert.Empty(t, rows)
}

func TestNodesEmbeddingMemoryDB(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()
	gs := core.NewGraphStore(db)
	ev := NewEvaluatorWithEnvironment(db, gs)
	env := ev.globalEnv
	installFakeEncoder(t, kwEncoder{})

	_, err := evalGraphExpr(`(add-node {:type "note" :text "the cat sat"})`, ev, env)
	require.NoError(t, err)
	v, err := evalGraphExpr(`(nodes "cat")`, ev, env)
	require.NoError(t, err)
	require.Len(t, v.([]Value), 1, "FTS fallback should still answer")
}
