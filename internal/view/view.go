// Package view builds the text of what the program shows, so the command line
// and the menu use the same words. It builds text only: it prints nothing and
// reads nothing.
package view

import (
	"fmt"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/query"
)

// DetailHint is the first line of the note that says a run has detail from the
// test tool that is not shown. The command adds how to see it ("Show it with:
// tester history 8 --detail"); the menu says which key to press.
const DetailHint = "The test tool gave more detail on the failed results (stack traces, error objects)."

// Run is a run's view in its parts, each ready to print. A part has no trailing
// line break, and Diagnostic and Detail are empty when the run has none.
type Run struct {
	// Header is the run's number, result, reason, times, source and environment.
	Header string
	// Results is "Results:" and every result, a failed one with its short
	// message under it.
	Results string
	// Diagnostic is what the commands themselves printed when one crashed, timed
	// out or exited with an error. Empty when there is nothing to show.
	Diagnostic string
	// Detail is the test tool's own stack traces and error objects for the
	// failed results. Empty when the run has none. It is long, so a view shows
	// it only on request.
	Detail string
	// HiddenChecks is how many checks of tests that printed several are not
	// listed in Results, because they passed. Zero when Options.Checks was set
	// or every check is listed.
	HiddenChecks int
}

// Options change what a run's view lists.
type Options struct {
	// Checks lists every check of a test that printed several. Without it only
	// the failed ones are listed under the test.
	Checks bool
}

// commitText says which commit a run was made against: the short commit and,
// when the files had uncommitted changes, a note saying so. A run with no
// commit (a folder not under git, or a file imported without one) says that.
func commitText(run core.Run) string {
	if run.CommitSHA == "" {
		if run.Source == core.SourceImport {
			return "not given"
		}
		return "not recorded"
	}
	short := run.CommitSHA
	if len(short) > 7 {
		short = short[:7]
	}
	if run.Dirty {
		return short + " + uncommitted changes"
	}
	return short
}

// toolsText lists the tool versions a run used, one per line under a "tools"
// label, or says there are none.
func toolsText(tools []core.Tool) string {
	if len(tools) == 0 {
		return "  tools        not recorded\n"
	}
	width := 0
	for _, t := range tools {
		width = max(width, len(t.Name))
	}
	var b strings.Builder
	for i, t := range tools {
		label := "  tools       "
		if i > 0 {
			label = "              "
		}
		fmt.Fprintf(&b, "%s %-*s  %s\n", label, width, t.Name, t.Version)
	}
	return b.String()
}

// HasDetail says whether the run has tool detail to offer.
func (r Run) HasDetail() bool { return r.Detail != "" }

// HasHiddenChecks says whether some checks are left out of Results.
func (r Run) HasHiddenChecks() bool { return r.HiddenChecks > 0 }

// ChecksHint is the first line of the note that says some passing checks are
// not listed. The command adds how to see them; the menu says which key to
// press.
const ChecksHint = "Some tests printed several checks; only their failed ones are listed."

// Build makes the view of one run, listing only the failed checks of a test
// that printed several. id is the run's number, and loc the zone the times are
// shown in (nil means the local zone).
func Build(d query.RunDetail, id int64, loc *time.Location) Run {
	return BuildWith(d, id, loc, Options{})
}

// BuildWith is Build with options.
func BuildWith(d query.RunDetail, id int64, loc *time.Location, opts Options) Run {
	run := d.Run

	// One row per test: the results are grouped into plays, each with one
	// verdict, however many checks the test printed.
	results := make([]core.Result, len(d.Results))
	numbers := make(map[string]int)
	for i, r := range d.Results {
		results[i] = r.Result
		if r.TestID != "" {
			numbers[r.TestID] = r.Number
		}
	}
	plays := core.Run{Results: results}.Plays()

	var head strings.Builder
	fmt.Fprintf(&head, "Run %d  %s\n", id, ResultWord(run.State, d.Verdict.Outcome))
	if d.Verdict.Reason != "" {
		fmt.Fprintf(&head, "  reason       %s\n", d.Verdict.Reason)
	}
	if checks, failed := len(results), failedChecks(results); checks != len(plays) {
		fmt.Fprintf(&head, "  checks       %d in %s, %d failed\n", checks, plural(len(plays), "test", "tests"), failed)
	}
	fmt.Fprintf(&head, "  when         %s to %s\n", When(loc, run.StartedAt), When(loc, run.FinishedAt))
	fmt.Fprintf(&head, "  source       %s, environment %s\n", run.Source, Dash(run.Environment))
	if run.ExitCode != 0 {
		fmt.Fprintf(&head, "  exit code    %d\n", run.ExitCode)
	}
	if run.Source == core.SourceRemote && run.ExternalID != "" {
		fmt.Fprintf(&head, "  remote run   %s, attempt %d, commit %s\n", run.ExternalID, run.Attempt, Dash(run.CommitSHA))
	}
	// A run the tester started or recorded says what it was made against. A
	// pulled run already shows its commit above, and has no tools of its own.
	if run.Source != core.SourceRemote {
		// An imported run's key ends with the hash of the file it came from.
		if i := strings.LastIndex(run.ExternalID, ":"); run.Source == core.SourceImport && i >= 0 && len(run.ExternalID) >= i+13 {
			fmt.Fprintf(&head, "  file         sha256 %s\n", run.ExternalID[i+1:i+13])
		}
		fmt.Fprintf(&head, "  commit       %s\n", commitText(run))
		if run.Trigger != "" {
			fmt.Fprintf(&head, "  started by   %s\n", run.Trigger)
		}
		head.WriteString(toolsText(run.Tools))
	}

	var rows strings.Builder
	rows.WriteString("Results:\n")
	if len(plays) == 0 {
		rows.WriteString("  (none)\n")
	}
	width := 0
	for _, p := range plays {
		width = max(width, len(Dash(p.TestID)))
	}
	hidden := 0
	for _, p := range plays {
		num := "-"
		if n := numbers[p.TestID]; p.TestID != "" && n > 0 {
			num = fmt.Sprintf("#%d", n)
		}
		if len(p.Checks) == 1 {
			// The usual case: one check is the whole test, shown as it always was.
			c := p.Checks[0]
			fmt.Fprintf(&rows, "  %-4s %-5s %-*s  %s\n", num, strings.ToUpper(string(p.Status)), width, Dash(p.TestID), c.Name)
			if strings.TrimSpace(c.Message) != "" {
				fmt.Fprintln(&rows, Indent(strings.TrimSpace(c.Message), "        "))
			}
			continue
		}
		fmt.Fprintf(&rows, "  %-4s %-5s %-*s  %s\n", num, strings.ToUpper(string(p.Status)), width, Dash(p.TestID), checksText(p))
		for _, c := range p.Checks {
			if !opts.Checks && !c.Status.Failed() {
				hidden++
				continue
			}
			fmt.Fprintf(&rows, "        %-5s %s\n", strings.ToUpper(string(c.Status)), c.Name)
			if strings.TrimSpace(c.Message) != "" {
				fmt.Fprintln(&rows, Indent(strings.TrimSpace(c.Message), "              "))
			}
		}
	}

	out := Run{
		Header:       strings.TrimRight(head.String(), "\n"),
		Results:      strings.TrimRight(rows.String(), "\n"),
		HiddenChecks: hidden,
	}
	if strings.TrimSpace(run.Diagnostic) != "" {
		out.Diagnostic = "Diagnostic:\n" + Indent(run.Diagnostic, "  ")
	}
	if strings.TrimSpace(run.Detail) != "" {
		out.Detail = "Tool detail:\n" + Indent(run.Detail, "  ")
	}
	return out
}

// checksText summarises a test that printed several checks: how many, and how
// many failed or were skipped.
func checksText(p core.Play) string {
	text := fmt.Sprintf("%d checks", len(p.Checks))
	if n := p.FailedChecks(); n > 0 {
		text += fmt.Sprintf(", %d failed", n)
	}
	skipped := 0
	for _, c := range p.Checks {
		if c.Status == core.StatusSkip {
			skipped++
		}
	}
	if skipped > 0 {
		text += fmt.Sprintf(", %d skipped", skipped)
	}
	return text
}

func failedChecks(results []core.Result) int {
	n := 0
	for _, r := range results {
		if r.Status.Failed() {
			n++
		}
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ResultWord is how a run's state and outcome read in a list.
func ResultWord(state core.RunState, outcome core.Outcome) string {
	switch {
	case state == core.RunPending:
		return "PENDING"
	case state == core.RunExpired:
		return "EXPIRED"
	case outcome == core.OutcomePass:
		return "PASS"
	case outcome == core.OutcomeFail:
		return "FAIL"
	}
	return "?"
}

// When formats a time in the zone loc (nil means the local zone), or "-" when
// it is not set.
func When(loc *time.Location, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	if loc == nil {
		loc = time.Local
	}
	return t.In(loc).Format("2006-01-02 15:04:05")
}

// Dash shows "-" for an empty cell.
func Dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Indent puts prefix at the start of every line of s.
func Indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
