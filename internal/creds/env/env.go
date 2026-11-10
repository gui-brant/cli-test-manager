// Package env is a credentials source that reads environment variables. The
// secret's name, as declared in the manifest, is the variable's name.
package env

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// validName is what a variable name may look like. Anything else (an empty
// name, a space, "=", a hyphen) is a mistake in the manifest, reported as such
// instead of being looked up.
var validName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Credentials reads secrets from environment variables.
type Credentials struct {
	lookup func(name string) (string, bool)
}

var _ core.Credentials = (*Credentials)(nil)

// New reads the real process environment.
func New() *Credentials { return &Credentials{lookup: os.LookupEnv} }

// NewWithLookup reads through the given function, so tests need not touch the
// process environment.
func NewWithLookup(lookup func(name string) (string, bool)) *Credentials {
	return &Credentials{lookup: lookup}
}

// Get returns the value of the variable called name.
//
// A variable that is unset, empty or only whitespace is core.ErrNotFound: an
// empty secret is a misconfiguration, never a usable value. A name that is not
// a valid variable name is a different error, not ErrNotFound. Errors name the
// secret and never contain its value.
func (c *Credentials) Get(_ context.Context, name string) (string, error) {
	if !validName.MatchString(name) {
		return "", fmt.Errorf("env: %q is not a valid secret name (letters, digits and underscores, not starting with a digit)", name)
	}
	value, ok := c.lookup(name)
	if !ok {
		return "", fmt.Errorf("env: secret %q is not set: %w", name, core.ErrNotFound)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("env: secret %q is empty: %w", name, core.ErrNotFound)
	}
	return value, nil
}
