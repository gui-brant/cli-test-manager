package cli

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/source/github"
)

// fakeGitHubServer answers the calls the real adapter makes: one finished run
// with a result file, and one still going.
func fakeGitHubServer(t *testing.T, limited bool) (url string, authSeen func() []string) {
	t.Helper()
	var mu sync.Mutex
	var auths []string

	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	w, _ := zw.Create("results.xml")
	w.Write([]byte(ciXML))
	zw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if limited {
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/actions/workflows/tests.yml/runs"):
			w.Write([]byte(`{"workflow_runs":[
				{"id":9002,"run_attempt":1,"head_sha":"abc9002","status":"in_progress","created_at":"2026-11-10T11:50:00Z"},
				{"id":9001,"run_attempt":1,"head_sha":"abc9001","status":"completed","created_at":"2026-11-10T11:00:00Z"}]}`))
		case strings.HasSuffix(p, "/actions/runs/9001/artifacts"):
			w.Write([]byte(`{"artifacts":[{"id":55,"name":"test-results","expired":false,"created_at":"2026-11-10T11:05:00Z"}]}`))
		case strings.HasSuffix(p, "/actions/artifacts/55/zip"):
			w.Write(zipped.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), auths...)
	}
}

// The whole chain except GitHub itself: the command, the real GitHub adapter,
// the pull service and the history file.
func TestPullThroughTheRealAdapterAgainstAFakeGitHub(t *testing.T) {
	url, auths := fakeGitHubServer(t, false)
	p := newProject(t, twoEnvironments+pullSection)
	p.deps.Creds = fakeCreds{"ACCOUNT_PASSWORD": "pw-12345-abc", "CI_TOKEN": ciToken}
	p.deps.NewSource = func(token string) core.ResultSource {
		s := github.New(token)
		s.BaseURL = url
		return s
	}

	if code := p.do("pull"); code != ExitOK {
		t.Fatalf("exit %d, stderr %s", code, p.stderr())
	}
	contains(t, "pull output", p.stdout(),
		"run 9001#1", "stored as run 1: 3 results (1 matched no test)",
		"run 9002#1", "still going; it will be checked again at the next pull",
		"Pulled: 1 stored, 1 still going.")
	for _, a := range auths() {
		if a != "Bearer "+ciToken {
			t.Errorf("a request carried %q instead of the token", a)
		}
	}

	p.do("history", "--source", "remote")
	contains(t, "history", p.stdout(), "remote", "deployed", "FAIL", "PENDING")
	p.do("history", "1")
	contains(t, "run 1", p.stdout(), "remote run 9001, attempt 1, commit abc9001", "signs in", "rejects a bad password")
}

func TestARateLimitFromTheRealAdapterIsReportedWithWhenToRetry(t *testing.T) {
	url, _ := fakeGitHubServer(t, true)
	p := newProject(t, twoEnvironments+pullSection)
	p.deps.Creds = fakeCreds{"CI_TOKEN": ciToken}
	p.deps.NewSource = func(token string) core.ResultSource {
		s := github.New(token)
		s.BaseURL = url
		return s
	}
	if code := p.do("pull"); code != ExitError {
		t.Fatalf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "rate limited by the result source: try again in 45 seconds")
	if strings.Contains(p.stderr(), ciToken) {
		t.Errorf("the token is in the error:\n%s", p.stderr())
	}
}
