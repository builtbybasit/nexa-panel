CREATE TABLE audit_chain_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    watermark INTEGER NOT NULL
);
--bun:split
INSERT INTO audit_chain_state (id, watermark)
SELECT 1, COALESCE(MAX(id), 0) FROM audit_events WHERE hash = '';
