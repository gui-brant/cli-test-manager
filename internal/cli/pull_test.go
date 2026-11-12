package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

const pullSection = `
pull:
  provider: github
  repo: owner/name
  workflow: tests.yml
  artifact: test-results
  environment: deployed
  lookback_days: 30
  token: CI_TOKEN
`

const ciToken = "tok-abcdef123"

// fakePullSource is a result source with a few runs and their files.
type fakePullSource struct {
	runs  []core.RemoteRun
	files map[string]string // external id -> XML
	err   map[string]error  // external id -> error when fetching it
	token string            // what NewSource was given
}

func (f *fakePullSource) ListRuns(context.Context, core.PullSpec, string) ([]core.RemoteRun, string, error) {
	return f.runs, "", nil
}

func (f *fakePullSource) FetchArtifact(_ context.Context, _ core.PullSpec, r core.RemoteRun) (io.ReadCloser, error) {
	if err := f.err[r.ExternalID]; err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(f.files[r.ExternalID])), nil
}

func ciRun(id string, minutesAgo int) core.RemoteRun {
	return core.RemoteRun{ExternalID: id, Attempt: 1, CommitSHA: "abc" + id, Finished: true,
		CreatedAt: t0.Add(-time.Duration(minutesAgo) * time.Minute)}
}

const ciXML = `<testsuites>
  <testsuite name="login-flow"><testcase classname="login-flow" name="signs in"/>
    <testcase classname="login-flow" name="rejects a bad password"><failure message="expected 401, got 200"/></testcase></testsuite>
  <testsuite name="stray"><testcase classname="stray" name="unknown"/></testsuite>
</testsuites>`

// pullProject is a project whose manifest has a pull section, with a source.
func pullProject(t *testing.T, src *fakePullSource) *project {
	t.Helper()
	p := newProject(t, twoEnvironments+pullSection)
	p.deps.Creds = fakeCreds{"ACCOUNT_PASSWORD": "pw-12345-abc", "CI_TOKEN": ciToken}
	p.deps.NewSource = func(token string) core.ResultSource {
		src.token = token
		return src
	}
	return p
}

func TestPullStoresNewRunsAndHistoryShowsThemAsRemote(t *testing.T) {
	src := &fakePullSource{runs: []core.RemoteRun{ciRun("9002", 5), ciRun("9001", 30)}, files: map[string]string{"9001": ciXML, "9002": ciXML}}
	p := pullProject(t, src)

	if code := p.do("pull"); code != ExitOK {
		t.Fatalf("exit %d, stderr %s", code, p.stderr())
	}
	contains(t, "pull output", p.stdout(),
		"run 9001#1 (2026-11-10 11:30:00) stored as run 1: 3 results (1 matched no test)",
		"run 9002#1 (2026-11-10 11:55:00) stored as run 2: 3 results (1 matched no test)",
		"Pulled: 2 stored.",
		"2 results matched no test of the manifest",
		"See them with: tester history --source remote")
	if src.token != ciToken {
		t.Errorf("the source was given %q; want the token from the environment", src.token)
	}
	if strings.Index(p.stdout(), "9001#1") > strings.Index(p.stdout(), "9002#1") {
		t.Errorf("the older run should be taken in first:\n%s", p.stdout())
	}

	p.do("history", "--source", "remote")
	contains(t, "history", p.stdout(), "remote", "deployed", "FAIL")
	p.do("history", "2")
	contains(t, "run 2", p.stdout(), "Run 2", "source remote, environment deployed", "remote run 9002, attempt 1, commit abc9002",
		"login-flow", "signs in", "rejects a bad password", "expected 401, got 200")
	for _, out := range []string{p.stdout(), p.stderr()} {
		if strings.Contains(out, ciToken) {
			t.Errorf("the token is in the output:\n%s", out)
		}
	}
}

func TestPullingAgainSaysThereIsNothingNew(t *testing.T) {
	src := &fakePullSource{runs: []core.RemoteRun{ciRun("9001", 30)}, files: map[string]string{"9001": ciXML}}
	p := pullProject(t, src)
	p.do("pull")
	if code := p.do("pull"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	contains(t, "second pull", p.stdout(), "Nothing new: every run is already in the history.")
	if strings.Contains(p.stdout(), "stored as") {
		t.Errorf("a run was stored twice:\n%s", p.stdout())
	}
}

func TestPullWithNoRunsInTheWindowSaysSo(t *testing.T) {
	p := pullProject(t, &fakePullSource{})
	if code := p.do("pull"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	contains(t, "output", p.stdout(), "No runs of the workflow in the last 30 days.")
}

func TestPullShowsAFinishedRunWithoutAFileAsFailed(t *testing.T) {
	src := &fakePullSource{runs: []core.RemoteRun{ciRun("9001", 30)},
		err: map[string]error{"9001": fmt.Errorf("%w: no artifact named %q on run 9001", core.ErrNoArtifact, "test-results")}}
	p := pullProject(t, src)
	if code := p.do("pull"); code != ExitOK {
		t.Fatalf("exit %d, stderr %s", code, p.stderr())
	}
	contains(t, "output", p.stdout(), "stored as failed (run 1): run has no result file: no artifact named", "1 stored as failed")
}

func TestPullWithoutAPullSectionExplains(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.deps.NewSource = func(string) core.ResultSource { return &fakePullSource{} }
	if code := p.do("pull"); code != ExitError {
		t.Fatalf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "no pull section")
	if p.historyExists() {
		t.Error("a refused pull created the history file")
	}
}

func TestPullWithoutTheTokenAsksForItAndCreatesNothing(t *testing.T) {
	src := &fakePullSource{}
	p := pullProject(t, src)
	p.deps.Creds = fakeCreds{} // CI_TOKEN is not set
	if code := p.do("pull"); code != ExitError {
		t.Fatalf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "the pull token CI_TOKEN is not set", "never stored")
	if p.historyExists() {
		t.Error("a pull without a token created the history file")
	}
}

func TestPullNeedsASourceInTheBuild(t *testing.T) {
	p := newProject(t, twoEnvironments+pullSection)
	p.deps.Creds = fakeCreds{"CI_TOKEN": ciToken}
	if code := p.do("pull"); code != ExitError {
		t.Fatalf("exit %d", code)
	}
	contains(t, "stderr", p.stderr(), "pulling is not available in this build")
}

func TestARateLimitKeepsWhatWasStoredAndSaysSo(t *testing.T) {
	src := &fakePullSource{
		runs:  []core.RemoteRun{ciRun("9002", 5), ciRun("9001", 30)},
		files: map[string]string{"9001": ciXML},
		err:   map[string]error{"9002": fmt.Errorf("%w: try again in 30 seconds", core.ErrRateLimited)},
	}
	p := pullProject(t, src)
	if code := p.do("pull"); code != ExitError {
		t.Fatalf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stdout", p.stdout(), "stored as run 1", "Pulled: 1 stored.")
	contains(t, "stderr", p.stderr(), "rate limited by the result source: try again in 30 seconds",
		"the runs above were stored; pulling again continues from there")

	src.err = nil
	src.files["9002"] = ciXML
	if code := p.do("pull"); code != ExitOK {
		t.Fatalf("second pull: exit %d, stderr %s", code, p.stderr())
	}
	contains(t, "second pull", p.stdout(), "run 9002#1", "stored as run 2")
}

func TestACancelledPullExitsWith130AndKeepsWhatWasStored(t *testing.T) {
	src := &fakePullSource{runs: []core.RemoteRun{ciRun("9001", 30)}, files: map[string]string{"9001": ciXML}}
	p := pullProject(t, src)
	ctx, cancel := context.WithCancel(context.Background())
	src.err = map[string]error{"9001": context.Canceled}
	cancel()
	if code := p.doCtx(ctx, "pull"); code != ExitCancelled {
		t.Fatalf("exit %d; want %d", code, ExitCancelled)
	}
	contains(t, "stderr", p.stderr(), "cancelled")
}

func TestPullIsListedInTheHelpAndExplainsItself(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.do("help")
	contains(t, "help", p.stdout(), "pull", "bring in the results of runs a CI service did the work for")
	if code := p.do("pull", "-h"); code != ExitOK {
		t.Errorf("pull -h exit %d", code)
	}
	contains(t, "pull -h", p.stderr(), "Usage: tester pull", "NAME of the environment variable", "stops at the first one it", "already has, so running it again")
}
