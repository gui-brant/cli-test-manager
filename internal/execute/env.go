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

// environment builds the environment every command of the run gets, and the
// scrubber for the secrets in it.
//
// A command gets exactly this and nothing else (the runner never inherits):
// the tester's own variables, MINUS every secret the manifest declares
// anywhere (all environments, and the pull token) and MINUS any variable the
// chosen environment sets itself, PLUS the environment's variables and its
// secrets. So a secret that belongs to another environment, or to pulling
// results, never reaches a test.
func (s *Service) environment(ctx context.Context, m *core.Manifest, spec core.EnvironmentSpec) (env []string, scrubber *redact.Redactor, problems []string) {
	declared := make(map[string]bool)
	for _, e := range m.Environments {
		for _, name := range e.Secrets {
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
	if len(problems) > 0 {
		return nil, nil, problems
	}

	scrubber, err := redact.New(values)
	if err != nil {
		return nil, nil, []string{err.Error()}
	}

	if s.Environ != nil {
		for _, kv := range s.Environ() {
			name, _, ok := strings.Cut(kv, "=")
			if !ok || declared[name] || own[name] {
				continue
			}
			env = append(env, kv)
		}
	}
	for _, k := range sortedKeys(spec.Vars) {
		env = append(env, k+"="+spec.Vars[k])
	}
	for _, name := range names {
		env = append(env, name+"="+values[name])
	}
	return env, scrubber, nil
}
