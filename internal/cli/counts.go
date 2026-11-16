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
	by := fs.String("by", "test", "what to count: test (how each test did) or feature (how often each feature's tests were played)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: tester counts [flags]

Shows how tests have done, counting a test once per run however many checks it
printed: its verdict in a run is the worst of its checks, so PASS, FAIL, ERROR
and SKIP add up to RUNS. Results that belong to no listed test are not counted.

  --by test     (default) one row per test.
  --by feature  one row per feature: TESTS the manifest lists in it, RUNS that
                played at least one of them, PLAYS (one for each test in each
                run) and how many of those plays FAILED. A feature never played
                shows zeros.

Flags:
`)
		fs.PrintDefaults()
	}
	if code, stop := a.parseFlags(fs, args, 0); stop {
		return code
	}
	if *by != "test" && *by != "feature" {
		return a.errorf("--by must be test or feature, not %q", *by)
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

	filter := core.StatsFilter{Feature: *feature, Environment: *env, Since: from}
	if *by == "feature" {
		return a.printFeatureCounts(ctx, loaded.Manifest, store, filter)
	}
	rows, err := (&query.Service{Store: store}).Counts(ctx, loaded.Manifest, filter)
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

// printFeatureCounts shows how often each feature's tests were played.
func (a *app) printFeatureCounts(ctx context.Context, m *core.Manifest, store core.Store, f core.StatsFilter) int {
	if f.Feature != "" && !m.HasFeature(f.Feature) {
		return a.errorf("feature %q is not defined", f.Feature)
	}
	rows, err := (&query.Service{Store: store}).Features(ctx, m, f)
	if err != nil {
		return a.errorf("%v", err)
	}
	t := newTable(a.Stdout)
	fmt.Fprintln(t, "FEATURE\tTESTS\tRUNS\tPLAYS\tFAILED\tLAST RUN")
	for _, r := range rows {
		last := "-"
		if !r.LastRunAt.IsZero() {
			last = a.when(r.LastRunAt)
		}
		fmt.Fprintf(t, "%s\t%d\t%d\t%d\t%d\t%s\n", r.Feature, r.Tests, r.Runs, r.Plays, r.Failed, last)
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
