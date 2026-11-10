package execute

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/runner"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

// The end-to-end test runs the real runner and the real store on small real
// commands. The commands are this test program itself: started with
// E2E_HELPER set, TestHelperProcess behaves as the mode named by its last
// argument (the test's "file") instead of testing anything.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("E2E_HELPER") == "" {
		return
	}
	pw := os.Getenv("ACCOUNT_PASSWORD")
	basic := base64.StdEncoding.EncodeToString([]byte("alice:" + pw))
	token := os.Getenv("CI_TOKEN") // must be empty: the pull token is never passed on

	switch mode := os.Args[len(os.Args)-1]; mode {
	case "tapfail":
		fmt.Printf("1..2\nok 1 - fine\nnot ok 2 - leaks\n# password=%s\n# Authorization: Basic %s\n# token=%s\n", pw, basic, token)
		fmt.Fprintf(os.Stderr, "stderr has the password %s too\n", pw)
		os.Exit(1)
	case "crash":
		fmt.Fprintf(os.Stderr, "fatal: could not log in with password %s (token=%s)\n", pw, token)
		os.Exit(2)
	case "report":
		for _, a := range os.Args {
			if path, ok := strings.CutPrefix(a, "--report="); ok {
				xml := `<testsuite name="s"><testcase name="over http"><failure message="rejected ` + pw + `"/></testcase></testsuite>`
				_ = os.WriteFile(path, []byte(xml), 0o600)
			}
		}
		os.Exit(1)
	}
	os.Exit(0)
}

func TestEndToEndWithTheRealRunnerAndStore(t *testing.T) {
	const (
		pw    = "S3cret!Pass-1"
		token = "ci-token-value-xyz"
	)
	basic := base64.StdEncoding.EncodeToString([]byte("alice:" + pw))
	t.Setenv("CI_TOKEN", token) // the tester's own environment holds the pull token

	exe := os.Args[0]
	m := &core.Manifest{
		Version: 1, Project: "e2e", History: "history.sqlite", Features: []string{"accounts"},
		Environments: map[string]core.EnvironmentSpec{
			"deployed": {
				// GORACE keeps race-built helper processes from pausing at exit.
				Vars:    map[string]string{"E2E_HELPER": "1", "GORACE": "atexit_sleep_ms=0"},
				Secrets: []string{"ACCOUNT_PASSWORD"},
			},
		},
		Runners: map[string]core.RunnerSpec{
			"tap": {
				Run: []string{exe, "-test.run=^TestHelperProcess$", "--", "{files}"}, Format: core.FormatTAP,
				Output: core.OutputStdout, Environments: []string{"deployed"}, Timeout: 60 * time.Second,
			},
			"report": {
				Run: []string{exe, "-test.run=^TestHelperProcess$", "--", "--report={report}", "{files}"}, Format: core.FormatJUnit,
				Output: core.OutputFile, Environments: []string{"deployed"}, Timeout: 60 * time.Second,
			},
		},
		Tests: []core.TestSpec{
			{ID: "tap-fail", Name: "TAP failure", Feature: "accounts", Runner: "tap", File: "tapfail"},
			{ID: "crash", Name: "A crash", Feature: "accounts", Runner: "tap", File: "crash"},
			{ID: "report", Name: "A result file", Feature: "accounts", Runner: "report", File: "report"},
		},
		Pull: &core.PullSpec{
			Provider: "github", Repo: "owner/name", Workflow: "tests.yml", Artifact: "r",
			Environment: "deployed", LookbackDays: 1, Token: "CI_TOKEN",
		},
	}

	historyPath := filepath.Join(t.TempDir(), "history.sqlite")
	store, err := sqlite.Open(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Runner:  runner.New(),
		Creds:   fakeCreds{"ACCOUNT_PASSWORD": pw},
		Store:   store,
		Environ: os.Environ,
	}
	rep, err := svc.Run(bg, Request{Manifest: m, Root: t.TempDir(), Environment: "deployed"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Saved {
		t.Fatal("the run was not saved")
	}
	if v := rep.Run.Verdict(); v.Outcome != core.OutcomeFail {
		t.Errorf("verdict %+v; want fail", v)
	}

	stored, err := store.GetRun(bg, rep.RunID)
	if err != nil {
		t.Fatal(err)
	}

	// What the commands printed was scrubbed before it was parsed.
	byName := map[string]core.Result{}
	for _, r := range stored.Results {
		byName[r.Name] = r
	}
	if r := byName["leaks"]; r.Status != core.StatusFail || !strings.Contains(r.Message, "password=[redacted]") {
		t.Errorf("failing TAP result %+v; want its password scrubbed", r)
	}
	if r := byName["over http"]; r.Status != core.StatusFail || !strings.Contains(r.Message, "rejected [redacted]") {
		t.Errorf("result from the result file %+v; want its password scrubbed", r)
	}
	if r := byName["A crash"]; r.Status != core.StatusError || !strings.HasPrefix(r.Message, "exited with code 2:") || !strings.Contains(r.Message, "[redacted]") {
		t.Errorf("crash result %+v", r)
	}

	// The pull token is in the tester's environment but never reached a command.
	for _, r := range stored.Results {
		if strings.Contains(r.Message, token) {
			t.Errorf("the pull token reached a command and its output: %q", r.Message)
		}
	}
	if r := byName["leaks"]; !strings.Contains(r.Message, "token=") {
		t.Errorf("the command's own output did not show an empty token: %q", r.Message)
	}

	// And not one byte of the history file holds a secret.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range map[string]string{
		"the password": pw, "its base64 form": base64.StdEncoding.EncodeToString([]byte(pw)),
		"a Basic header": basic, "the pull token": token,
	} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("the history file contains %s", name)
		}
	}
}
