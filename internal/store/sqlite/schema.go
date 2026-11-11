package sqlite

// migrations holds the schema, one entry per version. Entry i brings a file
// from schema version i to version i+1; the file's version is stored in
// SQLite's own user_version. To change the schema, append a new entry. Never
// edit an existing one: files already written with it would not be upgraded.
//
// Times are fixed-width UTC text (see timeLayout), which sorts correctly as
// text and can be read by eye in a SQLite tool. Empty strings stand for "not
// set" in the text columns.
var migrations = [][]string{
	{ // version 1
		`CREATE TABLE run (
			id          INTEGER PRIMARY KEY,
			source      TEXT NOT NULL CHECK (source IN ('local', 'remote')),
			environment TEXT NOT NULL,
			state       TEXT NOT NULL CHECK (state IN ('pending', 'complete', 'expired')),
			started_at  TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			exit_code   INTEGER NOT NULL,
			external_id TEXT NOT NULL,
			attempt     INTEGER NOT NULL,
			commit_sha  TEXT NOT NULL,
			diagnostic  TEXT NOT NULL,
			outcome     TEXT NOT NULL CHECK (outcome IN ('pass', 'fail', 'undetermined')),
			reason      TEXT NOT NULL,
			total       INTEGER NOT NULL,
			failed      INTEGER NOT NULL
		)`,
		// A remote run is identified by its external id and attempt. Local runs
		// have an empty external id and are never matched on it.
		`CREATE UNIQUE INDEX run_key ON run (external_id, attempt) WHERE external_id <> ''`,
		`CREATE INDEX run_started ON run (started_at)`,
		`CREATE INDEX run_state ON run (state)`,
		`CREATE TABLE result (
			id          INTEGER PRIMARY KEY,
			run_id      INTEGER NOT NULL REFERENCES run (id) ON DELETE CASCADE,
			position    INTEGER NOT NULL,
			test_id     TEXT NOT NULL,
			feature     TEXT NOT NULL,
			name        TEXT NOT NULL,
			status      TEXT NOT NULL CHECK (status IN ('pass', 'fail', 'skip', 'error')),
			duration_ns INTEGER NOT NULL,
			message     TEXT NOT NULL
		)`,
		`CREATE INDEX result_run ON result (run_id, position)`,
		`CREATE INDEX result_test ON result (test_id)`,
		`CREATE INDEX result_feature ON result (feature)`,
	},
	{ // version 2: the test tool's own detail, kept apart from the diagnostic
		`ALTER TABLE run ADD COLUMN detail TEXT NOT NULL DEFAULT ''`,
	},
}
