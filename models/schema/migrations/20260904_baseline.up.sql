-- Baseline schema, squashed from the pre-release migration chain. Every
-- statement is idempotent so a database migrated by that chain (whose
-- recorded versions are ignored as orphans) applies this as a no-op.

-- Graphs: nodes and edges carry free-form JSON properties.
CREATE TABLE IF NOT EXISTS graph (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    metadata JSON,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS node (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    graph_id INTEGER NOT NULL,
    type TEXT,
    properties JSON,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (graph_id) REFERENCES graph(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS edge (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    graph_id INTEGER NOT NULL,
    source INTEGER NOT NULL,
    target INTEGER NOT NULL,
    type TEXT,
    properties JSON,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (graph_id) REFERENCES graph(id) ON DELETE CASCADE,
    FOREIGN KEY (source) REFERENCES node(id) ON DELETE CASCADE,
    FOREIGN KEY (target) REFERENCES node(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_node_graph_id ON node(graph_id);
CREATE INDEX IF NOT EXISTS idx_node_type ON node(type);
CREATE INDEX IF NOT EXISTS idx_edge_graph_id ON edge(graph_id);
CREATE INDEX IF NOT EXISTS idx_edge_source ON edge(source);
CREATE INDEX IF NOT EXISTS idx_edge_target ON edge(target);
CREATE INDEX IF NOT EXISTS idx_edge_type ON edge(type);
CREATE INDEX IF NOT EXISTS idx_edge_source_target ON edge(source, target);

-- FTS over nodes: `text` is every string value in the properties JSON
-- (any depth, via json_tree), maintained by triggers.
CREATE VIRTUAL TABLE IF NOT EXISTS node_fts USING fts5(
    type,
    text
);

CREATE TRIGGER IF NOT EXISTS node_fts_insert AFTER INSERT ON node
BEGIN
    INSERT INTO node_fts (rowid, type, text)
    VALUES (NEW.id, COALESCE(NEW.type, ''),
        COALESCE((SELECT group_concat(jt.value, ' ')
                  FROM json_tree(COALESCE(NEW.properties, '{}')) AS jt
                  WHERE jt.type = 'text'), ''));
END;

CREATE TRIGGER IF NOT EXISTS node_fts_update AFTER UPDATE ON node
BEGIN
    DELETE FROM node_fts WHERE rowid = OLD.id;
    INSERT INTO node_fts (rowid, type, text)
    VALUES (NEW.id, COALESCE(NEW.type, ''),
        COALESCE((SELECT group_concat(jt.value, ' ')
                  FROM json_tree(COALESCE(NEW.properties, '{}')) AS jt
                  WHERE jt.type = 'text'), ''));
END;

CREATE TRIGGER IF NOT EXISTS node_fts_delete AFTER DELETE ON node
BEGIN
    DELETE FROM node_fts WHERE rowid = OLD.id;
END;

-- Assists for the quantized-embedding cache (core/embedding.go), which
-- lives outside the database: a per-graph change counter bumped by every
-- node write (the cache's freshness probe is one point read of it), and
-- the (graph_id, updated_at) index its delta reads range over.
CREATE TABLE IF NOT EXISTS node_version (
    graph_id INTEGER PRIMARY KEY,
    version INTEGER NOT NULL
) WITHOUT ROWID;

CREATE TRIGGER IF NOT EXISTS node_version_insert AFTER INSERT ON node
BEGIN
    INSERT INTO node_version (graph_id, version) VALUES (NEW.graph_id, 1)
    ON CONFLICT (graph_id) DO UPDATE SET version = version + 1;
END;

CREATE TRIGGER IF NOT EXISTS node_version_update AFTER UPDATE ON node
BEGIN
    INSERT INTO node_version (graph_id, version) VALUES (NEW.graph_id, 1)
    ON CONFLICT (graph_id) DO UPDATE SET version = version + 1;
END;

CREATE TRIGGER IF NOT EXISTS node_version_delete AFTER DELETE ON node
BEGIN
    INSERT INTO node_version (graph_id, version) VALUES (OLD.graph_id, 1)
    ON CONFLICT (graph_id) DO UPDATE SET version = version + 1;
END;

CREATE TRIGGER IF NOT EXISTS node_version_graph_delete AFTER DELETE ON graph
BEGIN
    DELETE FROM node_version WHERE graph_id = OLD.id;
END;

CREATE INDEX IF NOT EXISTS idx_node_graph_updated ON node (graph_id, updated_at);

-- A graph whose nodes predate the counter must not read as version 0 (an
-- empty graph, to the cache); seed one bump per populated graph.
INSERT INTO node_version (graph_id, version)
SELECT DISTINCT graph_id, 1 FROM node WHERE TRUE
ON CONFLICT (graph_id) DO NOTHING;

-- Command history: every line accepted at the prompt, with where and how
-- it ran.
CREATE TABLE IF NOT EXISTS history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    line TEXT NOT NULL,
    mode TEXT NOT NULL,
    cwd TEXT NOT NULL,
    project TEXT NOT NULL DEFAULT '',
    exit_code INTEGER,
    session TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS history_created_at_idx ON history (created_at);
CREATE INDEX IF NOT EXISTS history_cwd_idx ON history (cwd);

-- Chat sessions and the message corpus. `session` is one row per process
-- that had a chat turn (its id is the one history.session records; agent
-- sub-sessions link to their spawner via `parent`; `source` names the
-- harness: 'rf', or 'claude' for transcripts synced from Claude Code).
-- `message` is the append-only transcript log: rebuilt on load, never
-- rewritten, compaction appends. Synced rows keep their transcript line
-- uuid as the dedupe key; message_sync checkpoints a byte offset per
-- transcript file.
CREATE TABLE IF NOT EXISTS session (
    id TEXT PRIMARY KEY,
    cwd TEXT NOT NULL DEFAULT '',
    project TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    parent TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'rf'
);

CREATE TABLE IF NOT EXISTS message (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session TEXT NOT NULL REFERENCES session(id) ON DELETE CASCADE,
    parent_id INTEGER REFERENCES message(id),
    kind TEXT NOT NULL,
    payload JSON NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    uuid TEXT
);

CREATE INDEX IF NOT EXISTS message_session_idx ON message (session, id);
CREATE UNIQUE INDEX IF NOT EXISTS message_uuid_idx ON message (uuid) WHERE uuid IS NOT NULL;
CREATE INDEX IF NOT EXISTS message_created_idx ON message (created_at);

CREATE TABLE IF NOT EXISTS message_sync (
    path TEXT PRIMARY KEY,
    offset INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Model configuration lives in the prelude and credentials in ~/.rf.env;
-- the pre-release settings table is gone.
DROP TABLE IF EXISTS settings;
