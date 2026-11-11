package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/query"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

func (a *app) cmdHistory(ctx context.Context, manifestPath string, args []string) int {
	fs := a.flags("history")
	env := fs.String("env", "", "only runs against this environment")
	feature := fs.String("feature", "", "only runs that include this feature")
	source := fs.String("source", "", "only runs from this source: local or remote")
	limit := fs.Int("limit", 0, "show at most this many runs")
	offset := fs.Int("offset", 0, "skip this many runs")
	detail := fs.Bool("detail", false, "with a run number: also show the test tool's own detail on the failed results "+
		"(stack traces, error objects). It is long and mostly useful to whoever writes the tests, so it is hidden unless asked for")
	// "tester history -h" explains what a run's view is made of; the general help
	// does not repeat it.
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: tester history [flags]
       tester history <run number> [--detail]

With no run number it lists runs, newest first. With one it shows that run in full.

What "history <run number>" shows, in order:
  Results       every result; a failed one has its short message under it
  Diagnostic    what the commands themselves printed when one crashed, timed out
                or exited with an error (only when there is something to show)
  Tool detail   only with --detail: the test tool's own stack traces and error
                objects for the failed results

Flags:
`)
		fs.PrintDefaults()
	}
	if code, stop := a.parseFlags(fs, args, 100); stop {
		return code
	}
	rest := fs.Args()
	if len(rest) > 1 {
		// Flags may also follow the run number: "history 7 --detail".
		if code, stop := a.parseFlags(fs, rest[1:], 0); stop {
			return code
		}
		rest = rest[:1]
	}
	if *detail && len(rest) == 0 {
		return a.errorf("--detail shows one run in full: tester history <run number> --detail")
	}

	loaded, err := a.load(manifestPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	// Reading a project's history means this project is being used.
	if a.remember(ctx, loaded, false) {
		return ExitCancelled
	}
	store, err := a.openIfExists(loaded)
	if err != nil {
		return a.errorf("%v", err)
	}
	if store != nil {
		defer store.Close()
	}

	if len(rest) == 1 {
		return a.showRun(ctx, loaded, store, rest[0], *detail)
	}
	if store == nil {
		fmt.Fprintln(a.Stdout, "No runs yet.")
		return ExitOK
	}

	var src core.Source
	switch *source {
	case "":
	case "local", "remote":
		src = core.Source(*source)
	default:
		return a.errorf("--source must be local or remote, not %q", *source)
	}
	rows, err := (&query.Service{Store: store}).History(ctx, core.RunFilter{
		Environment: *env, Feature: *feature, Source: src, Limit: *limit, Offset: *offset,
	})
	if err != nil {
		return a.errorf("%v", err)
	}
	if len(rows) == 0 {
		if *env != "" || *feature != "" || *source != "" || *offset != 0 {
			fmt.Fprintln(a.Stdout, "No runs match.")
		} else {
			fmt.Fprintln(a.Stdout, "No runs yet.")
		}
		return ExitOK
	}

	t := newTable(a.Stdout)
	fmt.Fprintln(t, "ID\tWHEN\tSOURCE\tENV\tRESULT\tFAILED\tFEATURES\tREASON")
	for _, r := range rows {
		reason := shorten(r.Reason, 60)
		if r.Outcome == core.OutcomeFail {
			// Point at where the causes are, instead of showing one of them.
			reason += fmt.Sprintf("; details: tester history %d", r.ID)
		}
		fmt.Fprintf(t, "%d\t%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\n",
			r.ID, a.when(r.StartedAt), r.Source, dash(r.Environment), resultWord(r.State, r.Outcome),
			r.Failed, r.Total, dash(strings.Join(r.Features, ",")), reason)
	}
	t.Flush()
	return ExitOK
}

func (a *app) showRun(ctx context.Context, loaded *manifestfile.Loaded, store *sqlite.Store, arg string, detail bool) int {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return a.errorf("%q is not a run number", arg)
	}
	if store == nil {
		return a.errorf("run %d not found: there is no history yet", id)
	}
	d, err := (&query.Service{Store: store}).Run(ctx, loaded.Manifest, id)
	if errors.Is(err, core.ErrNotFound) {
		return a.errorf("run %d not found", id)
	}
	if err != nil {
		return a.errorf("%v", err)
	}

	run := d.Run
	fmt.Fprintf(a.Stdout, "Run %d  %s\n", id, resultWord(run.State, d.Verdict.Outcome))
	if d.Verdict.Reason != "" {
		fmt.Fprintf(a.Stdout, "  reason       %s\n", d.Verdict.Reason)
	}
	fmt.Fprintf(a.Stdout, "  when         %s to %s\n", a.when(run.StartedAt), a.when(run.FinishedAt))
	fmt.Fprintf(a.Stdout, "  source       %s, environment %s\n", run.Source, dash(run.Environment))
	if run.ExitCode != 0 {
		fmt.Fprintf(a.Stdout, "  exit code    %d\n", run.ExitCode)
	}
	if run.ExternalID != "" {
		fmt.Fprintf(a.Stdout, "  remote run   %s, attempt %d, commit %s\n", run.ExternalID, run.Attempt, dash(run.CommitSHA))
	}

	fmt.Fprintln(a.Stdout, "\nResults:")
	if len(d.Results) == 0 {
		fmt.Fprintln(a.Stdout, "  (none)")
	}
	width := 0
	for _, r := range d.Results {
		width = max(width, len(dash(r.TestID)))
	}
	for _, r := range d.Results {
		num := "-"
		if r.Number > 0 {
			num = fmt.Sprintf("#%d", r.Number)
		}
		fmt.Fprintf(a.Stdout, "  %-4s %-5s %-*s  %s\n", num, strings.ToUpper(string(r.Status)), width, dash(r.TestID), r.Name)
		if strings.TrimSpace(r.Message) != "" {
			fmt.Fprintln(a.Stdout, indent(strings.TrimSpace(r.Message), "        "))
		}
	}
	if strings.TrimSpace(run.Diagnostic) != "" {
		fmt.Fprintln(a.Stdout, "\nDiagnostic:")
		fmt.Fprintln(a.Stdout, indent(run.Diagnostic, "  "))
	}
	if strings.TrimSpace(run.Detail) != "" {
		if detail {
			fmt.Fprintln(a.Stdout, "\nTool detail:")
			fmt.Fprintln(a.Stdout, indent(run.Detail, "  "))
		} else {
			// Say that it exists and how to see it, so nobody has to guess.
			fmt.Fprintf(a.Stdout, "\nThe test tool gave more detail on the failed results (stack traces, error objects).\nShow it with: tester history %d --detail\n", id)
		}
	}
	return ExitOK
}
