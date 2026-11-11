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
}

// HasDetail says whether the run has tool detail to offer.
func (r Run) HasDetail() bool { return r.Detail != "" }

// Build makes the view of one run. id is the run's number, and loc the zone
// the times are shown in (nil means the local zone).
func Build(d query.RunDetail, id int64, loc *time.Location) Run {
	run := d.Run

	var head strings.Builder
	fmt.Fprintf(&head, "Run %d  %s\n", id, ResultWord(run.State, d.Verdict.Outcome))
	if d.Verdict.Reason != "" {
		fmt.Fprintf(&head, "  reason       %s\n", d.Verdict.Reason)
	}
	fmt.Fprintf(&head, "  when         %s to %s\n", When(loc, run.StartedAt), When(loc, run.FinishedAt))
	fmt.Fprintf(&head, "  source       %s, environment %s\n", run.Source, Dash(run.Environment))
	if run.ExitCode != 0 {
		fmt.Fprintf(&head, "  exit code    %d\n", run.ExitCode)
	}
	if run.ExternalID != "" {
		fmt.Fprintf(&head, "  remote run   %s, attempt %d, commit %s\n", run.ExternalID, run.Attempt, Dash(run.CommitSHA))
	}

	var results strings.Builder
	results.WriteString("Results:\n")
	if len(d.Results) == 0 {
		results.WriteString("  (none)\n")
	}
	width := 0
	for _, r := range d.Results {
		width = max(width, len(Dash(r.TestID)))
	}
	for _, r := range d.Results {
		num := "-"
		if r.Number > 0 {
			num = fmt.Sprintf("#%d", r.Number)
		}
		fmt.Fprintf(&results, "  %-4s %-5s %-*s  %s\n", num, strings.ToUpper(string(r.Status)), width, Dash(r.TestID), r.Name)
		if strings.TrimSpace(r.Message) != "" {
			fmt.Fprintln(&results, Indent(strings.TrimSpace(r.Message), "        "))
		}
	}

	out := Run{
		Header:  strings.TrimRight(head.String(), "\n"),
		Results: strings.TrimRight(results.String(), "\n"),
	}
	if strings.TrimSpace(run.Diagnostic) != "" {
		out.Diagnostic = "Diagnostic:\n" + Indent(run.Diagnostic, "  ")
	}
	if strings.TrimSpace(run.Detail) != "" {
		out.Detail = "Tool detail:\n" + Indent(run.Detail, "  ")
	}
	return out
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
