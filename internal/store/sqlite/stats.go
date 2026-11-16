package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// verdictSQL is a test's verdict in one run, worked out from its checks: the
// worst of them. It is the SQL form of core.WorstStatus, and a test that
// printed many checks in a run counts once.
const verdictSQL = `CASE WHEN SUM(status = 'error') > 0 THEN 'error'
	WHEN SUM(status = 'fail') > 0 THEN 'fail'
	WHEN SUM(status = 'pass') > 0 THEN 'pass'
	ELSE 'skip' END`

// statsWhere builds the conditions every statistic shares. Results with no test
// id (unlisted ones) are never counted.
func statsWhere(f core.StatsFilter) (where string, args []any) {
	conds := []string{`x.test_id <> ''`}
	if f.Feature != "" {
		conds = append(conds, `x.feature = ?`)
		args = append(args, f.Feature)
	}
	if f.Environment != "" {
		conds = append(conds, `r.environment = ?`)
		args = append(args, f.Environment)
	}
	if !f.Since.IsZero() {
		conds = append(conds, `r.started_at >= ?`)
		args = append(args, formatTime(f.Since))
	}
	return strings.Join(conds, ` AND `), args
}

// TestStats returns per-test counts across runs, ordered by test id. A test
// counts once per run: its verdict there is the worst of its checks, so a file
// that prints 42 checks adds one pass, not 42. Results with no test id
// (unlisted ones) are not counted. A test's name and feature are those of its
// most recent result, and its last status is its verdict in its most recent
// run, as recorded when the results were saved.
func (s *Store) TestStats(ctx context.Context, f core.StatsFilter) ([]core.TestStat, error) {
	where, args := statsWhere(f)

	q := `WITH lines AS (
			SELECT x.test_id, x.name, x.feature, x.status, x.position, r.id AS run_id, r.started_at
			FROM result x JOIN run r ON r.id = x.run_id
			WHERE ` + where + `
		),
		plays AS (
			SELECT test_id, run_id, started_at, ` + verdictSQL + ` AS verdict,
			       ROW_NUMBER() OVER (PARTITION BY test_id ORDER BY started_at DESC, run_id DESC) AS rn
			FROM lines GROUP BY test_id, run_id
		),
		last_line AS (
			SELECT test_id, name, feature,
			       ROW_NUMBER() OVER (PARTITION BY test_id ORDER BY started_at DESC, run_id DESC, position DESC) AS ln
			FROM lines
		)
		SELECT p.test_id,
		       (SELECT l.name FROM last_line l WHERE l.test_id = p.test_id AND l.ln = 1),
		       (SELECT l.feature FROM last_line l WHERE l.test_id = p.test_id AND l.ln = 1),
		       COUNT(*),
		       SUM(p.verdict = 'pass'), SUM(p.verdict = 'fail'), SUM(p.verdict = 'error'), SUM(p.verdict = 'skip'),
		       MAX(CASE WHEN p.rn = 1 THEN p.started_at END),
		       MAX(CASE WHEN p.rn = 1 THEN p.verdict END)
		FROM plays p GROUP BY p.test_id ORDER BY p.test_id`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: reading test stats: %w", err)
	}
	defer rows.Close()

	var out []core.TestStat
	for rows.Next() {
		var (
			st                  core.TestStat
			name, feature       sql.NullString
			lastRun, lastStatus sql.NullString
		)
		if err := rows.Scan(&st.TestID, &name, &feature, &st.Runs,
			&st.Passed, &st.Failed, &st.Errored, &st.Skipped, &lastRun, &lastStatus); err != nil {
			return nil, fmt.Errorf("store: reading test stats: %w", err)
		}
		st.Name, st.Feature = name.String, feature.String
		if st.LastRunAt, err = parseTime(lastRun.String); err != nil {
			return nil, fmt.Errorf("store: test %q: %w", st.TestID, err)
		}
		if st.LastStatus, err = core.ParseStatus(lastStatus.String); err != nil {
			return nil, fmt.Errorf("store: test %q: %w", st.TestID, err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading test stats: %w", err)
	}
	return out, nil
}

// FeatureStats returns per-feature counts, ordered by feature name: how many
// runs played at least one of the feature's tests, how many times its tests
// were played (one for each test in each run) and how many of those plays
// failed. Features that were never played do not appear; the caller adds them
// from the manifest. Results with no test or no feature are not counted.
func (s *Store) FeatureStats(ctx context.Context, f core.StatsFilter) ([]core.FeatureStat, error) {
	where, args := statsWhere(f)

	q := `WITH lines AS (
			SELECT x.test_id, x.feature, x.status, r.id AS run_id, r.started_at
			FROM result x JOIN run r ON r.id = x.run_id
			WHERE ` + where + ` AND x.feature <> ''
		),
		plays AS (
			SELECT test_id, run_id, MAX(feature) AS feature, MAX(started_at) AS started_at, ` + verdictSQL + ` AS verdict
			FROM lines GROUP BY test_id, run_id
		)
		SELECT feature, COUNT(DISTINCT run_id), COUNT(*),
		       SUM(verdict IN ('fail', 'error')), MAX(started_at)
		FROM plays GROUP BY feature ORDER BY feature`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: reading feature stats: %w", err)
	}
	defer rows.Close()

	var out []core.FeatureStat
	for rows.Next() {
		var (
			st      core.FeatureStat
			lastRun string
		)
		if err := rows.Scan(&st.Feature, &st.Runs, &st.Plays, &st.Failed, &lastRun); err != nil {
			return nil, fmt.Errorf("store: reading feature stats: %w", err)
		}
		if st.LastRunAt, err = parseTime(lastRun); err != nil {
			return nil, fmt.Errorf("store: feature %q: %w", st.Feature, err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading feature stats: %w", err)
	}
	return out, nil
}
