package execute

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/redact"
)

// envResult is what environment builds for a run.
type envResult struct {
	// env is the environment every command of the run gets.
	env []string
	// extra holds, per test id, the KEY=value lines only that test receives
	// (its own `secrets`), added to env when the test starts.
	extra map[string][]string
	// missing holds, per test id, the names of its secrets that are not set.
	// A test that has any is refused (see unmet); it never starts.
	missing map[string][]string
	// scrubber removes every secret value the run knows, the environment's and
	// each selected test's, from everything that is stored or shown.
	scrubber *redact.Redactor
}

// environment builds the environment the commands of the run get, and the
// scrubber for the secrets in it.
//
// A command gets exactly this and nothing else (the runner never inherits):
// the tester's own variables, MINUS every secret the manifest declares
// anywhere (all environments, every test, and the pull token) and MINUS any
// variable the chosen environment sets itself, PLUS the environment's
// variables and its secrets, PLUS (added per test by testEnv) the test's own
// secrets. So a secret that belongs to another environment, to another test,
// or to pulling results, never reaches a test.
//
// The environment's secrets are all required: one that is not set stops the
// run before it starts (problems). A test's own secret that is not set does not
// stop the run here; it is recorded in missing, and the run is refused only if
// that test is among the tests selected.
func (s *Service) environment(ctx context.Context, m *core.Manifest, spec core.EnvironmentSpec, tests []core.TestSpec) (res envResult, problems []string) {
	declared := make(map[string]bool)
	for _, e := range m.Environments {
		for _, name := range e.Secrets {
			declared[name] = true
		}
	}
	for _, t := range m.Tests {
		for _, name := range t.Secrets {
			declared[name] = true
		}
	}
	if m.Pull != nil {
		declared[m.Pull.Token] = true
	}

	own := make(map[string]bool)
	for k := range spec.Vars {
		own[k] = true
	}
	for _, name := range spec.Secrets {
		own[name] = true
	}

	values := make(map[string]string)
	var missing []string
	names := append([]string(nil), spec.Secrets...)
	sort.Strings(names)
	for _, name := range names {
		v, err := s.Creds.Get(ctx, name)
		switch {
		case errors.Is(err, core.ErrNotFound):
			missing = append(missing, name)
		case err != nil:
			problems = append(problems, fmt.Sprintf("secret %q could not be read: %v", name, err))
		default:
			values[name] = v
		}
	}
	if len(missing) > 0 {
		problems = append(problems, "secrets not set: "+strings.Join(missing, ", "))
	}

	// The selected tests' own secrets: read once each, and kept per test.
	testValues := make(map[string]string)
	notSet := make(map[string]bool)
	res.extra = make(map[string][]string)
	res.missing = make(map[string][]string)
	for _, t := range tests {
		for _, name := range t.Secrets {
			if _, done := testValues[name]; !done && !notSet[name] {
				v, err := s.Creds.Get(ctx, name)
				switch {
				case errors.Is(err, core.ErrNotFound):
					notSet[name] = true
				case err != nil:
					problems = append(problems, fmt.Sprintf("secret %q of test %s could not be read: %v", name, t.ID, err))
					notSet[name] = true
				default:
					testValues[name] = v
				}
			}
			if notSet[name] {
				res.missing[t.ID] = append(res.missing[t.ID], name)
				continue
			}
			res.extra[t.ID] = append(res.extra[t.ID], name+"="+testValues[name])
		}
	}
	if len(problems) > 0 {
		return envResult{}, problems
	}

	all := make(map[string]string, len(values)+len(testValues))
	for k, v := range values {
		all[k] = v
	}
	for k, v := range testValues {
		all[k] = v
	}
	scrubber, err := redact.New(all)
	if err != nil {
		return envResult{}, []string{err.Error()}
	}
	res.scrubber = scrubber

	if s.Environ != nil {
		for _, kv := range s.Environ() {
			name, _, ok := strings.Cut(kv, "=")
			if !ok || declared[name] || own[name] {
				continue
			}
			res.env = append(res.env, kv)
		}
	}
	for _, k := range sortedKeys(spec.Vars) {
		res.env = append(res.env, k+"="+spec.Vars[k])
	}
	for _, name := range names {
		res.env = append(res.env, name+"="+values[name])
	}
	return res, nil
}

// testEnv is the environment of one test's command: the run's, then the
// test's own secrets. The result is a new slice; the run's is never changed.
func (p *plan) testEnv(t core.TestSpec) []string {
	extra := p.extra[t.ID]
	if len(extra) == 0 {
		return p.env
	}
	out := make([]string, 0, len(p.env)+len(extra))
	out = append(out, p.env...)
	return append(out, extra...)
}

// secretRefusals turns each selected test's unset secrets into a refusal of
// the run, one per test, so the person sees what to set before anything starts.
func (p *plan) secretRefusals() []Refusal {
	var out []Refusal
	for _, t := range p.tests {
		names := p.missing[t.ID]
		if len(names) == 0 {
			continue
		}
		out = append(out, Refusal{
			Name:    "secrets of " + t.ID,
			Message: "Set " + strings.Join(names, ", ") + " in the shell you run the tester from (the project's own way of supplying secrets), then run again. Other tests are not affected.",
			Why:     "not set: " + strings.Join(names, ", "),
			Secret:  true,
		})
	}
	return out
}
