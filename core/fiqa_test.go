package core

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/models"
	"github.com/retrofilter/rf/models/schema"
	"github.com/tphakala/simd/f32"
	potion "github.com/trengrj/go-potion"
	_ "modernc.org/sqlite"
)

func TestFiQAEmbeddings(t *testing.T) {
	if os.Getenv("RF_FIQA") != "1" {
		t.Skip("set RF_FIQA=1 to run the FiQA embedding benchmark")
	}
	model := potion.RETRIEVAL32M
	if name := os.Getenv("RF_FIQA_MODEL"); name != "" {
		model = potion.Model(strings.ToUpper(name))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	p, err := potion.New(ctx, model)
	if err != nil {
		t.Fatal(err)
	}
	docs, queries := loadFiQA(t)
	t.Logf("corpus %d docs, %d test queries, %s %d dims", len(docs), len(queries), model, p.Dimensions())

	dbPath := filepath.Join(t.TempDir(), "fiqa.db")
	db, err := sqlx.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := schema.CreateTables(db); err != nil {
		t.Fatal(err)
	}
	gs := NewGraphStore(db)
	if _, err := gs.CreateGraph("fiqa"); err != nil {
		t.Fatal(err)
	}
	g, err := gs.GetGraph("fiqa")
	if err != nil {
		t.Fatal(err)
	}

	nodes := make([]*models.Node, len(docs))
	for i, d := range docs {
		props, _ := json.Marshal(map[string]string{"text": d.text})
		raw := json.RawMessage(props)
		nodes[i] = &models.Node{GraphID: g.ID, Properties: &raw}
	}
	start := time.Now()
	if err := models.CreateNodesBatch(db, nodes); err != nil {
		t.Fatal(err)
	}
	nodeIngest := time.Since(start)
	t.Logf("bulk node ingest (nodes + FTS triggers, one tx): %s (%.0f docs/s)",
		nodeIngest.Round(time.Millisecond), float64(len(docs))/nodeIngest.Seconds())

	// Map node ids back to fiqa doc ids (batch insert preserves order).
	var ids []uint32
	if err := db.Select(&ids, `SELECT id FROM node WHERE graph_id = ? ORDER BY id`, g.ID); err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(docs) {
		t.Fatalf("expected %d node ids, got %d", len(docs), len(ids))
	}
	idOf := make(map[uint32]string, len(ids))
	for i, id := range ids {
		idOf[id] = docs[i].id
	}

	emb := gs.Embeddings()
	cfg := EmbeddingConfig{
		Enabled: true,
		Model:   string(model),
		Encoder: func() (EmbeddingEncoder, error) { return p, nil },
	}
	emb.Configure(cfg)
	start = time.Now()
	encoded, err := emb.Reconcile(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	build := time.Since(start)
	cachePath := filepath.Join(filepath.Dir(dbPath), "cache", "embeddings", "g"+strconv.Itoa(int(g.ID))+"-"+string(model)+".emb")
	fi, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cache build (encode + append): %d docs in %s (%.0f docs/s, %s/doc); file %.1f MB",
		encoded, build.Round(time.Millisecond), float64(encoded)/build.Seconds(),
		(build / time.Duration(max(encoded, 1))).Round(time.Microsecond),
		float64(fi.Size())/(1<<20))

	gs2 := NewGraphStore(db)
	gs2.Embeddings().Configure(cfg)
	start = time.Now()
	encoded2, err := gs2.Embeddings().Reconcile(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("second process opens shared cache: %s, %d docs re-encoded", time.Since(start).Round(time.Millisecond), encoded2)

	texts := make([]string, len(docs))
	for i, d := range docs {
		texts[i] = d.text
	}
	start = time.Now()
	docVecs, err := p.EncodeMany(texts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("exact-truth corpus encode: %s", time.Since(start).Round(time.Millisecond))
	qvecs := make([][]float32, len(queries))
	truth := make([][]uint32, len(queries))
	scores := make([]float32, len(docs))
	for i, q := range queries {
		v, err := p.Encode(q.text)
		if err != nil {
			t.Fatal(err)
		}
		qvecs[i] = v
		f32.DotProductBatch(scores, docVecs, v)
		truth[i] = topIdx(scores, ids, 10)
	}
	t.Logf("exact nDCG@10 (brute-force float): %.4f", fiqaNDCG(queries, truth, idOf))

	emb.mu.Lock()
	cache := emb.caches[fmt.Sprintf("g%d", g.ID)]
	emb.mu.Unlock()
	if cache == nil {
		t.Fatal("cache not mapped after reconcile")
	}
	for _, d := range []int{100, 250, 500, 1000} {
		var recall float64
		start = time.Now()
		for i := range queries {
			cands := scanCache(cache, quantize(qvecs[i]), nil, d)
			in := make(map[uint32]bool, len(cands))
			for _, c := range cands {
				in[c] = true
			}
			hits := 0
			for _, id := range truth[i] {
				if in[id] {
					hits++
				}
			}
			recall += float64(hits) / float64(len(truth[i]))
		}
		perQ := time.Since(start) / time.Duration(len(queries))
		t.Logf("candidate recall@10, D=%-5d %.3f  (scan %s/query)", d, recall/float64(len(queries)), perQ.Round(time.Microsecond))
	}

	nq := min(100, len(queries))
	var tProbe, tEncQ, tScan, tFetch, tRescore time.Duration
	for i := 0; i < nq; i++ {
		s0 := time.Now()
		if _, err := models.NodeVersion(db, g.ID); err != nil {
			t.Fatal(err)
		}
		tProbe += time.Since(s0)
		s0 = time.Now()
		qv, err := p.Encode(queries[i].text)
		if err != nil {
			t.Fatal(err)
		}
		tEncQ += time.Since(s0)
		s0 = time.Now()
		cands := scanCache(cache, quantize(qv), nil, 500)
		tScan += time.Since(s0)
		s0 = time.Now()
		byID, err := models.GetNodesByIDs(db, cands)
		if err != nil {
			t.Fatal(err)
		}
		tFetch += time.Since(s0)
		s0 = time.Now()
		cTexts := make([]string, 0, len(cands))
		for _, id := range cands {
			if n, ok := byID[id]; ok {
				cTexts = append(cTexts, n.SearchText())
			}
		}
		vecs, err := p.EncodeMany(cTexts)
		if err != nil {
			t.Fatal(err)
		}
		sc := make([]float32, len(vecs))
		f32.DotProductBatch(sc, vecs, qvecs[i])
		tRescore += time.Since(s0)
	}
	per := func(x time.Duration) time.Duration { return (x / time.Duration(nq)).Round(time.Microsecond) }
	t.Logf("embedding arm stages (avg over %d queries, D=500): probe %s + encode-query %s + scan %s + fetch %s + rescore %s",
		nq, per(tProbe), per(tEncQ), per(tScan), per(tFetch), per(tRescore))

	// ── The three retrieval modes, end to end with qrels.
	run := func(name string, search func(q string) []uint32) {
		var recall float64
		ranked := make([][]uint32, len(queries))
		start := time.Now()
		for i, q := range queries {
			got := search(q.text)
			ranked[i] = got
			in := make(map[uint32]bool, len(got))
			for _, id := range got {
				in[id] = true
			}
			hits := 0
			for _, id := range truth[i] {
				if in[id] {
					hits++
				}
			}
			recall += float64(hits) / float64(len(truth[i]))
		}
		perQ := time.Since(start) / time.Duration(len(queries))
		t.Logf("%-28s nDCG@10 %.4f  recall@10-vs-exact %.3f  %s/query",
			name, fiqaNDCG(queries, ranked, idOf), recall/float64(len(queries)), perQ.Round(time.Microsecond))
	}

	run("FTS/BM25 arm alone:", func(q string) []uint32 {
		ns, _, err := models.SearchNodesLexical(db, g.ID, q, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		return nodeIDs(ns)
	})
	run("embedding arm alone:", func(q string) []uint32 {
		ns, _, err := emb.SearchNodes(g.ID, q, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		return nodeIDs(ns)
	})
	run("hybrid (RRF fused):", func(q string) []uint32 {
		ns, _, err := g.HybridSearchNodes(q, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		return nodeIDs(ns)
	})

	n := min(500, len(docs))
	start = time.Now()
	for _, d := range docs[:n] {
		props, _ := json.Marshal(map[string]string{"text": d.text})
		raw := json.RawMessage(props)
		if _, err := g.InsertNode(context.Background(), &models.Node{GraphID: g.ID, Properties: &raw}); err != nil {
			t.Fatal(err)
		}
	}
	single := time.Since(start)
	start = time.Now()
	delta, err := emb.Reconcile(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("single-doc ingest (per-doc tx, no embedding work): %s/doc; next-search reconcile encoded %d docs in %s",
		(single / time.Duration(n)).Round(time.Microsecond), delta, time.Since(start).Round(time.Millisecond))
}

func nodeIDs(ns []*models.Node) []uint32 {
	out := make([]uint32, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}

func topIdx(scores []float32, ids []uint32, k int) []uint32 {
	type sc struct {
		i int
		s float32
	}
	top := make([]sc, 0, k+1)
	for i, s := range scores {
		if len(top) == k && s <= top[len(top)-1].s {
			continue
		}
		pos := sort.Search(len(top), func(j int) bool { return top[j].s < s })
		top = append(top, sc{})
		copy(top[pos+1:], top[pos:])
		top[pos] = sc{i, s}
		if len(top) > k {
			top = top[:k]
		}
	}
	out := make([]uint32, len(top))
	for i, t := range top {
		out[i] = ids[t.i]
	}
	return out
}

type fiqaDoc struct{ id, text string }

type fiqaQuery struct {
	text string
	rels map[string]float64
}

func fiqaNDCG(queries []fiqaQuery, ranked [][]uint32, idOf map[uint32]string) float64 {
	var sum float64
	for i, q := range queries {
		var dcg float64
		for pos, key := range ranked[i] {
			if pos >= 10 {
				break
			}
			if g, ok := q.rels[idOf[key]]; ok {
				dcg += g / math.Log2(float64(pos+2))
			}
		}
		grades := make([]float64, 0, len(q.rels))
		for _, g := range q.rels {
			grades = append(grades, g)
		}
		sort.Sort(sort.Reverse(sort.Float64Slice(grades)))
		var idcg float64
		for pos, g := range grades {
			if pos >= 10 {
				break
			}
			idcg += g / math.Log2(float64(pos+2))
		}
		if idcg > 0 {
			sum += dcg / idcg
		}
	}
	return sum / float64(len(queries))
}

func loadFiQA(t *testing.T) ([]fiqaDoc, []fiqaQuery) {
	t.Helper()
	path := filepath.Join("testdata", "fiqa.zip")
	if _, err := os.Stat(path); err != nil {
		const url = "https://public.ukp.informatik.tu-darmstadt.de/thakur/BEIR/datasets/fiqa.zip"
		t.Logf("downloading %s ...", url)
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("download: %s", resp.Status)
		}
		tmp := path + ".part"
		f, err := os.Create(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(f, resp.Body); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}
	zf, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zf.Close()

	open := func(suffix string) io.ReadCloser {
		for _, f := range zf.File {
			if strings.HasSuffix(f.Name, suffix) {
				r, err := f.Open()
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
		}
		t.Fatalf("%s not in %s", suffix, path)
		return nil
	}
	lines := func(suffix string, fn func(line string)) {
		r := open(suffix)
		defer r.Close()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			if s := strings.TrimSpace(sc.Text()); s != "" {
				fn(s)
			}
		}
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
	}

	var docs []fiqaDoc
	lines("fiqa/corpus.jsonl", func(line string) {
		var raw struct {
			ID    string `json:"_id"`
			Title string `json:"title"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, fiqaDoc{id: raw.ID, text: strings.TrimSpace(raw.Title + " " + raw.Text)})
	})

	rels := map[string]map[string]float64{}
	first := true
	lines("fiqa/qrels/test.tsv", func(line string) {
		if first {
			first = false
			return
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Fatalf("bad qrel line %q", line)
		}
		score, err := strconv.ParseFloat(parts[2], 64)
		if err != nil {
			t.Fatal(err)
		}
		if rels[parts[0]] == nil {
			rels[parts[0]] = map[string]float64{}
		}
		rels[parts[0]][parts[1]] = score
	})

	var queries []fiqaQuery
	lines("fiqa/queries.jsonl", func(line string) {
		var raw struct {
			ID   string `json:"_id"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatal(err)
		}
		if r, ok := rels[raw.ID]; ok {
			queries = append(queries, fiqaQuery{text: raw.Text, rels: r})
		}
	})
	if len(docs) == 0 || len(queries) == 0 {
		t.Fatalf("empty dataset: %d docs, %d queries", len(docs), len(queries))
	}
	return docs, queries
}
