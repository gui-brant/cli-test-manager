package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/query"
)

func (a *app) cmdCounts(ctx context.Context, manifestPath string, args []string) int {
	fs := a.flags("counts")
	env := fs.String("env", "", "only runs against this environment")
	feature := fs.String("feature", "", "only results of this feature")
	since := fs.String("since", "", "only runs started on or after this date (2026-11-01) or time (RFC 3339)")
	if code, stop := a.parseFlags(fs, args, 0); stop {
		return code
	}

	var from time.Time
	if *since != "" {
		t, err := a.parseSince(*since)
		if err != nil {
			return a.errorf("%v", err)
		}
		from = t
	}

	loaded, err := a.load(manifestPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	store, err := a.openIfExists(loaded)
	if err != nil {
		return a.errorf("%v", err)
	}
	if store == nil {
		fmt.Fprintln(a.Stdout, "No runs yet.")
		return ExitOK
	}
	defer store.Close()

	rows, err := (&query.Service{Store: store}).Counts(ctx, loaded.Manifest, core.StatsFilter{Feature: *feature, Environment: *env, Since: from})
	if err != nil {
		return a.errorf("%v", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.Stdout, "No results match.")
		return ExitOK
	}

	t := newTable(a.Stdout)
	fmt.Fprintln(t, "#\tID\tNAME\tRUNS\tPASS\tFAIL\tERROR\tSKIP\tLAST\tLAST RUN")
	for _, r := range rows {
		num := "-"
		if r.Number > 0 {
			num = fmt.Sprintf("%d", r.Number)
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
			num, r.TestID, r.Name, r.Runs, r.Passed, r.Failed, r.Errored, r.Skipped,
			strings.ToUpper(string(r.LastStatus)), a.when(r.LastRunAt))
	}
	t.Flush()
	return ExitOK
}

// parseSince reads a date (in the shown time zone) or an RFC 3339 time.
func (a *app) parseSince(s string) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02", s, a.loc); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q is not a date like 2026-11-01 or a time like 2026-11-01T09:30:00Z", s)
}
