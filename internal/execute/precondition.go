package execute

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// preconditionTimeout is how long one precondition's check may run. It is not a
// manifest setting: a check only looks at something, so a check that is slow is
// a check that is wrong.
const preconditionTimeout = 30 * time.Second

// Refusal is a precondition that is not met: something the person must do
// before these tests can run.
type Refusal struct {
	// Name is the precondition's name in the manifest.
	Name string
	// Message is what the person must do, as the manifest words it.
	Message string
	// Why is what the check itself reported: the first line it printed, or that
	// it timed out, or its exit code. It is scrubbed of the run's secrets.
	Why string
	// Secret is true when this is not a precondition but a test's own secret
	// that is not set; Name then reads "secrets of <test id>".
	Secret bool
}

// preconditions lists the preconditions that apply to the tests about to run,
// once each, in a fixed order: the environment's, then each used runner's (in
// runner-name order), then each test's own (in test order). A name that appears
// in several places stands for one definition, which the manifest check
// guarantees, so only its first appearance is kept.
func preconditions(req Request, tests []core.TestSpec) []core.Precondition {
	var out []core.Precondition
	seen := make(map[string]bool)
	add := func(ps []core.Precondition) {
		for _, p := range ps {
			if !seen[p.Name] {
				seen[p.Name] = true
				out = append(out, p)
			}
		}
	}

	add(req.Manifest.Environments[req.Environment].Preconditions)

	runners := make(map[string]bool)
	for _, t := range tests {
		runners[t.Runner] = true
	}
	names := make([]string, 0, len(runners))
	for name := range runners {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		add(req.Manifest.Runners[name].Preconditions)
	}

	for _, t := range tests {
		add(t.Preconditions)
	}
	return out
}

// unmet runs the checks of the tests about to run and returns the ones that
// failed, all of them, so the person can put everything right in one go. A
// check that cannot even be started is not "not met": it is a mistake in the
// manifest, and it is returned as an error. A cancelled context is an error too.
//
// Checks only look, so running them more than once (a preview, then the run)
// is safe.
func (s *Service) unmet(ctx context.Context, req Request, p *plan) ([]Refusal, error) {
	// A selected test whose own secret is not set cannot run, so the run is
	// refused with the rest. Nothing is started to find that out.
	refusals := p.secretRefusals()
	for _, pre := range preconditions(req, p.tests) {
		out, err := s.Runner.Run(ctx, core.Invocation{Args: pre.Check, Env: p.env, Dir: req.Root, Timeout: preconditionTimeout})
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("execute: cancelled while checking precondition %q; nothing was saved: %w", pre.Name, ctx.Err())
			}
			return nil, fmt.Errorf("execute: cannot start the run: the check of precondition %q could not be run: %s",
				pre.Name, p.redactor.Redact(err.Error()))
		}
		switch {
		case out.TimedOut:
			refusals = append(refusals, Refusal{Name: pre.Name, Message: pre.Message, Why: fmt.Sprintf("its check timed out after %s", preconditionTimeout)})
		case out.ExitCode != 0:
			refusals = append(refusals, Refusal{Name: pre.Name, Message: pre.Message, Why: p.checkSaid(out)})
		}
	}
	return refusals, nil
}

// checkSaid is what a failed check reported: the first line it printed
// (standard output first, then standard error), or its exit code when it
// printed nothing.
func (p *plan) checkSaid(out core.Output) string {
	for _, stream := range [][]byte{out.Stdout, out.Stderr} {
		if line := firstLine(p.redactor.Redact(string(stream))); line != "" {
			return line
		}
	}
	return fmt.Sprintf("its check exited with code %d and printed nothing", out.ExitCode)
}

// RefusalText is how refusals are written in a run's diagnostic and shown to the
// person: what to do first, then what the check said.
func RefusalText(refusals []Refusal) string {
	blocks := make([]string, 0, len(refusals))
	for _, r := range refusals {
		if r.Secret {
			blocks = append(blocks, fmt.Sprintf("%s: %s\n  %s", r.Name, r.Message, r.Why))
			continue
		}
		blocks = append(blocks, fmt.Sprintf("precondition %s is not met: %s\n  the check said: %s", r.Name, r.Message, r.Why))
	}
	return strings.Join(blocks, "\n\n")
}

// refuse saves the run that was not made. It has no results, only the reason in
// its diagnostic, so the history shows that the run was asked for and why it
// did not start.
func (s *Service) refuse(ctx context.Context, req Request, p *plan, refusals []Refusal) (Report, error) {
	now := s.now()
	run := core.Run{
		Source: core.SourceLocal, Environment: req.Environment, State: core.RunRefused,
		StartedAt: now, FinishedAt: now,
		Trigger:    req.Trigger,
		Diagnostic: core.TailDiagnostic(RefusalText(refusals)),
	}
	if run.Trigger == "" {
		run.Trigger = core.TriggerManual
	}
	if s.Checkout != nil {
		run.CommitSHA, run.Dirty = s.Checkout.State(ctx, req.Root)
	}
	rep := Report{Run: run, Skipped: p.skipped, Changed: p.changed, Refused: refusals}
	id, err := s.Store.SaveRun(ctx, run)
	if err != nil {
		return rep, fmt.Errorf("execute: saving the refused run: %w", err)
	}
	rep.RunID, rep.Run.ID, rep.Saved = id, id, true
	return rep, nil
}
