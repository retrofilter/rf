package core

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/models"
	"github.com/retrofilter/rf/models/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type hashEncoder struct {
	dims     int
	synonyms map[string]string
}

func (h *hashEncoder) Encode(s string) ([]float32, error) {
	vec := make([]float32, h.dims)
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.Trim(w, ".,!?\"'")
		if canon, ok := h.synonyms[w]; ok {
			w = canon
		}
		hs := fnv.New64a()
		hs.Write([]byte(w))
		rng := rand.New(rand.NewSource(int64(hs.Sum64())))
		for i := range vec {
			vec[i] += float32(rng.NormFloat64())
		}
	}
	var norm float64
	for _, x := range vec {
		norm += float64(x) * float64(x)
	}
	if norm > 0 {
		inv := float32(1 / math.Sqrt(norm))
		for i := range vec {
			vec[i] *= inv
		}
	}
	return vec, nil
}

func (h *hashEncoder) EncodeMany(ss []string) ([][]float32, error) {
	out := make([][]float32, len(ss))
	for i, s := range ss {
		v, err := h.Encode(s)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func embTestStore(t *testing.T) (*GraphStore, *Graph, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlx.Open("sqlite", "file:"+filepath.Join(dir, "test.db")+"?_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, schema.CreateTables(db))
	gs := NewGraphStore(db)
	_, err = gs.CreateGraph("g")
	require.NoError(t, err)
	g, err := gs.GetGraph("g")
	require.NoError(t, err)
	return gs, g, filepath.Join(dir, "cache", "embeddings")
}

func embConfig(enc EmbeddingEncoder) EmbeddingConfig {
	return EmbeddingConfig{
		Enabled: true,
		Model:   "FAKE128",
		Encoder: func() (EmbeddingEncoder, error) { return enc, nil },
	}
}

func addTextNode(t *testing.T, g *Graph, typ, text string) uint32 {
	t.Helper()
	props, err := json.Marshal(map[string]string{"text": text})
	require.NoError(t, err)
	raw := json.RawMessage(props)
	var tp *string
	if typ != "" {
		tp = &typ
	}
	id, err := g.InsertNode(context.Background(), &models.Node{GraphID: g.ID, Type: tp, Properties: &raw})
	require.NoError(t, err)
	return id
}

var embCorpus = []string{
	"the cat sat on the mat purring softly",
	"a kitten chased the yarn across the kitchen",
	"quarterly finance report shows revenue growth",
	"the stock market closed higher on tech gains",
	"recipe for sourdough bread with rye flour",
	"baking a loaf needs patient fermentation",
	"dogs bark at the postman every morning",
	"astronomy news: a comet visible at dawn",
}

func TestEmbeddingSearchRanks(t *testing.T) {
	gs, g, cacheDir := embTestStore(t)
	gs.Embeddings().Configure(embConfig(&hashEncoder{dims: 128}))
	for _, text := range embCorpus {
		addTextNode(t, g, "", text)
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(cacheDir)
		assert.Empty(t, entries, "ingest must not build the cache")
	}

	nodes, scores, err := gs.Embeddings().SearchNodes(g.ID, "cat kitten purring", "", 3)
	require.NoError(t, err)
	require.Len(t, nodes, 3)
	for i := 1; i < len(scores); i++ {
		assert.LessOrEqual(t, scores[i], scores[i-1], "scores must be descending")
	}
	top := nodes[0].SearchText() + " " + nodes[1].SearchText()
	assert.Contains(t, top, "cat")
	assert.Contains(t, top, "kitten")

	if _, err := os.Stat(filepath.Join(cacheDir, "g1-FAKE128.emb")); err != nil {
		t.Fatalf("expected cache file after first search: %v", err)
	}
}

func TestEmbeddingTypeFilter(t *testing.T) {
	gs, g, _ := embTestStore(t)
	gs.Embeddings().Configure(embConfig(&hashEncoder{dims: 128}))
	addTextNode(t, g, "memory", "the cat sat on the mat")
	addTextNode(t, g, "task", "feed the cat before noon")
	addTextNode(t, g, "memory", "finance report for april")

	nodes, _, err := gs.Embeddings().SearchNodes(g.ID, "cat", "memory", 5)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)
	for _, n := range nodes {
		require.NotNil(t, n.Type)
		assert.Equal(t, "memory", *n.Type)
	}
}

func TestEmbeddingRefresh(t *testing.T) {
	gs, g, _ := embTestStore(t)
	gs.Embeddings().Configure(embConfig(&hashEncoder{dims: 128}))
	first := addTextNode(t, g, "", "the cat sat on the mat")
	_, _, err := gs.Embeddings().SearchNodes(g.ID, "cat", "", 1)
	require.NoError(t, err)

	addTextNode(t, g, "", "a comet blazed across the winter sky")
	nodes, _, err := gs.Embeddings().SearchNodes(g.ID, "comet sky", "", 1)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Contains(t, nodes[0].SearchText(), "comet")

	node, err := g.GetNode(context.Background(), first)
	require.NoError(t, err)
	props := json.RawMessage(`{"text": "submarine sonar array maintenance"}`)
	node.Properties = &props
	_, err = g.UpdateNode(context.Background(), node)
	require.NoError(t, err)
	nodes, _, err = gs.Embeddings().SearchNodes(g.ID, "submarine sonar", "", 1)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, first, nodes[0].ID)

	require.NoError(t, g.DeleteNode(context.Background(), first))
	nodes, _, err = gs.Embeddings().SearchNodes(g.ID, "submarine sonar", "", 5)
	require.NoError(t, err)
	for _, n := range nodes {
		assert.NotEqual(t, first, n.ID)
	}
}

func TestEmbeddingSharedFile(t *testing.T) {
	gs, g, _ := embTestStore(t)
	enc := &hashEncoder{dims: 128}
	gs.Embeddings().Configure(embConfig(enc))
	for _, text := range embCorpus {
		addTextNode(t, g, "", text)
	}
	n, err := gs.Embeddings().Reconcile(g.ID)
	require.NoError(t, err)
	assert.Equal(t, len(embCorpus), n)

	gs2 := NewGraphStore(gs.DB())
	gs2.Embeddings().Configure(embConfig(enc))
	n2, err := gs2.Embeddings().Reconcile(g.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, n2, "second process must reuse the shared file")

	nodes, _, err := gs2.Embeddings().SearchNodes(g.ID, "sourdough bread", "", 2)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)
	assert.Contains(t, nodes[0].SearchText(), "sourdough")
}

func TestEmbeddingCorruption(t *testing.T) {
	gs, g, cacheDir := embTestStore(t)
	enc := &hashEncoder{dims: 128}
	gs.Embeddings().Configure(embConfig(enc))
	for _, text := range embCorpus {
		addTextNode(t, g, "", text)
	}
	_, err := gs.Embeddings().Reconcile(g.ID)
	require.NoError(t, err)
	path := filepath.Join(cacheDir, "g1-FAKE128.emb")

	// Flip a byte inside record 2's code.
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	require.NoError(t, err)
	rs := embRecordSize((128 + 63) / 64)
	_, err = f.WriteAt([]byte{0xFF}, int64(embHeaderSize+2*rs+3))
	require.NoError(t, err)
	// And append half a record with no header update — a torn write.
	fi, err := f.Stat()
	require.NoError(t, err)
	_, err = f.WriteAt(make([]byte, rs/2), fi.Size())
	require.NoError(t, err)
	require.NoError(t, f.Close())

	gs2 := NewGraphStore(gs.DB())
	gs2.Embeddings().Configure(embConfig(enc))
	n, err := gs2.Embeddings().Reconcile(g.ID)
	require.NoError(t, err)
	assert.Greater(t, n, 0, "truncated tail should re-encode")
	nodes, _, err := gs2.Embeddings().SearchNodes(g.ID, "finance revenue market", "", 2)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)
	assert.Contains(t, nodes[0].SearchText(), "finance")

	// A wrecked header rebuilds from nothing.
	require.NoError(t, os.WriteFile(path, []byte("garbage"), 0o600))
	gs3 := NewGraphStore(gs.DB())
	gs3.Embeddings().Configure(embConfig(enc))
	nodes, _, err = gs3.Embeddings().SearchNodes(g.ID, "comet dawn", "", 1)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)
	assert.Contains(t, nodes[0].SearchText(), "comet")
}

func TestEmbeddingCompaction(t *testing.T) {
	gs, g, cacheDir := embTestStore(t)
	enc := &hashEncoder{dims: 128}
	gs.Embeddings().compactMin = 4
	gs.Embeddings().Configure(embConfig(enc))
	const n = 30
	ids := make([]uint32, n)
	for i := range ids {
		ids[i] = addTextNode(t, g, "", "document number "+string(rune('a'+i%26)))
	}
	_, err := gs.Embeddings().Reconcile(g.ID)
	require.NoError(t, err)

	for _, id := range ids {
		node, err := g.GetNode(context.Background(), id)
		require.NoError(t, err)
		props := json.RawMessage(`{"text": "revised content for the record"}`)
		node.Properties = &props
		_, err = g.UpdateNode(context.Background(), node)
		require.NoError(t, err)
	}
	_, err = gs.Embeddings().Reconcile(g.ID)
	require.NoError(t, err)

	fi, err := os.Stat(filepath.Join(cacheDir, "g1-FAKE128.emb"))
	require.NoError(t, err)
	rs := embRecordSize((128 + 63) / 64)
	assert.Equal(t, int64(embHeaderSize+n*rs), fi.Size(), "compaction should leave exactly one record per node")

	nodes, _, err := gs.Embeddings().SearchNodes(g.ID, "revised content", "", 1)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)
}

func TestEmbeddingExpiry(t *testing.T) {
	gs, g, _ := embTestStore(t)
	gs.Embeddings().ttl = 50 * time.Millisecond
	gs.Embeddings().Configure(embConfig(&hashEncoder{dims: 128}))
	addTextNode(t, g, "", "the cat sat on the mat")
	_, _, err := gs.Embeddings().SearchNodes(g.ID, "cat", "", 1)
	require.NoError(t, err)

	gs.Embeddings().mu.Lock()
	n := len(gs.Embeddings().caches)
	gs.Embeddings().mu.Unlock()
	assert.Equal(t, 1, n)

	require.Eventually(t, func() bool {
		gs.Embeddings().mu.Lock()
		defer gs.Embeddings().mu.Unlock()
		return len(gs.Embeddings().caches) == 0
	}, 2*time.Second, 10*time.Millisecond, "idle mapping should expire")

	// And come straight back on the next search.
	nodes, _, err := gs.Embeddings().SearchNodes(g.ID, "cat", "", 1)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
}

func TestEmbeddingModelSwitch(t *testing.T) {
	gs, g, cacheDir := embTestStore(t)
	gs.Embeddings().Configure(embConfig(&hashEncoder{dims: 128}))
	for _, text := range embCorpus {
		addTextNode(t, g, "", text)
	}
	_, _, err := gs.Embeddings().SearchNodes(g.ID, "cat", "", 1)
	require.NoError(t, err)

	cfg := embConfig(&hashEncoder{dims: 64})
	cfg.Model = "FAKE64"
	gs.Embeddings().Configure(cfg)
	nodes, _, err := gs.Embeddings().SearchNodes(g.ID, "kitten yarn", "", 1)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)
	assert.Contains(t, nodes[0].SearchText(), "kitten")

	for _, name := range []string{"g1-FAKE128.emb", "g1-FAKE64.emb"} {
		if _, err := os.Stat(filepath.Join(cacheDir, name)); err != nil {
			t.Fatalf("expected cache file %s: %v", name, err)
		}
	}
}

func TestEmbeddingMemoryDB(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, schema.CreateTables(db))
	gs := NewGraphStore(db)
	_, err = gs.CreateGraph("g")
	require.NoError(t, err)
	g, err := gs.GetGraph("g")
	require.NoError(t, err)
	gs.Embeddings().Configure(embConfig(&hashEncoder{dims: 128}))
	addTextNode(t, g, "", "the cat sat on the mat")
	_, _, err = gs.Embeddings().SearchNodes(g.ID, "cat", "", 1)
	require.Error(t, err)
}

func TestHybridSearchNodes(t *testing.T) {
	gs, g, _ := embTestStore(t)
	enc := &hashEncoder{dims: 128, synonyms: map[string]string{"auto": "car"}}
	gs.Embeddings().Configure(embConfig(enc))
	addTextNode(t, g, "", "car loan refinancing rates dropped this month")
	addTextNode(t, g, "", "the cat sat on the mat")
	addTextNode(t, g, "", "zorblax frobnicator manual")

	nodes, scores, err := g.HybridSearchNodes("auto financing loan", "", 3)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)
	assert.Contains(t, nodes[0].SearchText(), "car")
	assert.Equal(t, 1.0, scores[0], "scores normalize to top row = 1.0")

	nodes, _, err = g.HybridSearchNodes("zorblax", "", 3)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)
	assert.Contains(t, nodes[0].SearchText(), "zorblax")
}

func TestEmbeddingPreTriggerCorpus(t *testing.T) {
	gs, g, _ := embTestStore(t)
	for _, text := range embCorpus {
		addTextNode(t, g, "", text)
	}
	// Simulate rows that predate the triggers: erase the change counter.
	_, err := gs.DB().Exec(`DELETE FROM node_version`)
	require.NoError(t, err)
	v, err := models.NodeVersion(gs.DB(), g.ID)
	require.NoError(t, err)
	require.Equal(t, int64(0), v)

	gs.Embeddings().Configure(embConfig(&hashEncoder{dims: 128}))
	nodes, _, err := gs.Embeddings().SearchNodes(g.ID, "sourdough bread", "", 2)
	require.NoError(t, err)
	require.NotEmpty(t, nodes, "version-0 corpus must still backfill")
	assert.Contains(t, nodes[0].SearchText(), "sourdough")

	nodes, _, err = gs.Embeddings().SearchNodes(g.ID, "blasdflad zzqx", "", 3)
	require.NoError(t, err)
	assert.NotEmpty(t, nodes, "gibberish with a nonzero embedding still ranks the corpus")
}

func TestEmbeddingSweep(t *testing.T) {
	gs, g, cacheDir := embTestStore(t)
	enc := &hashEncoder{dims: 128}
	gs.Embeddings().Configure(embConfig(enc))
	addTextNode(t, g, "", "the cat sat on the mat")
	_, err := gs.Embeddings().Reconcile(g.ID)
	require.NoError(t, err)

	// A second graph builds a cache, then gets deleted.
	_, err = gs.CreateGraph("doomed")
	require.NoError(t, err)
	doomed, err := gs.GetGraph("doomed")
	require.NoError(t, err)
	addTextNode(t, doomed, "", "some doomed content here")
	_, err = gs.Embeddings().Reconcile(doomed.ID)
	require.NoError(t, err)
	doomedFile := filepath.Join(cacheDir, fmt.Sprintf("g%d-FAKE128.emb", doomed.ID))
	require.FileExists(t, doomedFile)
	require.NoError(t, gs.DeleteGraph("doomed"))

	// Plant an old-model file past the stale age and a fresh one within it.
	oldFile := filepath.Join(cacheDir, "g1-ANCIENT.emb")
	require.NoError(t, os.WriteFile(oldFile, []byte("x"), 0o600))
	require.NoError(t, os.Chtimes(oldFile, time.Now().Add(-8*24*time.Hour), time.Now().Add(-8*24*time.Hour)))
	freshFile := filepath.Join(cacheDir, "g1-RECENT.emb")
	require.NoError(t, os.WriteFile(freshFile, []byte("x"), 0o600))

	// A fresh store (fresh process) sweeps on first reconcile.
	gs2 := NewGraphStore(gs.DB())
	gs2.Embeddings().Configure(embConfig(enc))
	_, err = gs2.Embeddings().Reconcile(g.ID)
	require.NoError(t, err)

	assert.NoFileExists(t, doomedFile, "deleted graph's cache must be swept")
	assert.NoFileExists(t, oldFile, "stale-model cache must be swept")
	assert.FileExists(t, freshFile, "recent other-model cache survives")
	assert.FileExists(t, filepath.Join(cacheDir, "g1-FAKE128.emb"), "live cache survives")
}

func TestEmbeddingConcurrentInstances(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "test.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	openDB := func() *sqlx.DB {
		db, err := sqlx.Open("sqlite", dsn)
		require.NoError(t, err)
		t.Cleanup(func() { db.Close() })
		return db
	}
	enc := &hashEncoder{dims: 128}
	instance := func(db *sqlx.DB) (*GraphStore, *Graph) {
		gs := NewGraphStore(db)
		gs.Embeddings().Configure(embConfig(enc))
		g, err := gs.GetGraph("g")
		require.NoError(t, err)
		return gs, g
	}

	setup := openDB()
	require.NoError(t, schema.CreateTables(setup))
	gs0 := NewGraphStore(setup)
	_, err := gs0.CreateGraph("g")
	require.NoError(t, err)

	const writers = 3
	const docsPerWriter = 30
	var wg sync.WaitGroup
	errs := make(chan error, writers+2)
	done := make(chan struct{})

	for w := 0; w < writers; w++ {
		db := openDB()
		wg.Add(1)
		go func(w int, db *sqlx.DB) {
			defer wg.Done()
			_, g := instance(db)
			var mine []uint32
			for j := 0; j < docsPerWriter; j++ {
				text := fmt.Sprintf("wildcat mountain story writer%d doc%d", w, j)
				props, _ := json.Marshal(map[string]string{"text": text})
				raw := json.RawMessage(props)
				id, err := g.InsertNode(context.Background(), &models.Node{GraphID: g.ID, Properties: &raw})
				if err != nil {
					errs <- fmt.Errorf("writer %d insert: %w", w, err)
					return
				}
				mine = append(mine, id)
				if j == docsPerWriter/2 {
					node, err := g.GetNode(context.Background(), mine[0])
					if err != nil {
						errs <- fmt.Errorf("writer %d fetch: %w", w, err)
						return
					}
					rev := json.RawMessage(fmt.Sprintf(`{"text": "revised wildcat chronicle writer%d"}`, w))
					node.Properties = &rev
					if _, err := g.UpdateNode(context.Background(), node); err != nil {
						errs <- fmt.Errorf("writer %d update: %w", w, err)
						return
					}
				}
			}
		}(w, db)
	}

	for r := 0; r < 2; r++ {
		db := openDB()
		wg.Add(1)
		go func(r int, db *sqlx.DB) {
			defer wg.Done()
			gs, g := instance(db)
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, _, err := gs.Embeddings().SearchNodes(g.ID, "wildcat mountain", "", 5); err != nil {
					errs <- fmt.Errorf("reader %d: %w", r, err)
					return
				}
				_, _, err := g.HybridSearchNodes("mountain story", "", 5)
				if err != nil {
					errs <- fmt.Errorf("reader %d hybrid: %w", r, err)
					return
				}
			}
		}(r, db)
	}

	// Wait for the writers, then release the readers.
	writersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(writersDone)
	}()
	go func() {
		db := openDB()
		for {
			var n int
			if err := db.Get(&n, `SELECT COUNT(*) FROM node`); err == nil && n >= writers*docsPerWriter {
				close(done)
				return
			}
			select {
			case <-writersDone:
				close(done)
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	<-writersDone
	<-done
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	gsFresh, gFresh := instance(openDB())
	_, err = gsFresh.Embeddings().Reconcile(gFresh.ID)
	require.NoError(t, err)
	n, err := gsFresh.Embeddings().Reconcile(gFresh.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "quiesced cache must be fully encoded")

	nodes, _, err := gsFresh.Embeddings().SearchNodes(gFresh.ID, "revised wildcat chronicle", "", writers)
	require.NoError(t, err)
	require.Len(t, nodes, writers)
	for _, node := range nodes {
		assert.Contains(t, node.SearchText(), "revised")
	}

	gsFresh.Embeddings().mu.Lock()
	cache := gsFresh.Embeddings().caches[fmt.Sprintf("g%d", gFresh.ID)]
	live := cache.hdr.count - cache.deadCount
	gsFresh.Embeddings().mu.Unlock()
	assert.Equal(t, writers*docsPerWriter, live, "one live record per node after the storm")
}

func TestEmbeddingHeap(t *testing.T) {
	h := distHeap{cap: 3}
	for i, d := range []int32{9, 4, 7, 1, 8, 2, 6} {
		if d < h.worst() {
			h.push(uint32(i), d)
		}
	}
	require.Len(t, h.c, 3)
	got := map[int32]bool{}
	for _, c := range h.c {
		got[c.dist] = true
	}
	assert.True(t, got[1] && got[2] && got[4], "expected the three smallest distances, got %v", h.c)
}

func TestQuantize(t *testing.T) {
	vec := make([]float32, 70)
	vec[0], vec[63], vec[64], vec[69] = 1, 1, -1, 0.5
	words := quantize(vec)
	require.Len(t, words, 2)
	assert.Equal(t, uint64(1)|uint64(1)<<63, words[0])
	assert.Equal(t, uint64(1)<<5, words[1])
}

func BenchmarkCacheScan(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	const n = 100_000
	wpd := 8
	rs := embRecordSize(wpd)
	c := &embCache{
		hdr:  embHeader{dims: 512, count: n},
		wpd:  wpd,
		mm:   make([]byte, embHeaderSize+n*rs),
		dead: make([]bool, n),
	}
	rng.Read(c.mm[embHeaderSize:])
	q := make([]uint64, wpd)
	for j := range q {
		q[j] = rng.Uint64()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scanCache(c, q, nil, 500)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/doc")
}
