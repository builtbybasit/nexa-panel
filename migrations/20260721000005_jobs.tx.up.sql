CREATE TABLE jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	progress INTEGER NOT NULL DEFAULT 0,
	actor_user_id TEXT,
	request_json TEXT NOT NULL DEFAULT '{}',
	result_json TEXT,
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	started_at TIMESTAMP,
	completed_at TIMESTAMP
);
--bun:split
CREATE INDEX jobs_state_id_idx ON jobs (state, id);
--bun:split
CREATE TABLE job_events (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	state TEXT NOT NULL,
	progress INTEGER NOT NULL,
	message TEXT NOT NULL,
	occurred_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX job_events_job_sequence_idx ON job_events (job_id, sequence);
