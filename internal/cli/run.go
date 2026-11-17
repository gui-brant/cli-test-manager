package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/execute"
	"github.com/gui-brant/cli-test-manager/internal/query"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

// maxFailuresShown is how many failed results a run summary lists. The rest
// are in `tester history <id>`.
const maxFailuresShown = 20

func (a *app) cmdRun(ctx context.Context, manifestPath string, args []string) int {
	fs := a.flags("run")
	envFlag := fs.String("env", "", "the environment to run against")
	feature := fs.String("feature", "", "run only the tests of this feature")
	var ids stringList
	fs.Var(&ids, "test", "run only this test; repeat it to run several")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	trigger := fs.String("trigger", string(core.TriggerManual),
		"what started this run, recorded with it: manual (a person) or hook (a git hook or script)")
	var changed stringList
	fs.Var(&changed, "changed", "run only the tests of the features this changed file selects (a path relative to the project); "+
		"repeat it, or use - to read one path per line from standard input")
	unplayed := fs.Bool("unplayed", false, "leave out the tests that already passed in this environment with their file as it is now")
	dryRun := fs.Bool("dry-run", false, "say which tests would run, and run nothing: no command is started and nothing is saved")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: tester run [flags]

Runs tests and saves the run. With no restriction it runs every test of the
environment (and asks first, at a terminal). These flags narrow it, and
combine: a test runs only if it passes every one.

  --test ID         only this test (repeat for several)
  --feature NAME    only the tests of this feature
  --changed PATH    only the tests of the features this changed file selects.
                    Repeat it, or write --changed - to read one path per line
                    from standard input (for example from "git diff --name-only").
                    Paths are relative to the project. A file selects every
                    feature whose paths match it, and the feature of a test
                    whose file it is; the manifest selects every feature; a file
                    that does none of that falls under general. Paths outside the
                    project are ignored. No changed files selects nothing.
  --unplayed        leave out every test that already passed in this
                    environment with its file as it is now (its content, not
                    its feature's other files). A test that failed, never ran,
                    or whose file changed is run. Each test left out is listed,
                    with the run that passed it.

When the narrowing leaves nothing to run (nothing changed, or everything
already passed) that is a success: it says so, exits 0, and saves nothing. A
request that cannot run at all (a bad environment, a missing secret) exits 2.

  --dry-run         decide what would run and say so, and stop: no command is
                    started, nothing is saved, no history file is created, and
                    nothing is asked. It prints one line "would run  <test id>"
                    for each test, so a script can look for those lines (for
                    example to prepare something only when a test will run).
                    It exits 0 whether or not there is anything to run, and 2
                    when the request could not run.

Flags:
`)
		fs.PrintDefaults()
	}
	if code, stop := a.parseFlags(fs, args, 0); stop {
		return code
	}
	switch core.Trigger(*trigger) {
	case core.TriggerManual, core.TriggerHook:
	default:
		return a.errorf("--trigger must be %q or %q, not %q", core.TriggerManual, core.TriggerHook, *trigger)
	}
	useChanged := len(changed) > 0
	changedFiles, err := a.changedFiles(changed)
	if err != nil {
		return a.errorf("%v", err)
	}

	loaded, err := a.load(manifestPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	m := loaded.Manifest
	envName, err := chooseEnvironment(m, *envFlag)
	if err != nil {
		return a.errorf("%v", err)
	}

	svc := &execute.Service{Runner: a.Runner, Creds: a.Creds, Environ: a.Environ, Now: a.Now, Checkout: a.Checkout, Tree: a.Tree}
	req := execute.Request{
		Manifest:    m,
		Root:        loaded.Root,
		Environment: envName,
		Select: execute.Selection{
			TestIDs: ids, Feature: *feature,
			UseChanged: useChanged, Changed: changedFiles, Unplayed: *unplayed,
		},
		Trigger:      core.Trigger(*trigger),
		ManifestRaw:  loaded.Raw,
		ManifestFile: loaded.File(),
		Progress:     a.progress,
	}

	// Which tests have already passed is read from the history, if there is one.
	// Looking never creates it.
	var store *sqlite.Store
	if *unplayed {
		if store, err = a.openIfExists(loaded); err != nil {
			return a.errorf("%v", err)
		}
		if store != nil {
			defer store.Close()
			svc.Store = store
		}
	}

	// Check everything first. Nobody should be asked "Are you sure?" about a run
	// that cannot start, and a request that cannot start must not create the
	// history file.
	preview, err := svc.Preview(ctx, req)
	if err != nil {
		return a.errorf("%v", err)
	}
	// The request is sound, so this project is being used; --yes and --dry-run ask nothing.
	if a.remember(ctx, loaded, *yes || *dryRun) {
		return ExitCancelled
	}

	a.printChanged(preview.Changed)
	if len(preview.Tests) == 0 {
		a.printNothingToRun(preview, envName)
		return ExitOK
	}
	if *dryRun {
		if len(preview.Refused) > 0 {
			fmt.Fprintf(a.Stderr, "tester: a run would not start: %s not met:\n\n%s\n\nDry run: nothing was started or saved.\n",
				plural(len(preview.Refused), "precondition is", "preconditions are"), execute.RefusalText(preview.Refused))
			return ExitError
		}
		for _, sk := range preview.Skipped {
			fmt.Fprintf(a.Stdout, "skipped  %s  (%s)\n", sk.Test.ID, sk.Reason)
		}
		for _, t := range preview.Tests {
			fmt.Fprintf(a.Stdout, "would run  %s\n", t.ID)
		}
		fmt.Fprintf(a.Stdout, "Dry run: %s would run in %s; nothing was started or saved.\n", plural(len(preview.Tests), "test", "tests"), envName)
		return ExitOK
	}

	// A run of every test asks first, but only a person at a terminal can
	// answer: a push hook's input is a pipe, so a hook is never asked. A run that
	// is narrowed by --test, --feature, --changed or --unplayed is not "every test".
	// A run that cannot start (a precondition is not met) is never asked about.
	if len(ids) == 0 && *feature == "" && !useChanged && !*unplayed && !*yes && a.IsTerminal && len(preview.Refused) == 0 {
		if !a.confirm(ctx, confirmationQuestion(preview, envName)) {
			if ctx.Err() != nil { // Ctrl-C at the question is a cancel, not a "no"
				fmt.Fprintln(a.Stderr, "\ntester: cancelled; nothing was run")
				return ExitCancelled
			}
			return a.errorf("not confirmed; nothing was run")
		}
	}

	if store == nil {
		if store, err = sqlite.Open(loaded.HistoryPath); err != nil {
			return a.errorf("%v", err)
		}
		defer store.Close()
		svc.Store = store
	}

	rep, err := svc.Run(ctx, req)
	return a.finishRun(m, rep, err)
}

// changedFiles collects the paths given with --changed: each value is a path,
// except "-", which stands for one path per line on standard input.
func (a *app) changedFiles(values []string) ([]string, error) {
	var files []string
	for _, v := range values {
		if v != "-" {
			files = append(files, v)
			continue
		}
		if a.Stdin == nil {
			return nil, errors.New("--changed - reads paths from standard input, and there is none")
		}
		data, err := io.ReadAll(io.LimitReader(a.Stdin, 16<<20))
		if err != nil {
			return nil, fmt.Errorf("reading the changed paths from standard input: %w", err)
		}
		files = append(files, strings.Split(string(data), "\n")...)
	}
	return files, nil
}

// printChanged says which features the changed files selected, and why, so a
// run that did less than "everything" shows what it chose.
func (a *app) printChanged(c *execute.ChangedSummary) {
	if c == nil {
		return
	}
	line := fmt.Sprintf("Changed files: %d", c.Files)
	switch {
	case c.Manifest:
		line += ", including the manifest, which selects every feature"
	case c.InNoFeature > 0:
		line += fmt.Sprintf(" (%d in no feature, so general)", c.InNoFeature)
	}
	if len(c.Features) > 0 {
		line += "; they select: " + strings.Join(c.Features, ", ")
	} else {
		line += "; they select no feature"
	}
	if c.Outside > 0 {
		line += fmt.Sprintf(" (%s outside the project, ignored)", plural(c.Outside, "path", "paths"))
	}
	fmt.Fprintln(a.Stdout, line)
}

// printNothingToRun explains a run that has nothing to do, and lists the tests
// that were left out and why. It is a success: the exit code is 0.
func (a *app) printNothingToRun(p execute.Preview, env string) {
	for _, sk := range p.Skipped {
		fmt.Fprintf(a.Stdout, "skipped  %s  (%s)\n", sk.Test.ID, sk.Reason)
	}
	switch {
	case p.Changed != nil && len(p.Changed.Features) == 0:
		fmt.Fprintln(a.Stdout, "Nothing to run: no changed file selects a feature.")
	case len(p.Skipped) > 0:
		fmt.Fprintf(a.Stdout, "Nothing to run: every selected test has already passed in %s with its current file.\n", env)
	default:
		fmt.Fprintln(a.Stdout, "Nothing to run: no test belongs to the features selected.")
	}
}

// confirmationQuestion is what the run would really do, as a question.
func confirmationQuestion(p execute.Preview, env string) string {
	return p.Describe(env) + "? [y/N] "
}

// confirm asks a yes/no question. Anything but yes is no. The question goes to
// standard error, so piping or redirecting the run's output never hides it.
func (a *app) confirm(ctx context.Context, prompt string) bool {
	fmt.Fprint(a.Stderr, prompt)
	line, ok := a.readLine(ctx)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// progress prints one line per test as the run goes, and a line for each test
// that is skipped, with the reason.
func (a *app) progress(ev execute.Event) {
	switch ev.Kind {
	case execute.EventSkipped:
		fmt.Fprintf(a.Stdout, "skipped  %s  (%s)\n", ev.Test.ID, ev.Reason)
	case execute.EventStarted:
		fmt.Fprintf(a.Stdout, "[%d/%d] %s ... ", ev.Index, ev.Total, ev.Test.ID)
	case execute.EventFinished:
		if ev.Failed {
			fmt.Fprintf(a.Stdout, "FAILED: %s\n", shorten(ev.Reason, 100))
		} else {
			fmt.Fprintln(a.Stdout, "ok")
		}
	}
}

func (a *app) finishRun(m *core.Manifest, rep execute.Report, err error) int {
	switch {
	case err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		fmt.Fprintln(a.Stdout) // end the unfinished progress line
		fmt.Fprintf(a.Stderr, "tester: %v\n", err)
		return ExitCancelled
	case len(rep.Refused) > 0:
		return a.finishRefused(rep, err)
	case err != nil && len(rep.Run.Results) == 0:
		return a.errorf("%v", err) // a problem found before anything ran
	}

	a.printSummary(m, rep)
	if err != nil { // the tests ran but the run could not be saved
		fmt.Fprintf(a.Stderr, "tester: %v\n", err)
		return ExitError
	}
	if rep.Run.Verdict().Outcome == core.OutcomePass {
		return ExitOK
	}
	return ExitFailed
}

// finishRefused reports a run that was not started because something the person
// must do first has not been done: what to do, then where the refusal was
// recorded. The tester never does it for them, and it exits 2, so a push hook
// stops the push.
func (a *app) finishRefused(rep execute.Report, err error) int {
	fmt.Fprintf(a.Stderr, "tester: nothing was run: %s not met:\n\n%s\n", plural(len(rep.Refused), "precondition is", "preconditions are"), execute.RefusalText(rep.Refused))
	if rep.Saved {
		fmt.Fprintf(a.Stderr, "\nRecorded as run %d (tester history %d). Do what it says, then run again.\n", rep.RunID, rep.RunID)
	}
	if err != nil { // the refusal could not be recorded
		fmt.Fprintf(a.Stderr, "tester: %v\n", err)
	}
	return ExitError
}

func (a *app) printSummary(m *core.Manifest, rep execute.Report) {
	run := rep.Run
	v := run.Verdict()
	word := "PASSED"
	if v.Outcome != core.OutcomePass {
		word = "FAILED"
	}
	head := fmt.Sprintf("Run %d: %s", rep.RunID, word)
	if !rep.Saved {
		head = "Run (NOT saved): " + word
	}
	if v.Reason != "" {
		head += " (" + shorten(v.Reason, 100) + ")"
	}

	failed := 0
	for _, r := range run.Results {
		if r.Status.Failed() {
			failed++
		}
	}
	// The run counts tests; a test that printed several checks counts once, and
	// the checks are said beside it.
	tests, failedTests := run.Counts()
	line := fmt.Sprintf("  environment %s, %s, %d failed", run.Environment, plural(tests, "test", "tests"), failedTests)
	if len(run.Results) != tests {
		line += fmt.Sprintf(" (%s)", plural(len(run.Results), "check", "checks"))
	}
	if n := len(rep.Skipped); n > 0 {
		line += fmt.Sprintf(", %s skipped (listed above)", plural(n, "test", "tests"))
	}
	fmt.Fprintf(a.Stdout, "\n%s\n%s\n", head, line)

	numbers := query.Numbers(m)
	shown := 0
	for _, r := range run.Results {
		if !r.Status.Failed() {
			continue
		}
		if shown == maxFailuresShown {
			fmt.Fprintf(a.Stdout, "  ... and %d more failed checks\n", failed-shown)
			break
		}
		shown++
		num := "-"
		if n := numbers[r.TestID]; n > 0 {
			num = fmt.Sprintf("#%d", n)
		}
		text := fmt.Sprintf("  %-5s %s %s  %s", strings.ToUpper(string(r.Status)), num, dash(r.TestID), r.Name)
		if msg := shorten(r.Message, 100); msg != "" {
			text += ": " + msg
		}
		fmt.Fprintln(a.Stdout, text)
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(a.Stdout, "Note: %s\n", n)
	}
	if rep.Saved {
		fmt.Fprintf(a.Stdout, "Details: tester history %d\n", rep.RunID)
	}
}
