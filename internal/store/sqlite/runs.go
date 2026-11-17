package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// timeLayout is fixed-width UTC text: always nine fractional digits, so the
// strings sort in time order.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(timeLayout, s)
}

// SaveRun stores a run and returns its ID. The run's outcome and reason are
// worked out now, by the core's rule, and stored with it, so listing the
// history never has to load results. A run with a key (external id plus
// attempt) that is already stored replaces that run and keeps its ID.
func (s *Store) SaveRun(ctx context.Context, run core.Run) (int64, error) {
	verdict := run.Verdict()
	// The totals count tests, not checks: a file that prints 42 checks is one.
	total, failed := run.Counts()

	var id int64
	err := s.writeTx(ctx, func(conn *sql.Conn) error {
		if _, keyed := run.Key(); keyed {
			err := conn.QueryRowContext(ctx,
				`SELECT id FROM run WHERE external_id = ? AND attempt = ?`, run.ExternalID, run.Attempt).Scan(&id)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}

		fields := []any{
			string(run.Source), run.Environment, string(run.State),
			formatTime(run.StartedAt), formatTime(run.FinishedAt), run.ExitCode,
			run.ExternalID, run.Attempt, run.CommitSHA, run.Diagnostic, run.Detail,
			string(verdict.Outcome), verdict.Reason, total, failed,
			boolToInt(run.Dirty), string(run.Trigger),
		}
		if id != 0 {
			if _, err := conn.ExecContext(ctx, `UPDATE run SET
				source = ?, environment = ?, state = ?, started_at = ?, finished_at = ?, exit_code = ?,
				external_id = ?, attempt = ?, commit_sha = ?, diagnostic = ?, detail = ?,
				outcome = ?, reason = ?, total = ?, failed = ?, dirty = ?, trigger_name = ?
				WHERE id = ?`, append(fields, id)...); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `DELETE FROM result WHERE run_id = ?`, id); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `DELETE FROM run_tool WHERE run_id = ?`, id); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `DELETE FROM run_file WHERE run_id = ?`, id); err != nil {
				return err
			}
		} else {
			res, err := conn.ExecContext(ctx, `INSERT INTO run (
				source, environment, state, started_at, finished_at, exit_code,
				external_id, attempt, commit_sha, diagnostic, detail,
				outcome, reason, total, failed, dirty, trigger_name)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, fields...)
			if err != nil {
				return err
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
		}

		stmt, err := conn.PrepareContext(ctx, `INSERT INTO result
			(run_id, position, test_id, feature, name, status, duration_ns, message)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		// Prepared the first time a result has measurements: most runs have none.
		var mstmt *sql.Stmt
		defer func() {
			if mstmt != nil {
				mstmt.Close()
			}
		}()
		for i, r := range run.Results {
			res, err := stmt.ExecContext(ctx, id, i, r.TestID, r.Feature, r.Name, string(r.Status), int64(r.Duration), r.Message)
			if err != nil {
				return err
			}
			if len(r.Measurements) == 0 {
				continue
			}
			resultID, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if mstmt == nil {
				if mstmt, err = conn.PrepareContext(ctx, `INSERT INTO measurement (result_id, position, name, value) VALUES (?, ?, ?, ?)`); err != nil {
					return err
				}
			}
			for j, m := range r.Measurements {
				if _, err := mstmt.ExecContext(ctx, resultID, j, m.Name, m.Value); err != nil {
					return err
				}
			}
		}
		for i, tool := range run.Tools {
			if _, err := conn.ExecContext(ctx, `INSERT INTO run_tool (run_id, position, name, version) VALUES (?, ?, ?, ?)`,
				id, i, tool.Name, tool.Version); err != nil {
				return err
			}
		}
		for _, f := range run.Files {
			if f.Kept() {
				// The same text under the same hash is stored once.
				if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO blob (hash, content) VALUES (?, ?)`, f.Hash, f.Content); err != nil {
					return err
				}
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO run_file (run_id, path, role, test_id, hash, size, note)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, id, f.Path, string(f.Role), f.TestID, f.Hash, f.Size, f.Note); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: saving run: %w", err)
	}
	return id, nil
}

// GetRun returns one run with its results, or core.ErrNotFound.
func (s *Store) GetRun(ctx context.Context, id int64) (core.Run, error) {
	return s.loadRun(ctx, `WHERE id = ?`, id)
}

// FindByKey returns the stored run with this external id and attempt, or
// core.ErrNotFound.
func (s *Store) FindByKey(ctx context.Context, externalID string, attempt int) (core.Run, error) {
	if externalID == "" {
		return core.Run{}, fmt.Errorf("store: a local run has no key: %w", core.ErrNotFound)
	}
	return s.loadRun(ctx, `WHERE external_id = ? AND attempt = ?`, externalID, attempt)
}

func (s *Store) loadRun(ctx context.Context, where string, args ...any) (core.Run, error) {
	// One read transaction, so the run and its results come from the same
	// moment even if another program is replacing the run.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Run{}, fmt.Errorf("store: reading run: %w", err)
	}
	defer tx.Rollback()

	var (
		run                              core.Run
		source, state, started, finished string
		dirty                            int
		trigger                          string
	)
	err = tx.QueryRowContext(ctx, `SELECT id, source, environment, state, started_at, finished_at, exit_code,
		external_id, attempt, commit_sha, diagnostic, detail, dirty, trigger_name FROM run `+where, args...).
		Scan(&run.ID, &source, &run.Environment, &state, &started, &finished, &run.ExitCode,
			&run.ExternalID, &run.Attempt, &run.CommitSHA, &run.Diagnostic, &run.Detail, &dirty, &trigger)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Run{}, fmt.Errorf("store: run not found: %w", core.ErrNotFound)
	}
	if err != nil {
		return core.Run{}, fmt.Errorf("store: reading run: %w", err)
	}

	run.Dirty = dirty != 0
	run.Trigger = core.Trigger(trigger)
	if run.Source, err = parseSource(source); err != nil {
		return core.Run{}, err
	}
	if run.State, err = core.ParseRunState(state); err != nil {
		return core.Run{}, fmt.Errorf("store: run %d: %w", run.ID, err)
	}
	if run.StartedAt, err = parseTime(started); err != nil {
		return core.Run{}, fmt.Errorf("store: run %d: %w", run.ID, err)
	}
	if run.FinishedAt, err = parseTime(finished); err != nil {
		return core.Run{}, fmt.Errorf("store: run %d: %w", run.ID, err)
	}

	rows, err := tx.QueryContext(ctx, `SELECT test_id, feature, name, status, duration_ns, message
		FROM result WHERE run_id = ? ORDER BY position`, run.ID)
	if err != nil {
		return core.Run{}, fmt.Errorf("store: reading results: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r      core.Result
			status string
			nanos  int64
		)
		if err := rows.Scan(&r.TestID, &r.Feature, &r.Name, &status, &nanos, &r.Message); err != nil {
			return core.Run{}, fmt.Errorf("store: reading results: %w", err)
		}
		if r.Status, err = core.ParseStatus(status); err != nil {
			return core.Run{}, fmt.Errorf("store: run %d: %w", run.ID, err)
		}
		r.Duration = time.Duration(nanos)
		run.Results = append(run.Results, r)
	}
	if err := rows.Err(); err != nil {
		return core.Run{}, fmt.Errorf("store: reading results: %w", err)
	}

	// The numbers each result carries. A result's position is its index in
	// run.Results, because positions are saved 0, 1, 2 ... in order.
	measureRows, err := tx.QueryContext(ctx, `SELECT x.position, m.name, m.value
		FROM measurement m JOIN result x ON x.id = m.result_id
		WHERE x.run_id = ? ORDER BY x.position, m.position`, run.ID)
	if err != nil {
		return core.Run{}, fmt.Errorf("store: reading measurements: %w", err)
	}
	defer measureRows.Close()
	for measureRows.Next() {
		var (
			pos int
			m   core.Measurement
		)
		if err := measureRows.Scan(&pos, &m.Name, &m.Value); err != nil {
			return core.Run{}, fmt.Errorf("store: reading measurements: %w", err)
		}
		if pos < 0 || pos >= len(run.Results) {
			return core.Run{}, fmt.Errorf("store: run %d: a measurement belongs to result %d, which does not exist", run.ID, pos)
		}
		run.Results[pos].Measurements = append(run.Results[pos].Measurements, m)
	}
	if err := measureRows.Err(); err != nil {
		return core.Run{}, fmt.Errorf("store: reading measurements: %w", err)
	}

	toolRows, err := tx.QueryContext(ctx, `SELECT name, version FROM run_tool WHERE run_id = ? ORDER BY position`, run.ID)
	if err != nil {
		return core.Run{}, fmt.Errorf("store: reading tool versions: %w", err)
	}
	defer toolRows.Close()
	for toolRows.Next() {
		var t core.Tool
		if err := toolRows.Scan(&t.Name, &t.Version); err != nil {
			return core.Run{}, fmt.Errorf("store: reading tool versions: %w", err)
		}
		run.Tools = append(run.Tools, t)
	}
	if err := toolRows.Err(); err != nil {
		return core.Run{}, fmt.Errorf("store: reading tool versions: %w", err)
	}

	// The files' texts are not loaded with the run: a run can list hundreds of
	// files, and most readers want only the list.
	fileRows, err := tx.QueryContext(ctx, `SELECT path, role, test_id, hash, size, note FROM run_file WHERE run_id = ? ORDER BY path`, run.ID)
	if err != nil {
		return core.Run{}, fmt.Errorf("store: reading the run's files: %w", err)
	}
	defer fileRows.Close()
	for fileRows.Next() {
		var f core.SnapshotFile
		var role string
		if err := fileRows.Scan(&f.Path, &role, &f.TestID, &f.Hash, &f.Size, &f.Note); err != nil {
			return core.Run{}, fmt.Errorf("store: reading the run's files: %w", err)
		}
		f.Role = core.SnapshotRole(role)
		run.Files = append(run.Files, f)
	}
	if err := fileRows.Err(); err != nil {
		return core.Run{}, fmt.Errorf("store: reading the run's files: %w", err)
	}
	return run, nil
}

// snapshotColumns are the columns read for one snapshot file, with its text
// when the text was kept.
const snapshotColumns = `f.path, f.role, f.test_id, f.hash, f.size, f.note, b.content`

func scanSnapshot(row interface{ Scan(...any) error }) (core.SnapshotFile, error) {
	var f core.SnapshotFile
	var role string
	if err := row.Scan(&f.Path, &role, &f.TestID, &f.Hash, &f.Size, &f.Note, &f.Content); err != nil {
		return core.SnapshotFile{}, err
	}
	f.Role = core.SnapshotRole(role)
	return f, nil
}

// GetSnapshotFile returns one file of a run's snapshot, with its text when the
// text was kept, or core.ErrNotFound.
func (s *Store) GetSnapshotFile(ctx context.Context, runID int64, path string) (core.SnapshotFile, error) {
	f, err := scanSnapshot(s.db.QueryRowContext(ctx, `SELECT `+snapshotColumns+`
		FROM run_file f LEFT JOIN blob b ON b.hash = f.hash AND f.note = ''
		WHERE f.run_id = ? AND f.path = ?`, runID, path))
	if errors.Is(err, sql.ErrNoRows) {
		return core.SnapshotFile{}, fmt.Errorf("store: run %d has no file %q: %w", runID, path, core.ErrNotFound)
	}
	if err != nil {
		return core.SnapshotFile{}, fmt.Errorf("store: reading a snapshot file: %w", err)
	}
	return f, nil
}

// PassedTestFiles says which versions of each test's file have passed in the
// environment, each with the latest run that passed it. Only runs the tester
// made (they are the ones that have files), and only a test that has a passing
// check and no failed or errored one in the run.
func (s *Store) PassedTestFiles(ctx context.Context, environment string) (map[string]map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.test_id, f.hash, MAX(f.run_id)
		FROM run_file f JOIN run r ON r.id = f.run_id
		WHERE f.role = 'test' AND f.test_id <> '' AND f.hash <> ''
		  AND r.environment = ? AND r.source = 'local' AND r.state = 'complete'
		  AND EXISTS (SELECT 1 FROM result x WHERE x.run_id = f.run_id AND x.test_id = f.test_id AND x.status = 'pass')
		  AND NOT EXISTS (SELECT 1 FROM result x WHERE x.run_id = f.run_id AND x.test_id = f.test_id AND x.status IN ('fail', 'error'))
		GROUP BY f.test_id, f.hash`, environment)
	if err != nil {
		return nil, fmt.Errorf("store: reading what has passed: %w", err)
	}
	defer rows.Close()
	out := make(map[string]map[string]int64)
	for rows.Next() {
		var (
			testID, hash string
			runID        int64
		)
		if err := rows.Scan(&testID, &hash, &runID); err != nil {
			return nil, fmt.Errorf("store: reading what has passed: %w", err)
		}
		if out[testID] == nil {
			out[testID] = make(map[string]int64)
		}
		out[testID][hash] = runID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading what has passed: %w", err)
	}
	return out, nil
}

// LatestTestFile returns the snapshot of a test's file from the most recent run
// that played the test, or core.ErrNotFound.
func (s *Store) LatestTestFile(ctx context.Context, testID string) (int64, core.SnapshotFile, error) {
	var (
		runID int64
		role  string
		f     core.SnapshotFile
	)
	err := s.db.QueryRowContext(ctx, `SELECT f.run_id, `+snapshotColumns+`
		FROM run_file f JOIN run r ON r.id = f.run_id
		LEFT JOIN blob b ON b.hash = f.hash AND f.note = ''
		WHERE f.test_id = ? AND f.role = 'test'
		ORDER BY r.started_at DESC, r.id DESC LIMIT 1`, testID).
		Scan(&runID, &f.Path, &role, &f.TestID, &f.Hash, &f.Size, &f.Note, &f.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, core.SnapshotFile{}, fmt.Errorf("store: no run has played test %q: %w", testID, core.ErrNotFound)
	}
	if err != nil {
		return 0, core.SnapshotFile{}, fmt.Errorf("store: reading a snapshot file: %w", err)
	}
	f.Role = core.SnapshotRole(role)
	return runID, f, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func parseSource(s string) (core.Source, error) {
	switch src := core.Source(s); src {
	case core.SourceLocal, core.SourceRemote, core.SourceImport:
		return src, nil
	}
	return "", fmt.Errorf("store: unknown run source %q", s)
}

// ListRuns returns run summaries, newest first. The outcome, reason and counts
// are the ones stored when the run was saved.
func (s *Store) ListRuns(ctx context.Context, f core.RunFilter) ([]core.RunSummary, error) {
	var where []string
	var args []any
	if f.Feature != "" {
		where = append(where, `EXISTS (SELECT 1 FROM result x WHERE x.run_id = r.id AND x.feature = ?)`)
		args = append(args, f.Feature)
	}
	if f.TestID != "" {
		where = append(where, `EXISTS (SELECT 1 FROM result x WHERE x.run_id = r.id AND x.test_id = ?)`)
		args = append(args, f.TestID)
	}
	if f.Environment != "" {
		where = append(where, `r.environment = ?`)
		args = append(args, f.Environment)
	}
	if f.Source != "" {
		where = append(where, `r.source = ?`)
		args = append(args, string(f.Source))
	}
	if f.State != "" {
		where = append(where, `r.state = ?`)
		args = append(args, string(f.State))
	}

	q := `SELECT r.id, r.source, r.environment, r.state, r.started_at, r.outcome, r.reason,
			r.total, r.failed, r.external_id, r.attempt,
			(SELECT group_concat(f, char(31)) FROM
				(SELECT DISTINCT feature AS f FROM result WHERE run_id = r.id AND feature <> ''))
		FROM run r`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = core.DefaultListLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	q += ` ORDER BY r.started_at DESC, r.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: listing runs: %w", err)
	}
	defer rows.Close()

	var out []core.RunSummary
	for rows.Next() {
		var (
			sum                             core.RunSummary
			source, state, started, outcome string
			features                        sql.NullString
		)
		if err := rows.Scan(&sum.ID, &source, &sum.Environment, &state, &started, &outcome, &sum.Reason,
			&sum.Total, &sum.Failed, &sum.ExternalID, &sum.Attempt, &features); err != nil {
			return nil, fmt.Errorf("store: listing runs: %w", err)
		}
		if sum.Source, err = parseSource(source); err != nil {
			return nil, err
		}
		if sum.State, err = core.ParseRunState(state); err != nil {
			return nil, fmt.Errorf("store: run %d: %w", sum.ID, err)
		}
		if sum.StartedAt, err = parseTime(started); err != nil {
			return nil, fmt.Errorf("store: run %d: %w", sum.ID, err)
		}
		switch o := core.Outcome(outcome); o {
		case core.OutcomePass, core.OutcomeFail, core.OutcomeUndetermined:
			sum.Outcome = o
		default:
			return nil, fmt.Errorf("store: run %d: unknown outcome %q", sum.ID, outcome)
		}
		sum.Features = []string{}
		if features.Valid && features.String != "" {
			sum.Features = strings.Split(features.String, "\x1f")
			sort.Strings(sum.Features)
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing runs: %w", err)
	}
	return out, nil
}
