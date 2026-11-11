package execute

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/parse"
	"github.com/gui-brant/cli-test-manager/internal/parse/junit"
	"github.com/gui-brant/cli-test-manager/internal/parse/tap"
)

// failure is a test's failure that its own results do not explain.
type failure struct {
	// exitCode is never 0: it is the command's exit code, or -1 when the
	// command had none to report (it could not start, or exited 0 with no
	// results).
	exitCode int
	// cause is the message of the test's error result.
	cause string
	// output is the scrubbed end of the command's output.
	output string
}

// runOne runs one test's command and turns its output into results. A failure
// that the results do not explain (a crash after the last passing test, a
// timeout, a command that cannot start, unreadable results, no results) is
// pinned to the test as an extra error result and returned as a failure. The
// error return is only for a cancelled run.
//
// The second extra return is the test's failure details: for each failed result
// the tool gave more than a short message for, a block headed with the test and
// the result's name. They are for the run's diagnostic, not for a table.
func (s *Service) runOne(ctx context.Context, req Request, p *plan, t core.TestSpec) ([]core.Result, *failure, []string, error) {
	rs := req.Manifest.Runners[t.Runner]
	inv := core.Invocation{
		Args:        expand(rs.Run, t.File),
		Env:         p.env,
		Dir:         req.Root,
		Timeout:     rs.Timeout,
		WantsReport: rs.Output == core.OutputFile,
	}

	out, err := s.Runner.Run(ctx, inv)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, nil, ctx.Err()
		}
		cause := "could not start the command: " + p.redactor.Redact(err.Error())
		// The cause is the whole story, so no output is kept beside it.
		return []core.Result{errorResult(t, cause)}, &failure{exitCode: -1, cause: cause}, nil, nil
	}

	// Scrub first, then parse, so names and messages are already clean.
	stdout := p.redactor.Redact(string(out.Stdout))
	stderr := p.redactor.Redact(string(out.Stderr))
	text := stdout
	if rs.Output == core.OutputFile {
		text = p.redactor.Redact(string(out.Report))
	}

	var items []parse.Item
	var parseErr error
	switch rs.Format {
	case core.FormatTAP:
		items, parseErr = tap.Parse(strings.NewReader(text))
	case core.FormatJUnit:
		items, parseErr = junit.Parse(strings.NewReader(text))
	}

	results := make([]core.Result, 0, len(items)+1)
	own := make([]core.Result, 0, len(items)+1) // without the parser's findings
	reported := 0                               // failures the test tool itself reported
	var details []string
	for _, it := range items {
		r := it.Result
		r.TestID, r.Feature = t.ID, t.Feature // the feature is recorded as it is now
		results = append(results, r)
		if !it.Synthetic {
			own = append(own, r)
		}
		if r.Status.Failed() && !it.Synthetic {
			reported++
			if it.Detail != "" {
				details = append(details, fmt.Sprintf("[%s] %s\n%s", t.ID, r.Name, it.Detail))
			}
		}
	}

	// A non-zero exit that comes with a failure the tool reported is explained
	// by it. The parser's own findings about a broken stream do not count: a
	// command that crashes right after printing its plan line leaves a "planned
	// 1 tests but saw 0" result that says the stream is wrong, not why, and the
	// real cause is on standard error.
	var cause string
	switch {
	case out.TimedOut:
		cause = "timed out after " + rs.Timeout.String()
	case parseErr != nil:
		cause = "could not read the results: " + p.redactor.Redact(parseErr.Error())
	case out.ExitCode != 0 && reported == 0:
		cause = fmt.Sprintf("exited with code %d", out.ExitCode)
	case len(results) == 0:
		cause = "no results"
	}
	if cause == "" {
		return results, nil, details, nil
	}

	tail := outputTail(stdout, stderr)
	last := lastLine(tail)
	if last != "" {
		cause += ": " + last
	}
	code := out.ExitCode
	if code == 0 {
		code = -1
	}
	// One error per failed test: the real cause replaces the parser's finding
	// that the stream is wrong ("planned 1 tests but saw 0"), which only says
	// that something broke, not why.
	results = append(own, errorResult(t, cause))
	return results, &failure{exitCode: code, cause: cause, output: diagnosticBody(tail, last, rs.Format)}, details, nil
}

// diagnosticBody is the part of a failed command's output worth showing under
// the cause. The cause already ends with the output's last line, and a TAP
// stream's plan line ("1..3") says nothing about why the command failed, so
// neither is repeated.
func diagnosticBody(tail, last string, format core.ResultFormat) string {
	lines := strings.Split(tail, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if format == core.FormatTAP && tapPlanLine.MatchString(l) {
			continue
		}
		kept = append(kept, l)
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(kept) > 0 && last != "" && strings.TrimSpace(kept[len(kept)-1]) == last {
		kept = kept[:len(kept)-1]
	}
	return strings.Join(kept, "\n")
}

// tapPlanLine matches a TAP plan line such as "1..3".
var tapPlanLine = regexp.MustCompile(`^\s*1\.\.\d+\s*$`)

// expand fills a runner's argument list for one test. Only the files
// placeholder is replaced; the report placeholder is left for the runner,
// which owns the temporary result file.
func expand(args []string, file string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == core.PlaceholderFiles {
			a = file
		}
		out[i] = a
	}
	return out
}

func errorResult(t core.TestSpec, message string) core.Result {
	return core.Result{
		TestID:  t.ID,
		Feature: t.Feature,
		Name:    t.Name,
		Status:  core.StatusError,
		Message: parse.CapMessage(message),
	}
}

// outputTail is the end of the command's output for the diagnostic: standard
// output, then standard error last, since the error is usually there.
func outputTail(stdout, stderr string) string {
	joined := strings.Trim(strings.TrimRight(stdout, "\r\n")+"\n"+strings.TrimRight(stderr, "\r\n"), "\r\n")
	return core.TailDiagnostic(joined)
}

// lastLine is the last non-empty line of s, trimmed.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
