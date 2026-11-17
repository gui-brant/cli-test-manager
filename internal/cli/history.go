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
	"github.com/gui-brant/cli-test-manager/internal/view"
)

func (a *app) cmdHistory(ctx context.Context, manifestPath string, args []string) int {
	fs := a.flags("history")
	env := fs.String("env", "", "only runs against this environment")
	feature := fs.String("feature", "", "only runs that include this feature")
	source := fs.String("source", "", "only runs from this source: local, remote or import")
	limit := fs.Int("limit", 0, "show at most this many runs")
	offset := fs.Int("offset", 0, "skip this many runs")
	detail := fs.Bool("detail", false, "with a run number: also show the test tool's own detail on the failed results "+
		"(stack traces, error objects). It is long and mostly useful to whoever writes the tests, so it is hidden unless asked for")
	checks := fs.Bool("checks", false, "with a run number: list every check of a test that printed several, "+
		"not only the failed ones. A test counts once whatever number of checks it printed")
	files := fs.Bool("files", false, "with a run number: list the files the run was made with (the manifest, the tests' files, the features' files)")
	file := fs.String("file", "", "with a run number: print one of those files as it was in the run, by its path")
	// "tester history -h" explains what a run's view is made of; the general help
	// does not repeat it.
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: tester history [flags]
       tester history <run number> [--checks] [--detail]
       tester history <run number> --files
       tester history <run number> --file <path>

With no run number it lists runs, newest first. With one it shows that run in full.

What "history <run number>" shows, in order:
  Results       one row per test, with its verdict. A test that printed several
                checks shows how many, and lists only its failed checks (with
                their short messages); --checks lists every check. A test with
                one check shows that check, as it always did. Results that
                belong to no listed test are rows of their own.
  Diagnostic    what the commands themselves printed when one crashed, timed out
                or exited with an error (only when there is something to show)
  Tool detail   only with --detail: the test tool's own stack traces and error
                objects for the failed results

The files a run was made with are kept in the history, as they were when it
started: the manifest, the file of each test that ran, and the project files
that match the path patterns of those tests' features. "--files" lists them
(what each is for, its size, the start of its hash, and why its text was not
kept when it was not); "--file <path>" prints one. Only runs the tester started
have them; a pulled or imported run happened elsewhere.

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
	if *checks && len(rest) == 0 {
		return a.errorf("--checks shows one run in full: tester history <run number> --checks")
	}
	if (*files || *file != "") && len(rest) == 0 {
		return a.errorf("--files and --file show one run's files: tester history <run number> --files")
	}
	if *files && *file != "" {
		return a.errorf("use --files to list a run's files or --file <path> to print one, not both")
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
		if *files || *file != "" {
			return a.showRunFiles(ctx, store, rest[0], *file)
		}
		return a.showRun(ctx, loaded, store, rest[0], *detail, *checks)
	}
	if store == nil {
		fmt.Fprintln(a.Stdout, "No runs yet.")
		return ExitOK
	}

	var src core.Source
	switch *source {
	case "":
	case "local", "remote", "import":
		src = core.Source(*source)
	default:
		return a.errorf("--source must be local, remote or import, not %q", *source)
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
			r.ID, a.when(r.StartedAt), r.Source, dash(r.Environment), view.ResultWord(r.State, r.Outcome),
			r.Failed, r.Total, dash(strings.Join(r.Features, ",")), reason)
	}
	t.Flush()
	return ExitOK
}

// showRunFiles lists the files a run was made with, or with a path prints one of
// them as it was.
func (a *app) showRunFiles(ctx context.Context, store *sqlite.Store, arg, path string) int {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return a.errorf("%q is not a run number", arg)
	}
	if store == nil {
		return a.errorf("run %d not found: there is no history yet", id)
	}
	q := &query.Service{Store: store}
	if path != "" {
		snap, err := q.RunFile(ctx, id, path)
		if err != nil {
			return a.errorf("%v", err)
		}
		fmt.Fprintf(a.Stdout, "file: %s   (%s)\n%s\n\n", snap.File.Path, snap.File.Role, view.SnapshotHeader(snap, a.loc))
		if !snap.File.Kept() {
			fmt.Fprintln(a.Stdout, view.NotKeptText(snap.File))
			return ExitOK
		}
		fmt.Fprint(a.Stdout, string(snap.File.Content))
		return ExitOK
	}
	run, err := store.GetRun(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		return a.errorf("run %d not found", id)
	}
	if err != nil {
		return a.errorf("%v", err)
	}
	fmt.Fprintf(a.Stdout, "Run %d  %s\n\n%s\n", id, view.SnapshotHeader(query.Snapshot{RunID: id, StartedAt: run.StartedAt, Commit: run.CommitSHA, Dirty: run.Dirty}, a.loc), view.FilesText(run.Files))
	if len(run.Files) > 0 {
		fmt.Fprintf(a.Stdout, "\nPrint one with: tester history %d --file <path>\n", id)
	}
	return ExitOK
}

func (a *app) showRun(ctx context.Context, loaded *manifestfile.Loaded, store *sqlite.Store, arg string, detail, checks bool) int {
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

	// The words are built by the view package, which the menu shares; this command
	// puts them in order and says how to see the detail.
	parts := view.BuildWith(d, id, a.loc, view.Options{Checks: checks})
	fmt.Fprintln(a.Stdout, parts.Header)
	fmt.Fprintln(a.Stdout)
	fmt.Fprintln(a.Stdout, parts.Results)
	if parts.HasHiddenChecks() {
		fmt.Fprintf(a.Stdout, "\n%s\nShow every check with: tester history %d --checks\n", view.ChecksHint, id)
	}
	if parts.Diagnostic != "" {
		fmt.Fprintln(a.Stdout)
		fmt.Fprintln(a.Stdout, parts.Diagnostic)
	}
	if parts.HasDetail() {
		fmt.Fprintln(a.Stdout)
		if detail {
			fmt.Fprintln(a.Stdout, parts.Detail)
		} else {
			// Say that it exists and how to see it, so nobody has to guess.
			fmt.Fprintf(a.Stdout, "%s\nShow it with: tester history %d --detail\n", parts.DetailNote, id)
		}
	}
	return ExitOK
}
