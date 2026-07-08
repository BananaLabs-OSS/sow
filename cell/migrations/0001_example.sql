-- +sow up
CREATE TABLE IF NOT EXISTS sow_cell_demo (
	id         INTEGER PRIMARY KEY,
	note       TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO sow_cell_demo (id, note) VALUES (1, 'migrated by the sow cell');

-- +sow down
DROP TABLE sow_cell_demo;
