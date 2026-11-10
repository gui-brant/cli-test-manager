package core

import (
	"slices"
	"time"
)

// This file defines the shapes of the project manifest, test-manifest.yaml.
// It only declares the types. Loading the file (strict YAML: unknown keys and
// wrong types rejected) and the load-time checks are a later step; the yaml
// tags below are plain strings and need no import here.
//
// The manifest is how a project describes itself to the tester. It holds names
// of secrets, never values.

const (
	// GeneralFeature is the catch-all feature for checks that belong to no
	// single feature. It always exists and need not be listed in Features.
	GeneralFeature = "general"

	// PlaceholderFiles in a runner's command is replaced with the paths of the
	// tests to run.
	PlaceholderFiles = "{files}"
	// PlaceholderReport in a runner's command is replaced with the path of a
	// temporary result file, for tools that write their results to a file.
	PlaceholderReport = "{report}"
)

// ResultFormat is the standard format a runner prints its results in. These
// are the only two formats the tester reads.
type ResultFormat string

const (
	FormatTAP   ResultFormat = "tap"
	FormatJUnit ResultFormat = "junit"
)

// OutputMode says where a runner's results are written.
type OutputMode string

const (
	// OutputStdout: results are on standard output.
	OutputStdout OutputMode = "stdout"
	// OutputFile: results are written to the file named by PlaceholderReport.
	OutputFile OutputMode = "file"
)

// Manifest is the whole manifest file.
type Manifest struct {
	// Version is the manifest schema version. It starts at 1.
	Version int `yaml:"manifest_version"`
	// Project is the project's name.
	Project string `yaml:"project"`
	// History is the path of the project-owned history file, relative to the
	// manifest's folder.
	History string `yaml:"history"`
	// Features lists the project's features. GeneralFeature always exists.
	Features []string `yaml:"features"`
	// Environments maps an environment name (local, deployed, anything the
	// project defines) to its settings.
	Environments map[string]EnvironmentSpec `yaml:"environments"`
	// Runners maps a runner name to how it starts tests.
	Runners map[string]RunnerSpec `yaml:"runners"`
	// Tests is the test list. Its order is the test numbering, so it is
	// append-only: reordering or removing entries renumbers history.
	Tests []TestSpec `yaml:"tests"`
	// Pull configures bringing in results produced elsewhere. Nil when the
	// project does not pull.
	Pull *PullSpec `yaml:"pull,omitempty"`
}

// EnvironmentSpec is one place tests can act (the "type" in the menu).
type EnvironmentSpec struct {
	// Vars are non-secret variables handed to the command. They may be
	// committed.
	Vars map[string]string `yaml:"vars"`
	// Secrets are the NAMES of secret variables. Values are read through the
	// credentials interface at run time and are never in the manifest.
	Secrets []string `yaml:"secrets"`
}

// RunnerSpec says how to start tests of one kind.
type RunnerSpec struct {
	// Run is the command as an argument list, with no shell. It contains
	// PlaceholderFiles, and PlaceholderReport exactly when Output is
	// OutputFile.
	Run []string `yaml:"run"`
	// Format is the result format the command prints.
	Format ResultFormat `yaml:"format"`
	// Output says where the results are written.
	Output OutputMode `yaml:"output"`
	// Environments lists the environments this runner may run in.
	Environments []string `yaml:"environments"`
	// Batch is true when the command accepts several test paths in one call.
	// When false the tester starts it once per test.
	Batch bool `yaml:"batch"`
	// Timeout stops a command that runs too long.
	Timeout time.Duration `yaml:"timeout"`
}

// TestSpec is one entry of the test list.
type TestSpec struct {
	// ID is a stable name that is never changed or reused, so history
	// survives a rename. Lowercase letters, digits and hyphens.
	ID string `yaml:"id"`
	// Name is the readable label shown in lists.
	Name string `yaml:"name"`
	// Feature is the ONE feature the test belongs to, or GeneralFeature.
	Feature string `yaml:"feature"`
	// Runner is the name of the runner that starts it.
	Runner string `yaml:"runner"`
	// File is the test's path inside the project, used for the code view and
	// for matching results to the test.
	File string `yaml:"file"`
	// Tag says how the test was made (free text for now).
	Tag string `yaml:"tag,omitempty"`
}

// PullSpec says where results produced elsewhere can be fetched.
type PullSpec struct {
	// Provider names the result source. Only "github" is planned.
	Provider string `yaml:"provider"`
	// Repo is the repository, "owner/name".
	Repo string `yaml:"repo"`
	// Workflow is the workflow whose runs hold the results.
	Workflow string `yaml:"workflow"`
	// Artifact is the name of the result file attached to a run.
	Artifact string `yaml:"artifact"`
	// LookbackDays bounds how far back the first pull goes, so an empty
	// history does not download months of artifacts.
	LookbackDays int `yaml:"lookback_days"`
	// Token is the NAME of the secret holding the service token.
	Token string `yaml:"token"`
}

// Test returns the test with the given id.
func (m *Manifest) Test(id string) (TestSpec, bool) {
	for _, t := range m.Tests {
		if t.ID == id {
			return t, true
		}
	}
	return TestSpec{}, false
}

// HasFeature reports whether name is a feature of the project. GeneralFeature
// always is.
func (m *Manifest) HasFeature(name string) bool {
	return name == GeneralFeature || slices.Contains(m.Features, name)
}
