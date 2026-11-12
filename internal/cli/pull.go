package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/pull"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

func (a *app) cmdPull(ctx context.Context, manifestPath string, args []string) int {
	fs := a.flags("pull")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: tester pull

Brings in the results of runs a CI service did the work for. The manifest's
pull section says where: the repository, the workflow, the name of the result
file it attaches, which environment those results belong to, how many days back
to look the first time, and the NAME of the environment variable that holds the
service's token. The value is read from your environment and never stored.

It looks at the workflow's runs, newest first, and stops at the first one it
already has, so running it again brings in only what is new. A run that is
still going is kept as a placeholder and checked again next time. Pulled runs
show in "tester history" as remote.

Exit codes: 0 pulled (whatever the pulled tests did), 2 could not pull,
130 cancelled.
`)
	}
	if code, stop := a.parseFlags(fs, args, 0); stop {
		return code
	}

	loaded, err := a.load(manifestPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	m := loaded.Manifest
	spec := m.Pull
	if spec == nil {
		return a.errorf("this project's manifest has no pull section, so there is nothing to pull (examples/test-manifest.yaml shows one)")
	}
	if a.NewSource == nil {
		return a.errorf("pulling is not available in this build")
	}

	token, scrub, err := pull.Prepare(ctx, a.Creds, m)
	if err != nil {
		return a.errorf("%v", err)
	}

	store, err := sqlite.Open(loaded.HistoryPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	defer store.Close()

	svc := &pull.Service{Source: a.NewSource(token), Store: store, Redactor: scrub, Now: a.Now}
	rep, err := svc.Pull(ctx, pull.Request{Manifest: m, Progress: a.pullProgress})

	switch {
	case err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		fmt.Fprintf(a.Stderr, "tester: cancelled; the runs already stored are kept\n")
		return ExitCancelled
	case err != nil:
		if rep.New()+rep.Pending > 0 {
			a.printPullSummary(rep, spec.LookbackDays)
		}
		fmt.Fprintf(a.Stderr, "tester: %s\n", scrub.Redact(err.Error()))
		if rep.New() > 0 {
			fmt.Fprintln(a.Stderr, "tester: the runs above were stored; pulling again continues from there")
		}
		return ExitError
	}
	a.printPullSummary(rep, spec.LookbackDays)
	return ExitOK
}

// pullProgress prints one line for each run as it is taken in.
func (a *app) pullProgress(ev pull.Event) {
	head := fmt.Sprintf("run %s#%d (%s)", ev.Run.ExternalID, ev.Run.Attempt, a.when(ev.Run.CreatedAt))
	switch ev.Outcome {
	case pull.Stored:
		line := fmt.Sprintf("%s  stored as run %d: %s", head, ev.RunID, plural(ev.Results, "result", "results"))
		if ev.Unmatched > 0 {
			line += fmt.Sprintf(" (%d matched no test)", ev.Unmatched)
		}
		fmt.Fprintln(a.Stdout, line)
	case pull.Pending:
		fmt.Fprintf(a.Stdout, "%s  still going; it will be checked again at the next pull\n", head)
	case pull.Expired:
		fmt.Fprintf(a.Stdout, "%s  the result file has expired; kept as a gap (run %d)\n", head, ev.RunID)
	case pull.Unusable:
		fmt.Fprintf(a.Stdout, "%s  stored as failed (run %d): %s\n", head, ev.RunID, shorten(ev.Note, 100))
	}
}

func (a *app) printPullSummary(rep pull.Report, lookbackDays int) {
	if rep.New() == 0 && rep.Pending == 0 {
		if rep.AlreadyHad > 0 {
			fmt.Fprintln(a.Stdout, "Nothing new: every run is already in the history.")
		} else {
			fmt.Fprintf(a.Stdout, "No runs of the workflow in the last %d days.\n", lookbackDays)
		}
		return
	}
	var parts []string
	if rep.Stored > 0 {
		parts = append(parts, fmt.Sprintf("%d stored", rep.Stored))
	}
	if rep.Expired > 0 {
		parts = append(parts, fmt.Sprintf("%d expired", rep.Expired))
	}
	if rep.Unusable > 0 {
		parts = append(parts, fmt.Sprintf("%d stored as failed", rep.Unusable))
	}
	if rep.Pending > 0 {
		parts = append(parts, fmt.Sprintf("%d still going", rep.Pending))
	}
	fmt.Fprintf(a.Stdout, "\nPulled: %s.\n", strings.Join(parts, ", "))
	if rep.Unmatched > 0 {
		fmt.Fprintf(a.Stdout, "%s matched no test of the manifest and were kept without a test. "+
			"A result matches a test when its suite or class name is the test's id or ends with its file path.\n",
			plural(rep.Unmatched, "result", "results"))
	}
	if rep.New() > 0 {
		fmt.Fprintln(a.Stdout, "See them with: tester history --source remote")
	}
}
