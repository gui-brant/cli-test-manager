package cli

import (
	"context"
	"errors"
	"fmt"
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
	if code, stop := a.parseFlags(fs, args, 0); stop {
		return code
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

	svc := &execute.Service{Runner: a.Runner, Creds: a.Creds, Environ: a.Environ, Now: a.Now}
	req := execute.Request{
		Manifest:    m,
		Root:        loaded.Root,
		Environment: envName,
		Select:      execute.Selection{TestIDs: ids, Feature: *feature},
		Progress:    a.progress,
	}

	// Check everything first. Nobody should be asked "Are you sure?" about a run
	// that cannot start, and a request that cannot start must not create the
	// history file.
	preview, err := svc.Preview(ctx, req)
	if err != nil {
		return a.errorf("%v", err)
	}
	a.remember(loaded, *yes) // the request is sound, so this project is being used; --yes asks nothing

	// A run of every test asks first, but only a person at a terminal can
	// answer: a push hook's input is a pipe, so a hook is never asked.
	if len(ids) == 0 && *feature == "" && !*yes && a.IsTerminal {
		if !a.confirm(confirmationQuestion(preview, envName)) {
			return a.errorf("not confirmed; nothing was run")
		}
	}

	store, err := sqlite.Open(loaded.HistoryPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	defer store.Close()

	svc.Store = store
	rep, err := svc.Run(ctx, req)
	return a.finishRun(m, rep, err)
}

// confirmationQuestion says what a run will really do, so the count matches
// what happens: tests that would be skipped are not counted as run.
func confirmationQuestion(p execute.Preview, env string) string {
	if n := len(p.Skipped); n > 0 {
		return fmt.Sprintf("Run %d of %d tests in environment %q (%d skipped)? [y/N] ", len(p.Tests), len(p.Tests)+n, env, n)
	}
	return fmt.Sprintf("Run all %d tests in environment %q? [y/N] ", len(p.Tests), env)
}

// confirm asks a yes/no question. Anything but yes is no. The question goes to
// standard error, so piping or redirecting the run's output never hides it.
func (a *app) confirm(prompt string) bool {
	fmt.Fprint(a.Stderr, prompt)
	line, ok := a.readLine()
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
	line := fmt.Sprintf("  environment %s, %s, %d failed", run.Environment, plural(len(run.Results), "result", "results"), failed)
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
			fmt.Fprintf(a.Stdout, "  ... and %d more failed results\n", failed-shown)
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
	if rep.Saved {
		fmt.Fprintf(a.Stdout, "Details: tester history %d\n", rep.RunID)
	}
}
