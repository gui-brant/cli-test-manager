package manifest

import (
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// valid returns a fresh, sound manifest. Each test mutates its own copy.
func valid() *core.Manifest {
	return &core.Manifest{
		Version:  1,
		Project:  "example",
		History:  ".tester/history.sqlite",
		Features: []string{"accounts", "notes"},
		Environments: map[string]core.EnvironmentSpec{
			"local":    {Vars: map[string]string{"BASE_URL": "http://127.0.0.1:8000"}},
			"deployed": {Vars: map[string]string{"BASE_URL": "https://dev.example.test"}, Secrets: []string{"ACCOUNT_PASSWORD"}},
		},
		Runners: map[string]core.RunnerSpec{
			"sql": {
				Run: []string{"run-sql-tests", "{files}"}, Format: core.FormatTAP, Output: core.OutputStdout,
				Environments: []string{"local"}, Batch: true, Timeout: 10 * time.Minute,
			},
			"http": {
				Run: []string{"http-runner", "--report", "{report}", "{files}"}, Format: core.FormatJUnit, Output: core.OutputFile,
				Environments: []string{"local", "deployed"}, Batch: true, Timeout: 10 * time.Minute,
			},
		},
		Tests: []core.TestSpec{
			{ID: "users-table", Name: "Users table checks", Feature: "accounts", Runner: "sql", File: "tests/sql/users_table.sql"},
			{ID: "login-flow", Name: "Login flow", Feature: "accounts", Runner: "http", File: "tests/http/login_flow.http", Tag: "http"},
		},
		Pull: &core.PullSpec{
			Provider: "github", Repo: "owner/name", Workflow: "tests.yml", Artifact: "test-results",
			Environment: "deployed", LookbackDays: 30, Token: "CI_TOKEN",
		},
	}
}

func withRunner(m *core.Manifest, name string, f func(*core.RunnerSpec)) {
	r := m.Runners[name]
	f(&r)
	m.Runners[name] = r
}

func withEnv(m *core.Manifest, name string, f func(*core.EnvironmentSpec)) {
	e := m.Environments[name]
	f(&e)
	m.Environments[name] = e
}

func TestValidateAcceptsSoundManifests(t *testing.T) {
	cases := map[string]func(*core.Manifest){
		"the baseline":                     func(m *core.Manifest) {},
		"no pull section":                  func(m *core.Manifest) { m.Pull = nil },
		"no tests yet":                     func(m *core.Manifest) { m.Tests = nil },
		"a test in the general feature":    func(m *core.Manifest) { m.Tests[0].Feature = core.GeneralFeature },
		"general also listed as a feature": func(m *core.Manifest) { m.Features = append(m.Features, core.GeneralFeature) },
		"a runner serving every environment": func(m *core.Manifest) {
			withRunner(m, "sql", func(r *core.RunnerSpec) { r.Environments = []string{"local", "deployed"} })
		},
	}
	for name, mutate := range cases {
		m := valid()
		mutate(m)
		if errs := Validate(m); len(errs) != 0 {
			t.Errorf("%s: unexpected problems:\n%v", name, Report(errs))
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*core.Manifest)
		want   string // a substring one of the problems must contain
	}{
		// header
		{"version 2", func(m *core.Manifest) { m.Version = 2 }, "manifest_version: is 2"},
		{"version missing", func(m *core.Manifest) { m.Version = 0 }, "manifest_version: is missing"},
		{"project empty", func(m *core.Manifest) { m.Project = "" }, "project: is required"},
		{"history absolute", func(m *core.Manifest) { m.History = "/abs/h.sqlite" }, `history: "/abs/h.sqlite" is absolute`},
		{"history climbs out", func(m *core.Manifest) { m.History = "../h.sqlite" }, "climbs out of the project folder"},

		// features
		{"feature with a bad name", func(m *core.Manifest) { m.Features = []string{"Bad Name"} }, "features[0]"},
		{"feature listed twice", func(m *core.Manifest) { m.Features = []string{"a", "a"} }, "is listed twice"},

		// environments
		{"no environments", func(m *core.Manifest) { m.Environments = nil }, "at least one environment is required"},
		{"environment with a bad name", func(m *core.Manifest) { m.Environments["Bad Env"] = core.EnvironmentSpec{} }, "is not a valid name"},
		{"variable with a bad name", func(m *core.Manifest) {
			withEnv(m, "local", func(e *core.EnvironmentSpec) { e.Vars["bad-name"] = "x" })
		}, "is not a valid variable name"},
		{"variable that looks like a secret", func(m *core.Manifest) {
			withEnv(m, "local", func(e *core.EnvironmentSpec) { e.Vars["API_TOKEN"] = "x" })
		}, "looks like a secret; declare it under secrets"},
		{"secret written as name=value", func(m *core.Manifest) {
			withEnv(m, "deployed", func(e *core.EnvironmentSpec) { e.Secrets = []string{"A=B"} })
		}, "never name=value"},
		{"secret listed twice", func(m *core.Manifest) {
			withEnv(m, "deployed", func(e *core.EnvironmentSpec) { e.Secrets = []string{"X_ONE", "X_ONE"} })
		}, "is listed twice"},
		{"name both a variable and a secret", func(m *core.Manifest) {
			withEnv(m, "deployed", func(e *core.EnvironmentSpec) { e.Secrets = []string{"BASE_URL"} })
		}, "both a variable and a secret"},

		// runners
		{"no runners", func(m *core.Manifest) { m.Runners = nil }, "at least one runner is required"},
		{"runner with a bad name", func(m *core.Manifest) { m.Runners["Bad Runner"] = m.Runners["sql"] }, "is not a valid name"},
		{"empty command", func(m *core.Manifest) { withRunner(m, "sql", func(r *core.RunnerSpec) { r.Run = nil }) }, "the command is empty"},
		{"empty program name", func(m *core.Manifest) {
			withRunner(m, "sql", func(r *core.RunnerSpec) { r.Run = []string{"", "{files}"} })
		}, "the program name is empty"},
		{"no files placeholder", func(m *core.Manifest) {
			withRunner(m, "sql", func(r *core.RunnerSpec) { r.Run = []string{"tool"} })
		}, "must contain {files} as an argument of its own"},
		{"files placeholder twice", func(m *core.Manifest) {
			withRunner(m, "sql", func(r *core.RunnerSpec) { r.Run = []string{"tool", "{files}", "{files}"} })
		}, "contains {files} 2 times"},
		{"files placeholder inside an argument", func(m *core.Manifest) {
			withRunner(m, "sql", func(r *core.RunnerSpec) { r.Run = []string{"tool", "--f={files}"} })
		}, "must be an argument of its own"},
		{"unknown format", func(m *core.Manifest) { withRunner(m, "sql", func(r *core.RunnerSpec) { r.Format = "xml" }) }, `format: "xml" is not`},
		{"unknown output", func(m *core.Manifest) { withRunner(m, "sql", func(r *core.RunnerSpec) { r.Output = "pipe" }) }, `output: "pipe" is not`},
		{"file output without a report placeholder", func(m *core.Manifest) {
			withRunner(m, "http", func(r *core.RunnerSpec) { r.Run = []string{"http-runner", "{files}"} })
		}, "so the command must contain {report}"},
		{"stdout output with a report placeholder", func(m *core.Manifest) {
			withRunner(m, "sql", func(r *core.RunnerSpec) { r.Run = []string{"tool", "--r={report}", "{files}"} })
		}, "contains {report} but output is"},
		{"runner with no environments", func(m *core.Manifest) {
			withRunner(m, "sql", func(r *core.RunnerSpec) { r.Environments = nil })
		}, "at least one environment is required"},
		{"runner with an unknown environment", func(m *core.Manifest) {
			withRunner(m, "sql", func(r *core.RunnerSpec) { r.Environments = []string{"nope"} })
		}, `environment "nope" is not defined`},
		{"runner environment listed twice", func(m *core.Manifest) {
			withRunner(m, "sql", func(r *core.RunnerSpec) { r.Environments = []string{"local", "local"} })
		}, "is listed twice"},
		{"timeout of 10 nanoseconds", func(m *core.Manifest) { withRunner(m, "sql", func(r *core.RunnerSpec) { r.Timeout = 10 }) }, "it must be at least 1s"},
		{"timeout of zero", func(m *core.Manifest) { withRunner(m, "sql", func(r *core.RunnerSpec) { r.Timeout = 0 }) }, "it must be at least 1s"},

		// tests
		{"test id missing", func(m *core.Manifest) { m.Tests[0].ID = "" }, "id is required"},
		{"test id invalid", func(m *core.Manifest) { m.Tests[0].ID = "Login Flow" }, "is not valid"},
		{"test id duplicate", func(m *core.Manifest) { m.Tests[1].ID = m.Tests[0].ID }, "is already used by tests[0]"},
		{"test name missing", func(m *core.Manifest) { m.Tests[0].Name = "" }, "name is required"},
		{"test feature missing", func(m *core.Manifest) { m.Tests[0].Feature = "" }, "feature is required"},
		{"test feature unknown", func(m *core.Manifest) { m.Tests[0].Feature = "billing" }, `feature "billing" is not listed`},
		{"test runner missing", func(m *core.Manifest) { m.Tests[0].Runner = "" }, "runner is required"},
		{"test runner unknown", func(m *core.Manifest) { m.Tests[0].Runner = "http2" }, `runner "http2" is not defined`},
		{"test file shared", func(m *core.Manifest) { m.Tests[1].File = m.Tests[0].File }, "is already used by tests[0]"},

		// pull
		{"pull provider", func(m *core.Manifest) { m.Pull.Provider = "gitlab" }, "is not supported"},
		{"pull repo", func(m *core.Manifest) { m.Pull.Repo = "justaname" }, "owner/name"},
		{"pull workflow", func(m *core.Manifest) { m.Pull.Workflow = "" }, "pull.workflow: is required"},
		{"pull artifact", func(m *core.Manifest) { m.Pull.Artifact = "" }, "pull.artifact: is required"},
		{"pull environment missing", func(m *core.Manifest) { m.Pull.Environment = "" }, "pull.environment: is required"},
		{"pull environment unknown", func(m *core.Manifest) { m.Pull.Environment = "nope" }, `environment "nope" is not defined`},
		{"pull look-back", func(m *core.Manifest) { m.Pull.LookbackDays = 0 }, "must be at least 1"},
		{"pull token with a bad name", func(m *core.Manifest) { m.Pull.Token = "bad token" }, "not a valid variable name"},
		{"pull token also an environment secret", func(m *core.Manifest) { m.Pull.Token = "ACCOUNT_PASSWORD" }, "never be handed to a test command"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := valid()
			c.mutate(m)
			errs := Validate(m)
			if len(errs) == 0 {
				t.Fatal("Validate accepted a broken manifest")
			}
			if !containsError(errs, c.want) {
				t.Errorf("no problem contains %q; got:\n%v", c.want, Report(errs))
			}
		})
	}
}

func TestValidatePaths(t *testing.T) {
	for _, bad := range []string{"", "/abs", "../x", "..", "a/../../b", "a//b", "./a", "a/", "."} {
		m := valid()
		m.Tests[0].File = bad
		if !containsError(Validate(m), "tests[0]") {
			t.Errorf("file %q was accepted", bad)
		}
	}
	for _, good := range []string{"a", "a/b.sql", "tests/sql/x.y", "dir.with.dots/file"} {
		m := valid()
		m.Tests[0].File = good
		if errs := Validate(m); len(errs) != 0 {
			t.Errorf("file %q was rejected: %v", good, Report(errs))
		}
	}
}

func TestValidateReportsEveryProblemInStableOrder(t *testing.T) {
	m := valid()
	m.Project = ""
	m.Tests[0].Runner = "nope"
	m.Pull.LookbackDays = 0

	errs := Validate(m)
	if len(errs) != 3 {
		t.Fatalf("got %d problems; want exactly 3:\n%v", len(errs), Report(errs))
	}
	report := Report(errs).Error()
	if !strings.HasPrefix(report, "manifest has 3 problems:") {
		t.Errorf("report starts %q", report)
	}
	// Header problems come before test problems, which come before pull problems.
	iProject, iTest, iPull := strings.Index(report, "project:"), strings.Index(report, "tests[0]"), strings.Index(report, "pull.lookback_days")
	if !(iProject < iTest && iTest < iPull) {
		t.Errorf("problems are out of order:\n%s", report)
	}
}

func TestValidateIsDeterministicAcrossMapOrder(t *testing.T) {
	// Several broken runners and environments: Go randomises map order, so an
	// unsorted implementation would give different reports on different runs.
	build := func() *core.Manifest {
		m := valid()
		for _, name := range []string{"a1", "b2", "c3", "d4"} {
			m.Runners[name] = core.RunnerSpec{Run: []string{"tool"}, Format: "x", Output: "y"}
			m.Environments[name] = core.EnvironmentSpec{Vars: map[string]string{"X_SECRET": "v"}}
		}
		return m
	}
	first := Report(Validate(build())).Error()
	for i := 0; i < 30; i++ {
		if got := Report(Validate(build())).Error(); got != first {
			t.Fatalf("the report changed between runs:\n--- first\n%s\n--- later\n%s", first, got)
		}
	}
}

func TestReport(t *testing.T) {
	if Report(nil) != nil {
		t.Error("no problems must give no error")
	}
	one := Report([]error{errString("a: bad")})
	if one == nil || !strings.HasPrefix(one.Error(), "manifest has 1 problem:") {
		t.Errorf("one problem: %v", one)
	}
	two := Report([]error{errString("a: bad"), errString("b: worse")})
	if two == nil || !strings.Contains(two.Error(), "2 problems:") || !strings.Contains(two.Error(), "\n  - b: worse") {
		t.Errorf("two problems: %v", two)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
