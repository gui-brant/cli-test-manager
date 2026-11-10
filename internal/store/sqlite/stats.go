package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// TestStats returns per-test counts across runs, ordered by test id. Results
// with no test id (unlisted ones) are not counted. A test's name, feature and
// last status are those of its most recent result, as recorded when it was
// saved.
func (s *Store) TestStats(ctx context.Context, f core.StatsFilter) ([]core.TestStat, error) {
	where := []string{`x.test_id <> ''`}
	var args []any
	if f.Feature != "" {
		where = append(where, `x.feature = ?`)
		args = append(args, f.Feature)
	}
	if f.Environment != "" {
		where = append(where, `r.environment = ?`)
		args = append(args, f.Environment)
	}
	if !f.Since.IsZero() {
		where = append(where, `r.started_at >= ?`)
		args = append(args, formatTime(f.Since))
	}

	q := `WITH base AS (
			SELECT x.test_id, x.name, x.feature, x.status, r.id AS run_id, r.started_at,
			       ROW_NUMBER() OVER (PARTITION BY x.test_id
			                          ORDER BY r.started_at DESC, r.id DESC, x.position DESC) AS rn
			FROM result x JOIN run r ON r.id = x.run_id
			WHERE ` + strings.Join(where, ` AND `) + `
		)
		SELECT test_id,
		       MAX(CASE WHEN rn = 1 THEN name END),
		       MAX(CASE WHEN rn = 1 THEN feature END),
		       COUNT(DISTINCT run_id),
		       SUM(status = 'pass'), SUM(status = 'fail'), SUM(status = 'error'), SUM(status = 'skip'),
		       MAX(CASE WHEN rn = 1 THEN started_at END),
		       MAX(CASE WHEN rn = 1 THEN status END)
		FROM base GROUP BY test_id ORDER BY test_id`

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
