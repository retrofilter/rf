package core

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math/bits"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/retrofilter/rf/logger"
	"github.com/retrofilter/rf/models"
	"github.com/tphakala/simd/f32"
	"golang.org/x/sys/unix"
)

// EmbeddingEncoder is the slice of the embedding model the cache needs.
// eval wires go-potion's cached encoder in; tests substitute fakes.
type EmbeddingEncoder interface {
	Encode(sentence string) ([]float32, error)
	EncodeMany(sentences []string) ([][]float32, error)
}

// EmbeddingConfig is pushed in from Scheme bindings (config as data).
type EmbeddingConfig struct {
	Enabled bool   // the graph-embeddings binding
	Model   string // embedding-model: names the per-graph cache files
	Encoder func() (EmbeddingEncoder, error)
}

const (
	rescoreDepth  = 500
	cacheTTL      = 5 * time.Minute
	encodeBatch   = 1024
	scanShardMin  = 65536
	staleModelAge = 7 * 24 * time.Hour

	embMagic         = "RFQE" // retrofilter quantized embeddings
	embFormatVersion = 1
	embHeaderSize    = 64
)

var embCRC = crc32.MakeTable(crc32.Castagnoli)

type embDoc struct {
	id    uint32
	stamp int64
	text  string
}

type embSource interface {
	key() string                               // cache-file stem, e.g. "g3"
	version() (int64, error)                   // trigger-bumped change counter
	changed(watermark int64) ([]embDoc, error) // docs at/past the watermark
	count() (int, error)                       // corpus size
	allIDs() ([]uint32, error)                 // for stray/delete reconciliation
	byID(ids []uint32) (map[uint32]embDoc, error)
}

type graphSource struct {
	db      *sqlx.DB
	graphID uint32
}

func (g graphSource) key() string { return fmt.Sprintf("g%d", g.graphID) }

func (g graphSource) version() (int64, error) { return models.NodeVersion(g.db, g.graphID) }

func (g graphSource) changed(watermark int64) ([]embDoc, error) {
	nodes, err := models.NodesUpdatedSince(g.db, g.graphID, time.Unix(0, watermark).UTC())
	if err != nil {
		return nil, err
	}
	docs := make([]embDoc, len(nodes))
	for i, n := range nodes {
		docs[i] = embDoc{id: n.ID, stamp: n.UpdatedAt.UnixNano(), text: n.SearchText()}
	}
	return docs, nil
}

func (g graphSource) count() (int, error) { return models.CountNodesInGraph(g.db, g.graphID) }

func (g graphSource) allIDs() ([]uint32, error) { return models.NodeIDs(g.db, g.graphID) }

func (g graphSource) byID(ids []uint32) (map[uint32]embDoc, error) {
	nodes, err := models.GetNodesByIDs(g.db, ids)
	if err != nil {
		return nil, err
	}
	docs := make(map[uint32]embDoc, len(nodes))
	for id, n := range nodes {
		docs[id] = embDoc{id: id, stamp: n.UpdatedAt.UnixNano(), text: n.SearchText()}
	}
	return docs, nil
}

type embHeader struct {
	dims      int
	version   int64
	count     int
	watermark int64
}

func (h embHeader) encode() []byte {
	b := make([]byte, embHeaderSize)
	copy(b, embMagic)
	binary.LittleEndian.PutUint32(b[4:], embFormatVersion)
	binary.LittleEndian.PutUint32(b[8:], uint32(h.dims))
	binary.LittleEndian.PutUint64(b[16:], uint64(h.version))
	binary.LittleEndian.PutUint64(b[24:], uint64(h.count))
	binary.LittleEndian.PutUint64(b[32:], uint64(h.watermark))
	binary.LittleEndian.PutUint32(b[40:], crc32.Checksum(b[:40], embCRC))
	return b
}

func decodeHeader(b []byte) (embHeader, error) {
	if len(b) < embHeaderSize || string(b[:4]) != embMagic {
		return embHeader{}, errors.New("not an embedding cache file")
	}
	if v := binary.LittleEndian.Uint32(b[4:]); v != embFormatVersion {
		return embHeader{}, fmt.Errorf("embedding cache format v%d (want v%d)", v, embFormatVersion)
	}
	if crc32.Checksum(b[:40], embCRC) != binary.LittleEndian.Uint32(b[40:]) {
		return embHeader{}, errors.New("embedding cache header CRC mismatch")
	}
	return embHeader{
		dims:      int(binary.LittleEndian.Uint32(b[8:])),
		version:   int64(binary.LittleEndian.Uint64(b[16:])),
		count:     int(binary.LittleEndian.Uint64(b[24:])),
		watermark: int64(binary.LittleEndian.Uint64(b[32:])),
	}, nil
}

func embRecordSize(wpd int) int { return wpd*8 + 16 }

type embCache struct {
	f         *os.File
	mm        []byte // header + count records
	hdr       embHeader
	wpd       int
	verified  int    // records CRC-checked so far
	dead      []bool // tombstones: superseded or deleted records
	deadCount int
	version   int64 // change counter reconciled to, in-process
	lastUse   time.Time
}

func (c *embCache) record(i int) []byte {
	rs := embRecordSize(c.wpd)
	off := embHeaderSize + i*rs
	return c.mm[off : off+rs]
}

func (c *embCache) recordID(i int) uint32 {
	r := c.record(i)
	return binary.LittleEndian.Uint32(r[c.wpd*8+8:])
}

func (c *embCache) recordStamp(i int) int64 {
	r := c.record(i)
	return int64(binary.LittleEndian.Uint64(r[c.wpd*8:]))
}

func (c *embCache) close() {
	if c.mm != nil {
		unix.Munmap(c.mm)
		c.mm = nil
	}
	if c.f != nil {
		c.f.Close()
		c.f = nil
	}
}

// Embeddings is the per-GraphStore embedding cache: config, mappings, expiry.
type Embeddings struct {
	db         *sqlx.DB
	mu         sync.Mutex
	cfg        EmbeddingConfig
	dir        string // cache dir, resolved lazily from the db's own path
	dirErr     error
	dirOnce    bool
	swept      bool // stranded-file sweep, once per process
	caches     map[string]*embCache
	janitor    bool
	ttl        time.Duration // cacheTTL; tests shorten it
	compactMin int           // tombstone floor before compaction; tests lower it
}

func newEmbeddings(db *sqlx.DB) *Embeddings {
	return &Embeddings{db: db, caches: map[string]*embCache{}, ttl: cacheTTL, compactMin: 1024}
}

// Configure replaces the config. A model switch drops the mappings — each
// model has its own cache files.
func (s *Embeddings) Configure(cfg EmbeddingConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.Model != s.cfg.Model {
		s.dropAll()
	}
	s.cfg = cfg
}

// Enabled reports whether the graph arm is on.
func (s *Embeddings) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Enabled
}

func (s *Embeddings) dropAll() {
	for key, c := range s.caches {
		c.close()
		delete(s.caches, key)
	}
}

func (s *Embeddings) cacheDir() (string, error) {
	if s.dirOnce {
		return s.dir, s.dirErr
	}
	s.dirOnce = true
	var dbFile string
	if err := s.db.Get(&dbFile, `SELECT file FROM pragma_database_list WHERE name = 'main'`); err != nil {
		s.dirErr = err
		return "", s.dirErr
	}
	if dbFile == "" {
		s.dirErr = errors.New("in-memory database has no embedding cache directory")
		return "", s.dirErr
	}
	s.dir = filepath.Join(filepath.Dir(dbFile), "cache", "embeddings")
	s.dirErr = os.MkdirAll(s.dir, 0o700)
	return s.dir, s.dirErr
}

func (s *Embeddings) sweep(dir, model string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var graphIDs []uint32
	if err := s.db.Select(&graphIDs, `SELECT id FROM graph`); err != nil {
		return
	}
	live := make(map[string]struct{}, len(graphIDs))
	for _, id := range graphIDs {
		live["g"+strconv.FormatUint(uint64(id), 10)] = struct{}{}
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".emb") {
			continue
		}
		stem, fileModel, ok := strings.Cut(strings.TrimSuffix(name, ".emb"), "-")
		if !ok {
			continue // not ours to judge
		}
		remove := false
		if _, alive := live[stem]; !alive {
			remove = true
		} else if fileModel != model {
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > staleModelAge {
				remove = true
			}
		}
		if remove {
			path := filepath.Join(dir, name)
			logger.Debug().Str("file", path).Msg("removing stranded embedding cache file")
			os.Remove(path)
			os.Remove(path + ".lock")
		}
	}
}

// Reconcile brings the graph's cache file up to date with the node table —
// encoding new and changed nodes, tombstoning deletes — and returns how many
// documents it encoded.
func (s *Embeddings) Reconcile(graphID uint32) (int, error) {
	src := graphSource{db: s.db, graphID: graphID}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.cfg
	if !cfg.Enabled {
		return 0, errors.New("graph embeddings disabled")
	}
	enc, err := cfg.Encoder()
	if err != nil {
		return 0, err
	}
	ver, err := src.version()
	if err != nil {
		return 0, err
	}
	_, n, err := s.reconcile(src, cfg, enc, ver)
	return n, err
}

// SearchNodes is the graph's semantic arm: reconcile if the graph moved,
// Hamming-scan the mapped codes to the top-D candidates, re-encode those
// documents, and return the topK nodes by exact cosine with their scores.
func (s *Embeddings) SearchNodes(graphID uint32, query, nodeType string, topK int) ([]*models.Node, []float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.cfg
	if !cfg.Enabled {
		return nil, nil, errors.New("graph embeddings disabled")
	}
	var filter map[uint32]struct{}
	if nodeType != "" {
		var err error
		filter, err = models.GetNodeIDsByType(s.db, graphID, nodeType)
		if err != nil {
			return nil, nil, err
		}
	}
	ids, scores, err := s.search(graphSource{db: s.db, graphID: graphID}, cfg, query, filter, topK)
	if err != nil || len(ids) == 0 {
		return nil, nil, err
	}
	byID, err := models.GetNodesByIDs(s.db, ids)
	if err != nil {
		return nil, nil, err
	}
	nodes := make([]*models.Node, 0, len(ids))
	kept := scores[:0]
	for i, id := range ids {
		if n, ok := byID[id]; ok {
			nodes = append(nodes, n)
			kept = append(kept, scores[i])
		}
	}
	return nodes, kept, nil
}

func (s *Embeddings) search(src embSource, cfg EmbeddingConfig, query string, filter map[uint32]struct{}, topK int) ([]uint32, []float64, error) {
	if topK < 1 {
		return nil, nil, errors.New("embedding search needs a positive limit")
	}
	enc, err := cfg.Encoder()
	if err != nil {
		return nil, nil, err
	}
	ver, err := src.version()
	if err != nil {
		return nil, nil, err
	}
	c := s.caches[src.key()]
	if c == nil || c.version != ver || c.mm == nil {
		if c, _, err = s.reconcile(src, cfg, enc, ver); err != nil {
			return nil, nil, fmt.Errorf("embedding reconcile: %w", err)
		}
	}
	c.lastUse = time.Now()
	if c.hdr.count == 0 {
		return nil, nil, nil
	}

	qvec, err := enc.Encode(query)
	if err != nil {
		return nil, nil, err
	}
	if zeroVec(qvec) {
		return nil, nil, nil
	}
	qwords := quantize(qvec)
	if len(qwords) != c.wpd {
		return nil, nil, fmt.Errorf("embedding cache has %d dims, encoder produced %d", c.hdr.dims, len(qvec))
	}
	cands := scanCache(c, qwords, filter, max(rescoreDepth, topK))
	if len(cands) == 0 {
		return nil, nil, nil
	}

	docs, err := src.byID(cands)
	if err != nil {
		return nil, nil, err
	}
	kept := make([]uint32, 0, len(cands))
	texts := make([]string, 0, len(cands))
	for _, id := range cands {
		d, ok := docs[id]
		if !ok {
			continue // deleted between scan and fetch
		}
		kept = append(kept, id)
		texts = append(texts, d.text)
	}
	vecs, err := enc.EncodeMany(texts)
	if err != nil {
		return nil, nil, err
	}
	scores := make([]float32, len(vecs))
	f32.DotProductBatch(scores, vecs, qvec)
	order := make([]int, len(kept))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	if topK < len(order) {
		order = order[:topK]
	}
	outIDs := make([]uint32, len(order))
	outScores := make([]float64, len(order))
	for i, j := range order {
		outIDs[i] = kept[j]
		outScores[i] = float64(scores[j])
	}
	return outIDs, outScores, nil
}

func (s *Embeddings) reconcile(src embSource, cfg EmbeddingConfig, enc EmbeddingEncoder, ver int64) (*embCache, int, error) {
	dir, err := s.cacheDir()
	if err != nil {
		return nil, 0, err
	}
	if !s.swept {
		s.swept = true
		s.sweep(dir, cfg.Model)
	}
	path := filepath.Join(dir, src.key()+"-"+cfg.Model+".emb")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, 0, err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return nil, 0, err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)

	c := s.caches[src.key()]
	if c == nil {
		c = &embCache{}
		s.caches[src.key()] = c
	}
	if err := c.refresh(path); err != nil {
		// Unreadable beyond salvage: rebuild from nothing — it's a cache.
		logger.Warn().Err(err).Str("file", path).Msg("embedding cache unreadable; rebuilding")
		c.close()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, 0, err
		}
		*c = embCache{}
		if err := c.refresh(path); err != nil {
			return nil, 0, err
		}
	}
	c.rebuildTombstones()

	stale := c.hdr.version != ver
	if !stale && c.hdr.count == 0 {
		if n, err := src.count(); err == nil && n > 0 {
			stale = true
		}
	}
	encoded := 0
	if stale {
		if encoded, err = s.applyDelta(c, src, enc, ver); err != nil {
			return nil, 0, err
		}
	}
	if c.deadCount > max(s.compactMin, c.hdr.count/5) {
		if err := c.compact(path); err != nil {
			return nil, 0, err
		}
	}
	c.version = ver
	c.lastUse = time.Now()
	s.startJanitor()
	return c, encoded, nil
}

func (c *embCache) refresh(path string) error {
	if c.f != nil {
		onDisk, err := os.Stat(path)
		self, ferr := c.f.Stat()
		if err != nil || ferr != nil || !os.SameFile(onDisk, self) {
			c.close()
		}
	}
	if c.f == nil {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		c.f = f
		c.verified = 0
	}
	fi, err := c.f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() < embHeaderSize {
		// Fresh file: dims unknown until the first encode.
		if _, err := c.f.WriteAt(embHeader{}.encode(), 0); err != nil {
			return err
		}
		c.hdr = embHeader{}
		c.wpd = 0
		return c.remap()
	}
	hb := make([]byte, embHeaderSize)
	if _, err := c.f.ReadAt(hb, 0); err != nil {
		return err
	}
	hdr, err := decodeHeader(hb)
	if err != nil {
		return err
	}
	c.hdr = hdr
	c.wpd = (hdr.dims + 63) / 64
	if c.wpd > 0 {
		if fit := int((fi.Size() - embHeaderSize)) / embRecordSize(c.wpd); fit < c.hdr.count {
			c.hdr.count = fit // torn append: header raced ahead of the data
		}
	}
	if err := c.remap(); err != nil {
		return err
	}
	return c.verifyRecords(path)
}

func (c *embCache) remap() error {
	if c.mm != nil {
		unix.Munmap(c.mm)
		c.mm = nil
	}
	size := embHeaderSize
	if c.wpd > 0 {
		size += c.hdr.count * embRecordSize(c.wpd)
	}
	mm, err := unix.Mmap(int(c.f.Fd()), 0, size, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return err
	}
	c.mm = mm
	return nil
}

func (c *embCache) verifyRecords(path string) error {
	if c.wpd == 0 {
		return nil
	}
	rs := embRecordSize(c.wpd)
	for i := c.verified; i < c.hdr.count; i++ {
		r := c.record(i)
		if crc32.Checksum(r[:rs-4], embCRC) != binary.LittleEndian.Uint32(r[rs-4:]) {
			logger.Warn().Str("file", path).Int("record", i).Msg("embedding cache record CRC mismatch; truncating")
			c.hdr.count = i
			var wm int64
			for j := 0; j < i; j++ {
				wm = max(wm, c.recordStamp(j))
			}
			c.hdr.watermark = wm
			c.hdr.version = 0 // force a delta pass
			if _, err := c.f.WriteAt(c.hdr.encode(), 0); err != nil {
				return err
			}
			return c.remap()
		}
	}
	c.verified = c.hdr.count
	return nil
}

func (c *embCache) rebuildTombstones() {
	c.dead = make([]bool, c.hdr.count)
	c.deadCount = 0
	if c.hdr.count == 0 {
		return
	}
	last := make(map[uint32]int, c.hdr.count)
	for i := 0; i < c.hdr.count; i++ {
		id := c.recordID(i)
		if prev, ok := last[id]; ok {
			c.dead[prev] = true
			c.deadCount++
		}
		last[id] = i
	}
}

func (s *Embeddings) applyDelta(c *embCache, src embSource, enc EmbeddingEncoder, ver int64) (int, error) {
	changed, err := src.changed(c.hdr.watermark)
	if err != nil {
		return 0, err
	}
	current := make(map[uint32]int64, c.hdr.count)
	slot := make(map[uint32]int, c.hdr.count)
	for i := 0; i < c.hdr.count; i++ {
		if c.dead[i] {
			continue
		}
		id := c.recordID(i)
		current[id] = c.recordStamp(i)
		slot[id] = i
	}
	var todo []embDoc
	for _, d := range changed {
		if stamp, ok := current[d.id]; ok && stamp == d.stamp {
			continue
		}
		todo = append(todo, d)
	}
	encoded, err := c.appendDocs(enc, todo, slot, current)
	if err != nil {
		return encoded, err
	}

	live := c.hdr.count - c.deadCount
	dbCount, err := src.count()
	if err != nil {
		return encoded, err
	}
	if live != dbCount {
		ids, err := src.allIDs()
		if err != nil {
			return encoded, err
		}
		alive := make(map[uint32]struct{}, len(ids))
		var missing []uint32
		for _, id := range ids {
			alive[id] = struct{}{}
			if _, ok := slot[id]; !ok {
				missing = append(missing, id)
			}
		}
		for id, i := range slot {
			if _, ok := alive[id]; !ok && !c.dead[i] {
				c.dead[i] = true
				c.deadCount++
			}
		}
		if len(missing) > 0 {
			byID, err := src.byID(missing)
			if err != nil {
				return encoded, err
			}
			strays := make([]embDoc, 0, len(byID))
			for _, id := range missing {
				if d, ok := byID[id]; ok {
					strays = append(strays, d)
				}
			}
			n, err := c.appendDocs(enc, strays, slot, current)
			encoded += n
			if err != nil {
				return encoded, err
			}
		}
	}

	stamped := c.hdr
	stamped.version = ver
	if _, err := c.f.WriteAt(stamped.encode(), 0); err != nil {
		return encoded, err
	}
	c.hdr = stamped
	return encoded, nil
}

func (c *embCache) appendDocs(enc EmbeddingEncoder, todo []embDoc, slot map[uint32]int, current map[uint32]int64) (int, error) {
	encoded := 0
	watermark := c.hdr.watermark
	for start := 0; start < len(todo); start += encodeBatch {
		batch := todo[start:min(start+encodeBatch, len(todo))]
		texts := make([]string, len(batch))
		for i, d := range batch {
			texts[i] = d.text
		}
		vecs, err := enc.EncodeMany(texts)
		if err != nil {
			return encoded, err
		}
		if c.wpd == 0 && len(vecs) > 0 {
			c.hdr.dims = len(vecs[0])
			c.wpd = (c.hdr.dims + 63) / 64
		}
		rs := embRecordSize(c.wpd)
		buf := make([]byte, 0, len(batch)*rs)
		for i, d := range batch {
			words := quantize(vecs[i])
			if len(words) != c.wpd {
				return encoded, fmt.Errorf("encoder produced %d dims, cache has %d", len(vecs[i]), c.hdr.dims)
			}
			rec := make([]byte, rs)
			for j, w := range words {
				binary.LittleEndian.PutUint64(rec[j*8:], w)
			}
			binary.LittleEndian.PutUint64(rec[c.wpd*8:], uint64(d.stamp))
			binary.LittleEndian.PutUint32(rec[c.wpd*8+8:], d.id)
			binary.LittleEndian.PutUint32(rec[rs-4:], crc32.Checksum(rec[:rs-4], embCRC))
			buf = append(buf, rec...)
			watermark = max(watermark, d.stamp)
		}
		if _, err := c.f.WriteAt(buf, int64(embHeaderSize+c.hdr.count*rs)); err != nil {
			return encoded, err
		}
		newCount := c.hdr.count + len(batch)
		grown := c.hdr
		grown.count = newCount
		grown.watermark = watermark
		if _, err := c.f.WriteAt(grown.encode(), 0); err != nil {
			return encoded, err
		}
		c.hdr = grown
		if err := c.remap(); err != nil {
			return encoded, err
		}
		for i, d := range batch {
			c.dead = append(c.dead, false)
			if prev, ok := slot[d.id]; ok && !c.dead[prev] {
				c.dead[prev] = true
				c.deadCount++
			}
			idx := newCount - len(batch) + i
			slot[d.id] = idx
			current[d.id] = d.stamp
		}
		c.verified = c.hdr.count // we wrote these; no need to re-verify
		encoded += len(batch)
	}
	return encoded, nil
}

func (c *embCache) compact(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".compact-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	hdr := c.hdr
	hdr.count = 0
	hdr.watermark = 0
	if _, err := tmp.Write(hdr.encode()); err != nil {
		tmp.Close()
		return err
	}
	for i := 0; i < len(c.dead); i++ {
		if c.dead[i] {
			continue
		}
		if _, err := tmp.Write(c.record(i)); err != nil {
			tmp.Close()
			return err
		}
		hdr.count++
		hdr.watermark = max(hdr.watermark, c.recordStamp(i))
	}
	if _, err := tmp.WriteAt(hdr.encode(), 0); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	c.close()
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	c.f = f
	c.hdr = hdr
	c.verified = hdr.count
	if err := c.remap(); err != nil {
		return err
	}
	c.rebuildTombstones()
	return nil
}

func (s *Embeddings) startJanitor() {
	if s.janitor {
		return
	}
	s.janitor = true
	ttl := s.ttl
	go func() {
		for {
			time.Sleep(ttl / 5)
			s.mu.Lock()
			now := time.Now()
			for key, c := range s.caches {
				if now.Sub(c.lastUse) > ttl {
					c.close()
					delete(s.caches, key)
				}
			}
			if len(s.caches) == 0 {
				s.janitor = false
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
		}
	}()
}

type candidate struct {
	id   uint32
	dist int32
}

type distHeap struct {
	c   []candidate
	cap int
}

func (h *distHeap) push(id uint32, dist int32) {
	if len(h.c) < h.cap {
		h.c = append(h.c, candidate{id, dist})
		i := len(h.c) - 1
		for i > 0 {
			p := (i - 1) / 2
			if h.c[p].dist >= h.c[i].dist {
				break
			}
			h.c[p], h.c[i] = h.c[i], h.c[p]
			i = p
		}
		return
	}
	if dist >= h.c[0].dist {
		return
	}
	h.c[0] = candidate{id, dist}
	i := 0
	for {
		l, r := 2*i+1, 2*i+2
		big := i
		if l < len(h.c) && h.c[l].dist > h.c[big].dist {
			big = l
		}
		if r < len(h.c) && h.c[r].dist > h.c[big].dist {
			big = r
		}
		if big == i {
			return
		}
		h.c[i], h.c[big] = h.c[big], h.c[i]
		i = big
	}
}

func (h *distHeap) worst() int32 {
	if len(h.c) < h.cap {
		return 1 << 30
	}
	return h.c[0].dist
}

func scanCache(c *embCache, q []uint64, filter map[uint32]struct{}, topD int) []uint32 {
	n := c.hdr.count
	if n == 0 {
		return nil
	}
	shards := 1
	if n >= scanShardMin {
		shards = min(runtime.GOMAXPROCS(0), 8)
	}
	rs := embRecordSize(c.wpd)
	heaps := make([]distHeap, shards)
	var wg sync.WaitGroup
	for sh := 0; sh < shards; sh++ {
		lo, hi := n*sh/shards, n*(sh+1)/shards
		heaps[sh] = distHeap{cap: topD}
		wg.Add(1)
		go func(h *distHeap, lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				if c.dead[i] {
					continue
				}
				rec := c.mm[embHeaderSize+i*rs:]
				id := binary.LittleEndian.Uint32(rec[c.wpd*8+8:])
				if filter != nil {
					if _, ok := filter[id]; !ok {
						continue
					}
				}
				var dist int32
				for j := range q {
					dist += int32(bits.OnesCount64(binary.LittleEndian.Uint64(rec[j*8:]) ^ q[j]))
				}
				if dist < h.worst() {
					h.push(id, dist)
				}
			}
		}(&heaps[sh], lo, hi)
	}
	wg.Wait()
	var all []candidate
	for i := range heaps {
		all = append(all, heaps[i].c...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].dist != all[j].dist {
			return all[i].dist < all[j].dist
		}
		return all[i].id < all[j].id
	})
	if topD < len(all) {
		all = all[:topD]
	}
	ids := make([]uint32, len(all))
	for i, cand := range all {
		ids[i] = cand.id
	}
	return ids
}

func quantize(vec []float32) []uint64 {
	words := make([]uint64, (len(vec)+63)/64)
	for i, x := range vec {
		if x > 0 {
			words[i/64] |= 1 << (uint(i) % 64)
		}
	}
	return words
}

func zeroVec(v []float32) bool {
	for _, x := range v {
		if x != 0 {
			return false
		}
	}
	return true
}

const rrfK = 60

const hybridArmDepth = 50

// HybridSearchNodes fuses the FTS/BM25 arm with the embedding arm by
// reciprocal rank; scores normalize to top row = 1.0, and limit must be
// positive.
func (g *Graph) HybridSearchNodes(query, nodeType string, limit int) ([]*models.Node, []float64, error) {
	if g.emb == nil {
		return nil, nil, errors.New("no embedding cache")
	}
	depth := max(hybridArmDepth, limit)
	lexNodes, _, err := models.SearchNodesLexical(g.db, g.ID, query, nodeType, depth)
	if err != nil {
		return nil, nil, fmt.Errorf("lexical arm: %w", err)
	}
	semNodes, _, err := g.emb.SearchNodes(g.ID, query, nodeType, depth)
	if err != nil {
		return nil, nil, fmt.Errorf("embedding arm: %w", err)
	}

	fused := map[uint32]float64{}
	byID := map[uint32]*models.Node{}
	for rank, n := range lexNodes {
		fused[n.ID] += 0.5 / float64(rrfK+rank+1)
		byID[n.ID] = n
	}
	for rank, n := range semNodes {
		fused[n.ID] += 0.5 / float64(rrfK+rank+1)
		byID[n.ID] = n
	}
	if len(fused) == 0 {
		return nil, nil, nil
	}
	ids := make([]uint32, 0, len(fused))
	for id := range fused {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if fused[ids[i]] != fused[ids[j]] {
			return fused[ids[i]] > fused[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if limit > 0 && limit < len(ids) {
		ids = ids[:limit]
	}
	top := fused[ids[0]]
	nodes := make([]*models.Node, len(ids))
	scores := make([]float64, len(ids))
	for i, id := range ids {
		nodes[i] = byID[id]
		scores[i] = fused[id] / top
	}
	return nodes, scores, nil
}
