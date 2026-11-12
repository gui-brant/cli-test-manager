package github

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

const token = "ghp_secretvalue123"

var spec = core.PullSpec{Provider: "github", Repo: "owner/name", Workflow: "tests.yml", Artifact: "test-results"}

// fakeGitHub answers the three calls the adapter makes.
type fakeGitHub struct {
	mu        sync.Mutex
	runPages  map[int]string // page number -> JSON body
	artifacts map[string]string
	zips      map[string][]byte
	seen      []http.Header // headers of every request
	override  func(w http.ResponseWriter, r *http.Request) bool
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.Header.Clone())
	f.mu.Unlock()
	if f.override != nil && f.override(w, r) {
		return
	}
	p := r.URL.Path
	parts := strings.Split(p, "/")
	switch {
	case strings.HasSuffix(p, "/actions/workflows/tests.yml/runs"):
		page := 1
		fmt.Sscan(r.URL.Query().Get("page"), &page)
		body, ok := f.runPages[page]
		if !ok {
			body = `{"workflow_runs":[]}`
		}
		fmt.Fprint(w, body)
	case strings.Contains(p, "/actions/runs/") && strings.HasSuffix(p, "/artifacts"):
		body, ok := f.artifacts[parts[len(parts)-2]]
		if !ok {
			body = `{"artifacts":[]}`
		}
		fmt.Fprint(w, body)
	case strings.Contains(p, "/actions/artifacts/") && strings.HasSuffix(p, "/zip"):
		data, ok := f.zips[parts[len(parts)-2]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	default:
		http.NotFound(w, r)
	}
}

func newSource(t *testing.T, f *fakeGitHub) *Source {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s := New(token)
	s.BaseURL = srv.URL
	return s
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, content)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func artifactJSON(entries ...string) string {
	return `{"artifacts":[` + strings.Join(entries, ",") + `]}`
}

func TestListRunsReadsTheFieldsOfEachRun(t *testing.T) {
	f := &fakeGitHub{runPages: map[int]string{1: `{"workflow_runs":[
		{"id":9002,"run_attempt":2,"head_sha":"abc123","status":"completed","created_at":"2026-11-10T09:00:00Z","run_started_at":"2026-11-10T10:00:00Z"},
		{"id":9001,"head_sha":"def456","status":"in_progress","created_at":"2026-11-09T09:00:00Z"}]}`}}
	s := newSource(t, f)

	runs, next, err := s.ListRuns(context.Background(), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if next != "" || len(runs) != 2 {
		t.Fatalf("got %d runs and next %q; want 2 and none", len(runs), next)
	}
	want0 := core.RemoteRun{ExternalID: "9002", Attempt: 2, CommitSHA: "abc123", CreatedAt: time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC), Finished: true}
	if runs[0] != want0 {
		t.Errorf("first run %+v; want %+v (a re-run's time is its start, not its creation)", runs[0], want0)
	}
	want1 := core.RemoteRun{ExternalID: "9001", Attempt: 1, CommitSHA: "def456", CreatedAt: time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC), Finished: false}
	if runs[1] != want1 {
		t.Errorf("second run %+v; want %+v (no attempt means 1; in progress is not finished)", runs[1], want1)
	}
}

func TestListRunsPagesThroughACursor(t *testing.T) {
	var page1 []string
	for i := 0; i < perPage; i++ {
		page1 = append(page1, fmt.Sprintf(`{"id":%d,"status":"completed","created_at":"2026-11-10T09:00:00Z"}`, 1000-i))
	}
	f := &fakeGitHub{runPages: map[int]string{
		1: `{"workflow_runs":[` + strings.Join(page1, ",") + `]}`,
		2: `{"workflow_runs":[{"id":5,"status":"completed","created_at":"2026-11-01T09:00:00Z"}]}`,
	}}
	s := newSource(t, f)

	runs, next, err := s.ListRuns(context.Background(), spec, "")
	if err != nil || len(runs) != perPage || next != "2" {
		t.Fatalf("page 1: %d runs, next %q, err %v; want %d, \"2\", nil", len(runs), next, err, perPage)
	}
	runs, next, err = s.ListRuns(context.Background(), spec, next)
	if err != nil || len(runs) != 1 || next != "" {
		t.Fatalf("page 2: %d runs, next %q, err %v; want 1, \"\", nil", len(runs), next, err)
	}
	if _, _, err := s.ListRuns(context.Background(), spec, "not-a-page"); err == nil {
		t.Error("a cursor that is not a page number was accepted")
	}
}

func TestEveryRequestCarriesTheTokenAndNothingPersonal(t *testing.T) {
	f := &fakeGitHub{runPages: map[int]string{1: `{"workflow_runs":[]}`}}
	s := newSource(t, f)
	if _, _, err := s.ListRuns(context.Background(), spec, ""); err != nil {
		t.Fatal(err)
	}
	h := f.seen[0]
	if h.Get("Authorization") != "Bearer "+token {
		t.Errorf("Authorization is %q", h.Get("Authorization"))
	}
	if h.Get("Accept") != "application/vnd.github+json" || h.Get("User-Agent") != "cli-test-manager" {
		t.Errorf("Accept %q, User-Agent %q", h.Get("Accept"), h.Get("User-Agent"))
	}
}

func TestFetchArtifactReturnsTheOneXMLFile(t *testing.T) {
	f := &fakeGitHub{
		artifacts: map[string]string{"9001": artifactJSON(
			`{"id":11,"name":"other","expired":false,"created_at":"2026-11-10T09:00:00Z"}`,
			`{"id":12,"name":"test-results","expired":false,"created_at":"2026-11-10T09:00:00Z"}`)},
		zips: map[string][]byte{"12": zipOf(t, map[string]string{"results.xml": "<testsuites/>"})},
	}
	s := newSource(t, f)
	rc, err := s.FetchArtifact(context.Background(), spec, core.RemoteRun{ExternalID: "9001", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "<testsuites/>" {
		t.Errorf("got %q", got)
	}
}

func TestTheNewestArtifactOfTheNameIsUsed(t *testing.T) {
	f := &fakeGitHub{
		artifacts: map[string]string{"9001": artifactJSON(
			`{"id":12,"name":"test-results","expired":false,"created_at":"2026-11-10T09:00:00Z"}`,
			`{"id":13,"name":"test-results","expired":false,"created_at":"2026-11-10T11:00:00Z"}`)},
		zips: map[string][]byte{
			"12": zipOf(t, map[string]string{"r.xml": "<old/>"}),
			"13": zipOf(t, map[string]string{"r.xml": "<new/>"}),
		},
	}
	rc, err := newSource(t, f).FetchArtifact(context.Background(), spec, core.RemoteRun{ExternalID: "9001"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	if string(got) != "<new/>" {
		t.Errorf("got %q; want the newer artifact", got)
	}
}

func TestMissingAndExpiredArtifactsAreTold(t *testing.T) {
	f := &fakeGitHub{
		artifacts: map[string]string{
			"1": artifactJSON(`{"id":1,"name":"other","expired":false}`),
			"2": artifactJSON(`{"id":2,"name":"test-results","expired":true}`),
			"3": artifactJSON(`{"id":3,"name":"test-results","expired":false}`),
		},
		zips: map[string][]byte{},
	}
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/artifacts/3/zip") {
			w.WriteHeader(http.StatusGone) // the list said live, the download says gone
			return true
		}
		return false
	}
	s := newSource(t, f)
	for _, c := range []struct {
		id   string
		want error
	}{{"1", core.ErrNoArtifact}, {"2", core.ErrExpired}, {"3", core.ErrExpired}, {"404", core.ErrNoArtifact}} {
		_, err := s.FetchArtifact(context.Background(), spec, core.RemoteRun{ExternalID: c.id})
		if !errors.Is(err, c.want) {
			t.Errorf("run %s: error %v; want %v", c.id, err, c.want)
		}
	}
}

func TestUnusableArchivesAreRefusedWithAReason(t *testing.T) {
	huge := strings.Repeat("a", maxXMLBytes+1)
	for name, c := range map[string]struct {
		zip  []byte
		want string
	}{
		"not a zip":     {[]byte("this is not a zip file at all"), "damaged"},
		"no xml":        {zipOf(t, map[string]string{"notes.txt": "hello"}), "no .xml file"},
		"two xml files": {zipOf(t, map[string]string{"a.xml": "<a/>", "b.xml": "<b/>"}), "2 .xml files"},
		"too large":     {zipOf(t, map[string]string{"big.xml": huge}), "larger than"},
	} {
		f := &fakeGitHub{
			artifacts: map[string]string{"1": artifactJSON(`{"id":7,"name":"test-results","expired":false}`)},
			zips:      map[string][]byte{"7": c.zip},
		}
		_, err := newSource(t, f).FetchArtifact(context.Background(), spec, core.RemoteRun{ExternalID: "1"})
		if !errors.Is(err, core.ErrBadArtifact) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v; want ErrBadArtifact mentioning %q", name, err, c.want)
		}
	}
}

func TestRateLimitsAreToldWithWhenToTryAgain(t *testing.T) {
	cases := map[string]struct {
		status  int
		headers map[string]string
		hint    string
	}{
		"403 with no requests left": {403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1794000000"}, "try again after"},
		"429 with retry-after":      {429, map[string]string{"Retry-After": "30"}, "try again in 30 seconds"},
		"403 with retry-after":      {403, map[string]string{"Retry-After": "60"}, "try again in 60 seconds"},
	}
	for name, c := range cases {
		f := &fakeGitHub{override: func(w http.ResponseWriter, r *http.Request) bool {
			for k, v := range c.headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(c.status)
			return true
		}}
		_, _, err := newSource(t, f).ListRuns(context.Background(), spec, "")
		if !errors.Is(err, core.ErrRateLimited) || !strings.Contains(err.Error(), c.hint) {
			t.Errorf("%s: error %v; want ErrRateLimited with %q", name, err, c.hint)
		}
	}
}

func TestRefusalsSayWhatToCheckAndNeverShowTheToken(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		want   string
	}{
		"401": {401, "token was refused"},
		"403": {403, "may lack permission"},
		"404": {404, "check the repository, workflow and artifact names"},
		"500": {500, "unexpected answer (HTTP 500)"},
	} {
		f := &fakeGitHub{override: func(w http.ResponseWriter, r *http.Request) bool {
			w.WriteHeader(c.status)
			fmt.Fprintf(w, `{"message":"trouble with %s here"}`, token) // a server that echoes the token
			return true
		}}
		_, _, err := newSource(t, f).ListRuns(context.Background(), spec, "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v; want it to contain %q", name, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("%s: the error contains the token: %v", name, err)
		}
	}
}

func TestTheTokenDoesNotFollowARedirectToAnotherHost(t *testing.T) {
	var blobAuth string
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blobAuth = r.Header.Get("Authorization")
		w.Write(zipOf(t, map[string]string{"r.xml": "<ok/>"}))
	}))
	defer blob.Close()

	f := &fakeGitHub{artifacts: map[string]string{"1": artifactJSON(`{"id":7,"name":"test-results","expired":false}`)}}
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/artifacts/7/zip") {
			http.Redirect(w, r, blob.URL+"/download", http.StatusFound) // like GitHub's blob storage
			return true
		}
		return false
	}
	rc, err := newSource(t, f).FetchArtifact(context.Background(), spec, core.RemoteRun{ExternalID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	if string(got) != "<ok/>" {
		t.Errorf("got %q; the download did not follow the redirect", got)
	}
	if blobAuth != "" {
		t.Errorf("the other host received an Authorization header: %q", blobAuth)
	}
}

func TestACancelledRequestReturnsTheContextsError(t *testing.T) {
	f := &fakeGitHub{override: func(w http.ResponseWriter, r *http.Request) bool {
		<-r.Context().Done()
		return true
	}}
	s := newSource(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := s.ListRuns(ctx, spec, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v; want the context's deadline error", err)
	}
}

func TestAnUnreachableServerIsToldWithoutTheToken(t *testing.T) {
	s := New(token)
	s.BaseURL = "http://127.0.0.1:1" // nothing listens here
	_, _, err := s.ListRuns(context.Background(), spec, "")
	if err == nil || !strings.Contains(err.Error(), "cannot reach") || strings.Contains(err.Error(), token) {
		t.Errorf("error %v", err)
	}
}
