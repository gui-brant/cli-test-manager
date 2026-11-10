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
	failed := 0
	for _, r := range run.Results {
		if r.Status.Failed() {
			failed++
		}
	}

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
			run.ExternalID, run.Attempt, run.CommitSHA, run.Diagnostic,
			string(verdict.Outcome), verdict.Reason, len(run.Results), failed,
		}
		if id != 0 {
			if _, err := conn.ExecContext(ctx, `UPDATE run SET
				source = ?, environment = ?, state = ?, started_at = ?, finished_at = ?, exit_code = ?,
				external_id = ?, attempt = ?, commit_sha = ?, diagnostic = ?,
				outcome = ?, reason = ?, total = ?, failed = ?
				WHERE id = ?`, append(fields, id)...); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `DELETE FROM result WHERE run_id = ?`, id); err != nil {
				return err
			}
		} else {
			res, err := conn.ExecContext(ctx, `INSERT INTO run (
				source, environment, state, started_at, finished_at, exit_code,
				external_id, attempt, commit_sha, diagnostic,
				outcome, reason, total, failed)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, fields...)
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
		for i, r := range run.Results {
			if _, err := stmt.ExecContext(ctx, id, i, r.TestID, r.Feature, r.Name, string(r.Status), int64(r.Duration), r.Message); err != nil {
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
	)
	err = tx.QueryRowContext(ctx, `SELECT id, source, environment, state, started_at, finished_at, exit_code,
		external_id, attempt, commit_sha, diagnostic FROM run `+where, args...).
		Scan(&run.ID, &source, &run.Environment, &state, &started, &finished, &run.ExitCode,
			&run.ExternalID, &run.Attempt, &run.CommitSHA, &run.Diagnostic)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Run{}, fmt.Errorf("store: run not found: %w", core.ErrNotFound)
	}
	if err != nil {
		return core.Run{}, fmt.Errorf("store: reading run: %w", err)
	}

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
	return run, nil
}

func parseSource(s string) (core.Source, error) {
	switch src := core.Source(s); src {
	case core.SourceLocal, core.SourceRemote:
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
