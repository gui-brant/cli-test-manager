package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

const pullSection = `
pull:
  provider: github
  repo: owner/name
  workflow: tests.yml
  artifact: test-results
  environment: staging
  lookback_days: 30
  token: CI_TOKEN
`

var remoteNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

type mapCreds map[string]string

func (c mapCreds) Get(_ context.Context, name string) (string, error) {
	if v, ok := c[name]; ok {
		return v, nil
	}
	return "", fmt.Errorf("fake: %s: %w", name, core.ErrNotFound)
}

// remoteSource is a result source the tests change between calls.
type remoteSource struct {
	mu        sync.Mutex
	runs      []core.RemoteRun
	err       error // returned when listing
	lists     int
	downloads int
}

func (s *remoteSource) ListRuns(context.Context, core.PullSpec, string) ([]core.RemoteRun, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists++
	if s.err != nil {
		return nil, "", s.err
	}
	return append([]core.RemoteRun(nil), s.runs...), "", nil
}

func (s *remoteSource) FetchArtifact(context.Context, core.PullSpec, core.RemoteRun) (io.ReadCloser, error) {
	s.mu.Lock()
	s.downloads++
	s.mu.Unlock()
	return io.NopCloser(strings.NewReader(`<testsuite name="t1"><testcase classname="t1" name="signs in"/></testsuite>`)), nil
}

func (s *remoteSource) set(runs ...core.RemoteRun) {
	s.mu.Lock()
	s.runs, s.err = runs, nil
	s.mu.Unlock()
}

func (s *remoteSource) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *remoteSource) counts() (lists, downloads int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lists, s.downloads
}

func ciRun(id string, minutesAgo int) core.RemoteRun {
	return core.RemoteRun{ExternalID: id, Attempt: 1, CommitSHA: "sha" + id, Finished: true,
		CreatedAt: remoteNow.Add(-time.Duration(minutesAgo) * time.Minute)}
}

// remoteMenu is a menu on a project with this manifest, with a fake source.
func remoteMenu(t *testing.T, manifest string, src *remoteSource, creds mapCreds) (*Model, string) {
	t.Helper()
	m, file, _ := historyMenu(t, manifest)
	m.deps.Creds = creds
	m.deps.Environ = func() []string { return nil }
	m.deps.Now = func() time.Time { return remoteNow }
	m.deps.CheckEvery = time.Millisecond
	m.deps.NewSource = func(string) core.ResultSource { return src }
	m.deps.Save = func(path string) (core.Store, error) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		return sqlite.Open(path)
	}
	m.stack = []screen{newChooser(m.deps)}
	return m, file
}

// settle delivers what the commands report, and what theirs report, to the menu,
// without waiting for the timer: a tick ends that branch.
func settle(m *Model, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case remoteTick:
			// a timer; the tests deliver ticks themselves
		default:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
}

var goodCreds = mapCreds{"CI_TOKEN": "tok-abcdef123"}

func TestOpeningAProjectWithAPullSectionPullsAndSaysSo(t *testing.T) {
	src := &remoteSource{}
	src.set(ciRun("2", 5), ciRun("1", 10))
	m, file := remoteMenu(t, twoEnvironments+pullSection, src, goodCreds)

	cmd := press(m, "enter") // open the project
	mustContain(t, "footer", m.View(), "GitHub: pulling...")
	settle(m, cmd)

	mustContain(t, "footer", m.View(), "GitHub: connected", "pulled 12:00 (2 new runs)", "p pull")
	if strings.Contains(m.View(), "●") {
		t.Errorf("a dot shows although everything was just pulled:\n%s", m.View())
	}
	if n := storedRuns(t, file); n != 2 {
		t.Errorf("%d runs stored; want 2", n)
	}
}

func TestAProjectWithoutAPullSectionShowsNothingAboutGitHub(t *testing.T) {
	src := &remoteSource{}
	m, _ := remoteMenu(t, twoEnvironments, src, goodCreds)
	settle(m, press(m, "enter"))
	lists, _ := src.counts()
	if strings.Contains(m.View(), "GitHub") || strings.Contains(m.View(), "p pull") || lists != 0 {
		t.Errorf("a project with nothing to pull from shows or uses GitHub (%d calls):\n%s", lists, m.View())
	}
	if m.remote != nil {
		t.Error("a link was made for a project with no pull section")
	}
}

func TestThePullKeyBringsInNewRunsAndTheOpenHistoryShowsThem(t *testing.T) {
	src := &remoteSource{}
	src.set(ciRun("1", 10))
	m, _ := remoteMenu(t, twoEnvironments+pullSection, src, goodCreds)
	settle(m, press(m, "enter"))                   // open the project; its first pull
	press(m, "enter", "down", "enter")             // birds › staging (where the pulled runs belong) › actions
	press(m, "enter")                              // Run history
	mustContain(t, "history", m.View(), "> Run 1") // the first pull's run
	if _, ok := top(m).(*historyScreen); !ok {
		t.Fatalf("top screen is %T; want the history", top(m))
	}

	src.set(ciRun("2", 2), ciRun("1", 10))
	cmd := press(m, "p")
	mustContain(t, "footer", m.View(), "GitHub: pulling...")
	settle(m, cmd)
	mustContain(t, "history", m.View(), "Run 2", "Run 1", "pulled 12:00 (1 new run)")
}

func TestTheCheckShowsADotWhenGitHubHasRunsNotPulledYet(t *testing.T) {
	src := &remoteSource{}
	src.set(ciRun("1", 10))
	m, _ := remoteMenu(t, twoEnvironments+pullSection, src, goodCreds)
	settle(m, press(m, "enter"))
	if _, downloads := src.counts(); downloads != 1 {
		t.Fatalf("%d downloads after the first pull; want 1", downloads)
	}

	src.set(ciRun("3", 1), ciRun("2", 2), ciRun("1", 10)) // two new runs appear on GitHub
	_, cmd := m.Update(remoteTick{m.remote})
	mustContain(t, "footer", m.View(), "GitHub: checking for new runs...")
	settle(m, cmd)

	mustContain(t, "footer", m.View(), "2 new runs on GitHub, not pulled yet", "p pull ●")
	if _, downloads := src.counts(); downloads != 1 {
		t.Errorf("the check downloaded files (%d downloads); it must only list", downloads)
	}

	settle(m, press(m, "p")) // pulling clears the dot
	if _, downloads := src.counts(); strings.Contains(m.View(), "●") || downloads != 3 {
		t.Errorf("after pulling: %d downloads, view:\n%s", downloads, m.View())
	}
}

func TestOnlyOneTimerRunsAtATime(t *testing.T) {
	src := &remoteSource{}
	src.set(ciRun("1", 10))
	m, _ := remoteMenu(t, twoEnvironments+pullSection, src, goodCreds)
	settle(m, press(m, "enter"))
	if !m.remote.tickPending {
		t.Fatal("no timer after the first pull")
	}
	// Pulling again while the timer runs must not start a second timer.
	_, cmd := m.Update(key("p"))
	_, next := m.Update(cmd())
	if next != nil {
		t.Errorf("a second timer was started")
	}
	// A tick that arrives while a call is going is put off, not answered with another call.
	m.remote.busy = "pulling"
	m.remote.tickPending = false
	_, cmd = m.Update(remoteTick{m.remote})
	if cmd == nil || m.remote.busy != "pulling" {
		t.Errorf("a tick during a pull should only reschedule")
	}
}

func TestWithoutTheTokenTheFooterSaysWhatToDoAndNothingIsCreated(t *testing.T) {
	src := &remoteSource{}
	m, file := remoteMenu(t, twoEnvironments+pullSection, src, mapCreds{})
	settle(m, press(m, "enter"))
	mustContain(t, "footer", m.View(), "GitHub: not connected: the pull token CI_TOKEN is not set: export it in your shell")
	if lists, _ := src.counts(); lists != 0 {
		t.Error("GitHub was asked without a token")
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("a pull without a token created the history file")
	}
}

func TestAProblemWithGitHubIsShownAndRecoversOnTheNextPull(t *testing.T) {
	src := &remoteSource{}
	src.fail(errors.New("github: the token was refused (HTTP 401): Bad credentials; check that it is valid and has not expired"))
	m, _ := remoteMenu(t, twoEnvironments+pullSection, src, goodCreds)
	settle(m, press(m, "enter"))
	mustContain(t, "footer", m.View(), "GitHub: not connected: the token was refused (HTTP 401)")
	if strings.Contains(m.View(), "tok-abcdef123") {
		t.Error("the token is on the screen")
	}

	src.set(ciRun("1", 10))
	settle(m, press(m, "p"))
	mustContain(t, "footer", m.View(), "GitHub: connected", "(1 new run)")
}

func TestARateLimitIsShownWithWhenToTryAgain(t *testing.T) {
	src := &remoteSource{}
	src.fail(fmt.Errorf("%w: try again in 45 seconds", core.ErrRateLimited))
	m, _ := remoteMenu(t, twoEnvironments+pullSection, src, goodCreds)
	settle(m, press(m, "enter"))
	mustContain(t, "footer", m.View(), "GitHub: not connected: rate limited by the result source: try again in 45 seconds")
}

func TestThePullKeyIsLeftAloneOnAQuestion(t *testing.T) {
	src := &remoteSource{}
	src.set(ciRun("1", 10))
	m, _ := remoteMenu(t, twoEnvironments+pullSection, src, goodCreds)
	m.deps.Runner = &passingRunner{}
	m.stack = []screen{newChooser(m.deps)}
	settle(m, press(m, "enter"))
	before, _ := src.counts()

	press(m, "enter", "enter")                // birds › local, then the actions
	press(m, "down", "down", "down", "enter") // Run all: the question
	if _, ok := top(m).(*confirmScreen); !ok {
		t.Fatalf("top screen is %T; want the question", top(m))
	}
	if strings.Contains(m.View(), "p pull") {
		t.Errorf("the question offers the pull key:\n%s", m.View())
	}
	cmd := press(m, "p")
	if after, _ := src.counts(); cmd != nil || m.remote.busy != "" || after != before {
		t.Error("p on the question started a pull")
	}
	if _, ok := top(m).(*actionScreen); !ok {
		t.Errorf("p on the question went to %T; any key but y goes back", top(m))
	}
}

func TestLeavingTheProjectEndsTheLinkAndIgnoresLateAnswers(t *testing.T) {
	src := &remoteSource{}
	src.set(ciRun("1", 10))
	m, _ := remoteMenu(t, twoEnvironments+pullSection, src, goodCreds)
	cmd := press(m, "enter") // the pull is on its way
	press(m, "esc")          // back to the projects before it answers
	if m.remote != nil {
		t.Fatal("the link outlived the project")
	}
	settle(m, cmd) // the late answer arrives
	if strings.Contains(m.View(), "GitHub") || m.remote != nil {
		t.Errorf("a late answer came back to life:\n%s", m.View())
	}
	if _, ok := top(m).(*chooser); !ok {
		t.Errorf("top screen is %T; want the projects", top(m))
	}
}

func TestQAndCtrlCStillLeaveWithTheLinkOpen(t *testing.T) {
	for _, quit := range []string{"q", "ctrl+c"} {
		src := &remoteSource{}
		m, _ := remoteMenu(t, twoEnvironments+pullSection, src, goodCreds)
		settle(m, press(m, "enter"))
		if !quits(press(m, quit)) {
			t.Errorf("%s did not leave", quit)
		}
	}
}

func TestTheFooterSurvivesATinyTerminal(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {10, 2}, {30, 5}, {200, 3}} {
		src := &remoteSource{}
		src.set(ciRun("1", 10))
		m, _ := remoteMenu(t, twoEnvironments+pullSection, src, goodCreds)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		cmd := press(m, "enter")
		_ = m.View()
		settle(m, cmd)
		_ = m.View()
		press(m, "enter", "enter", "down", "enter", "esc", "esc")
		_ = m.View()
	}
}
