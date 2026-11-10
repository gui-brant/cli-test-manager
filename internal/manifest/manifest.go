// Package manifest reads and checks a project's test-manifest.yaml.
//
// It does no disk access: Parse reads from an io.Reader and Validate works on
// the parsed value, so both are pure and easy to test. Finding and reading the
// file, and checking that the test files exist, belong to the manifestfile
// package.
package manifest

import (
	"errors"
	"fmt"
	"io"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

const (
	// DefaultTimeout is given to a runner that sets none.
	DefaultTimeout = 10 * time.Minute

	// MinTimeout is the shortest explicit timeout accepted. A timeout under a
	// second is always a mistake, such as 10ms written where 10m was meant. A
	// bare number (`timeout: 10`) never gets this far: the YAML reader rejects
	// it and asks for a unit.
	MinTimeout = time.Second
)

// Parse reads a manifest from r, strictly: an unknown key, a wrong type, a
// duplicate key, an empty file or a second YAML document is an error. Where
// YAML can say where, the error carries a line number. Defaults are applied
// (a runner with no timeout gets DefaultTimeout).
//
// Parse checks the file's shape only. Whether the values make sense together
// (a test's runner exists, a path is safe, and so on) is Validate's job.
func Parse(r io.Reader) (*core.Manifest, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var m core.Manifest
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("manifest: the file is empty")
		}
		return nil, fmt.Errorf("manifest: %w", err)
	}

	// A second document in the same file is a mistake: the tester would read
	// only the first and silently ignore the rest.
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, errors.New("manifest: the file has more than one YAML document; keep exactly one")
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("manifest: %w", err)
	}

	applyDefaults(&m)
	return &m, nil
}

func applyDefaults(m *core.Manifest) {
	for name, r := range m.Runners {
		if r.Timeout == 0 {
			r.Timeout = DefaultTimeout
			m.Runners[name] = r
		}
	}
}
