package pull

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

func TestCheckCountsFinishedRunsAPullWouldTakeInAndTouchesNothing(t *testing.T) {
	src := &fakeSource{
		pages: [][]core.RemoteRun{{run("3", 1, 1, false), run("2", 1, 5, true), run("1", 1, 10, true)}},
		files: map[string]any{"1#1": passXML, "2#1": passXML},
	}
	s, st := newService(t, src)

	n, err := s.Check(bg, testManifest())
	if err != nil || n != 2 {
		t.Fatalf("checked: %d, %v; want 2 (the run still going does not count)", n, err)
	}
	if len(src.fetches) != 0 || len(storedRuns(t, st)) != 0 {
		t.Errorf("a check downloaded %v and stored %d runs; it must only read", src.fetches, len(storedRuns(t, st)))
	}

	pullOnce(t, s)
	if n, err := s.Check(bg, testManifest()); err != nil || n != 0 {
		t.Errorf("after pulling: %d, %v; want nothing new", n, err)
	}
}

func TestCheckSeesAPlaceholderThatHasFinished(t *testing.T) {
	src := &fakeSource{pages: [][]core.RemoteRun{{run("1", 1, 5, false)}}, files: map[string]any{"1#1": passXML}}
	s, _ := newService(t, src)
	pullOnce(t, s) // stored as a placeholder

	if n, _ := s.Check(bg, testManifest()); n != 0 {
		t.Errorf("a run still going counted as new: %d", n)
	}
	src.pages = [][]core.RemoteRun{{run("1", 1, 5, true)}}
	if n, _ := s.Check(bg, testManifest()); n != 1 {
		t.Errorf("a placeholder that finished: %d; want 1", n)
	}
}

func TestCheckReturnsTheSourcesError(t *testing.T) {
	s, _ := newService(t, errSource{fmt.Errorf("%w: try again in 30 seconds", core.ErrRateLimited)})
	if _, err := s.Check(bg, testManifest()); !errors.Is(err, core.ErrRateLimited) {
		t.Errorf("error %v", err)
	}
	m := testManifest()
	m.Pull = nil
	if _, err := s.Check(bg, m); err == nil {
		t.Error("a manifest without a pull section was accepted")
	}
}

type errSource struct{ err error }

func (e errSource) ListRuns(context.Context, core.PullSpec, string) ([]core.RemoteRun, string, error) {
	return nil, "", e.err
}
func (e errSource) FetchArtifact(context.Context, core.PullSpec, core.RemoteRun) (io.ReadCloser, error) {
	return nil, e.err
}

type creds map[string]string

func (c creds) Get(_ context.Context, name string) (string, error) {
	if v, ok := c[name]; ok {
		return v, nil
	}
	return "", fmt.Errorf("fake: %s: %w", name, core.ErrNotFound)
}

func manifestWithSecrets() *core.Manifest {
	m := testManifest()
	m.Environments = map[string]core.EnvironmentSpec{"deployed": {Secrets: []string{"ACCOUNT_PASSWORD", "OTHER_SECRET"}}}
	return m
}

func TestPrepareReadsTheTokenAndScrubsItWithTheEnvironmentsSecrets(t *testing.T) {
	token, scrub, err := Prepare(bg, creds{"CI_TOKEN": "tok-abcdef123", "ACCOUNT_PASSWORD": "pw-12345-abc"}, manifestWithSecrets())
	if err != nil || token != "tok-abcdef123" {
		t.Fatalf("token %q, error %v", token, err)
	}
	got := scrub.Redact("auth tok-abcdef123 and pw-12345-abc here")
	if strings.Contains(got, "tok-abcdef123") || strings.Contains(got, "pw-12345-abc") {
		t.Errorf("not scrubbed: %q", got)
	}
	// OTHER_SECRET is not set here: that is not a problem for pulling.
}

func TestPrepareWithoutTheTokenIsATokenErrorThatSaysWhatToDo(t *testing.T) {
	_, _, err := Prepare(bg, creds{}, manifestWithSecrets())
	var te *TokenError
	if !errors.As(err, &te) || te.Name != "CI_TOKEN" || !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("error %v; want a TokenError for CI_TOKEN", err)
	}
	if !strings.Contains(err.Error(), "the pull token CI_TOKEN is not set: export it in your shell") {
		t.Errorf("message %q", err)
	}
	_, _, err = Prepare(bg, errCreds{errors.New("the keyring is locked")}, manifestWithSecrets())
	if err == nil || !strings.Contains(err.Error(), "could not be read: the keyring is locked") {
		t.Errorf("message %v", err)
	}
}

type errCreds struct{ err error }

func (e errCreds) Get(context.Context, string) (string, error) { return "", e.err }

func TestPrepareRefusesATokenTooShortToScrubSafely(t *testing.T) {
	if _, _, err := Prepare(bg, creds{"CI_TOKEN": "abc"}, manifestWithSecrets()); err == nil {
		t.Error("a 3-character token was accepted")
	}
}
