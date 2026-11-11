package execute

import (
	"context"
	"fmt"
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
func (s *Service) runOne(ctx context.Context, req Request, p *plan, t core.TestSpec) ([]core.Result, *failure, error) {
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
			return nil, nil, ctx.Err()
		}
		cause := "could not start the command: " + p.redactor.Redact(err.Error())
		return []core.Result{errorResult(t, cause)}, &failure{exitCode: -1, cause: cause, output: cause}, nil
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
	reported := 0 // failures the test tool itself reported
	for _, it := range items {
		r := it.Result
		r.TestID, r.Feature = t.ID, t.Feature // the feature is recorded as it is now
		results = append(results, r)
		if r.Status.Failed() && !it.Synthetic {
			reported++
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
		return results, nil, nil
	}

	tail := outputTail(stdout, stderr)
	if line := lastLine(tail); line != "" {
		cause += ": " + line
	}
	code := out.ExitCode
	if code == 0 {
		code = -1
	}
	results = append(results, errorResult(t, cause))
	return results, &failure{exitCode: code, cause: cause, output: tail}, nil
}

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
