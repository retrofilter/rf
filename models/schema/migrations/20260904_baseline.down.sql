DROP TABLE IF EXISTS message_sync;
DROP INDEX IF EXISTS message_created_idx;
DROP INDEX IF EXISTS message_uuid_idx;
DROP INDEX IF EXISTS message_session_idx;
DROP TABLE IF EXISTS message;
DROP TABLE IF EXISTS session;

DROP INDEX IF EXISTS history_cwd_idx;
DROP INDEX IF EXISTS history_created_at_idx;
DROP TABLE IF EXISTS history;

DROP INDEX IF EXISTS idx_node_graph_updated;
DROP TRIGGER IF EXISTS node_version_graph_delete;
DROP TRIGGER IF EXISTS node_version_delete;
DROP TRIGGER IF EXISTS node_version_update;
DROP TRIGGER IF EXISTS node_version_insert;
DROP TABLE IF EXISTS node_version;

DROP TRIGGER IF EXISTS node_fts_delete;
DROP TRIGGER IF EXISTS node_fts_update;
DROP TRIGGER IF EXISTS node_fts_insert;
DROP TABLE IF EXISTS node_fts;

DROP INDEX IF EXISTS idx_edge_source_target;
DROP INDEX IF EXISTS idx_edge_type;
DROP INDEX IF EXISTS idx_edge_target;
DROP INDEX IF EXISTS idx_edge_source;
DROP INDEX IF EXISTS idx_edge_graph_id;
DROP INDEX IF EXISTS idx_node_type;
DROP INDEX IF EXISTS idx_node_graph_id;
DROP TABLE IF EXISTS edge;
DROP TABLE IF EXISTS node;
DROP TABLE IF EXISTS graph;
