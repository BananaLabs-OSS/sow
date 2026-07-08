-- +sew up
CREATE TABLE IF NOT EXISTS sew_cell_demo (
	id         INTEGER PRIMARY KEY,
	note       TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO sew_cell_demo (id, note) VALUES (1, 'migrated by the sew cell');

-- +sew down
DROP TABLE sew_cell_demo;
