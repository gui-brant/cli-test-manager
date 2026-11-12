package pull

import (
	"context"
	"errors"
	"fmt"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/redact"
)

// TokenError is the pull token's name and why it could not be had. Its message
// says what to do; the value is never part of it.
type TokenError struct {
	Name string
	Err  error
}

func (e *TokenError) Error() string {
	if errors.Is(e.Err, core.ErrNotFound) {
		return fmt.Sprintf("the pull token %s is not set: export it in your shell (its value is never stored)", e.Name)
	}
	return fmt.Sprintf("the pull token %s could not be read: %v", e.Name, e.Err)
}

func (e *TokenError) Unwrap() error { return e.Err }

// Prepare reads the service's token, named in the manifest's pull section, and
// builds the scrubber for what is pulled: the token and the secrets of the
// environment the pulled results belong to, when they are set here too, since a
// workflow may print them. A token that cannot be had is a *TokenError.
func Prepare(ctx context.Context, creds core.Credentials, m *core.Manifest) (token string, scrub *redact.Redactor, err error) {
	if m == nil || m.Pull == nil {
		return "", nil, errors.New("pull: the manifest has no pull section")
	}
	spec := m.Pull
	token, err = creds.Get(ctx, spec.Token)
	if err != nil {
		return "", nil, &TokenError{Name: spec.Token, Err: err}
	}
	values := map[string]string{spec.Token: token}
	for _, name := range m.Environments[spec.Environment].Secrets {
		if v, err := creds.Get(ctx, name); err == nil {
			values[name] = v
		}
	}
	scrub, err = redact.New(values)
	if err != nil {
		return "", nil, err
	}
	return token, scrub, nil
}
