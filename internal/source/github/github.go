// Package github reads a workflow's results from GitHub Actions. It is the one
// place that knows GitHub's interface; the rest of the program sees only the
// core.ResultSource port, so another service can replace it.
//
// What it asks GitHub for, all read-only:
//   - the runs of a workflow, a page at a time, newest first;
//   - the artifacts of one run, to find the one with the manifest's name;
//   - that artifact's zip archive, from which it returns the one .xml file.
//
// These calls follow GitHub's documented REST interface but have not been
// checked against a real repository yet: the first real pull is that check.
package github

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// DefaultBaseURL is GitHub's public interface.
const DefaultBaseURL = "https://api.github.com"

const (
	perPage = 30
	// maxZipBytes bounds the download, and maxXMLBytes what is read out of it,
	// so a huge or hostile archive cannot fill the machine's memory.
	maxZipBytes = 64 << 20
	maxXMLBytes = 32 << 20
	// maxBodyBytes bounds the answers that are not archives.
	maxBodyBytes = 8 << 20
)

// Source talks to GitHub with one token.
type Source struct {
	// BaseURL is the interface's address. Empty means DefaultBaseURL; tests
	// point it at a fake server.
	BaseURL string
	token   string
	client  *http.Client
}

// New makes a source that authenticates with token. The token is sent only in a
// request header, to the interface's own address, and never appears in an
// error.
func New(token string) *Source {
	s := &Source{token: token}
	s.client = &http.Client{
		Timeout: 60 * time.Second,
		// GitHub answers an archive download with a redirect to another host.
		// The token must not follow it there.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("github: too many redirects")
			}
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
	return s
}

func (s *Source) base() string {
	if s.BaseURL != "" {
		return strings.TrimRight(s.BaseURL, "/")
	}
	return DefaultBaseURL
}

// repoPath is "/repos/owner/name" with each part escaped.
func repoPath(repo string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// ListRuns returns one page of the workflow's runs, newest first. The cursor is
// the page number, empty for the first.
func (s *Source) ListRuns(ctx context.Context, spec core.PullSpec, cursor string) ([]core.RemoteRun, string, error) {
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return nil, "", fmt.Errorf("github: %q is not a page cursor", cursor)
		}
		page = n
	}
	u := fmt.Sprintf("%s%s/actions/workflows/%s/runs?per_page=%d&page=%d",
		s.base(), repoPath(spec.Repo), url.PathEscape(spec.Workflow), perPage, page)

	body, err := s.get(ctx, u, maxBodyBytes)
	if err != nil {
		return nil, "", err
	}
	var doc struct {
		Runs []struct {
			ID           int64     `json:"id"`
			Attempt      int       `json:"run_attempt"`
			HeadSHA      string    `json:"head_sha"`
			Status       string    `json:"status"`
			CreatedAt    time.Time `json:"created_at"`
			RunStartedAt time.Time `json:"run_started_at"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, "", fmt.Errorf("github: the list of runs is not what was expected: %w", err)
	}

	runs := make([]core.RemoteRun, 0, len(doc.Runs))
	for _, r := range doc.Runs {
		attempt := r.Attempt
		if attempt < 1 {
			attempt = 1
		}
		// A re-run keeps created_at but starts again, so the start is the time.
		when := r.RunStartedAt
		if when.IsZero() {
			when = r.CreatedAt
		}
		runs = append(runs, core.RemoteRun{
			ExternalID: strconv.FormatInt(r.ID, 10),
			Attempt:    attempt,
			CommitSHA:  r.HeadSHA,
			CreatedAt:  when,
			Finished:   r.Status == "completed",
		})
	}
	next := ""
	if len(doc.Runs) >= perPage {
		next = strconv.Itoa(page + 1)
	}
	return runs, next, nil
}

// FetchArtifact returns the content of the one .xml file in the run's artifact
// with the manifest's name. A run's attempts share its artifacts, so the newest
// artifact of that name is used.
func (s *Source) FetchArtifact(ctx context.Context, spec core.PullSpec, run core.RemoteRun) (io.ReadCloser, error) {
	list := fmt.Sprintf("%s%s/actions/runs/%s/artifacts?name=%s&per_page=100",
		s.base(), repoPath(spec.Repo), url.PathEscape(run.ExternalID), url.QueryEscape(spec.Artifact))
	body, err := s.get(ctx, list, maxBodyBytes)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Artifacts []struct {
			ID        int64     `json:"id"`
			Name      string    `json:"name"`
			Expired   bool      `json:"expired"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("github: the list of artifacts is not what was expected: %w", err)
	}

	type candidate struct {
		id      int64
		created time.Time
	}
	var live []candidate
	seen := false
	for _, a := range doc.Artifacts {
		if a.Name != spec.Artifact {
			continue
		}
		seen = true
		if !a.Expired {
			live = append(live, candidate{a.ID, a.CreatedAt})
		}
	}
	switch {
	case !seen:
		return nil, fmt.Errorf("%w: no artifact named %q on run %s", core.ErrNoArtifact, spec.Artifact, run.ExternalID)
	case len(live) == 0:
		return nil, fmt.Errorf("%w: the artifact %q of run %s has expired", core.ErrExpired, spec.Artifact, run.ExternalID)
	}
	sort.Slice(live, func(i, j int) bool { return live[i].created.After(live[j].created) })

	// The address is built from the id, not taken from the answer, so the token
	// only ever goes to the interface's own address.
	zipURL := fmt.Sprintf("%s%s/actions/artifacts/%d/zip", s.base(), repoPath(spec.Repo), live[0].id)
	data, err := s.get(ctx, zipURL, maxZipBytes)
	if err != nil {
		return nil, err
	}
	xml, err := unzipOne(data)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(xml)), nil
}

// unzipOne returns the content of the only .xml file in the archive.
func unzipOne(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: the archive is damaged: %v", core.ErrBadArtifact, err)
	}
	var files []*zip.File
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() && strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
			files = append(files, f)
		}
	}
	switch len(files) {
	case 0:
		return nil, fmt.Errorf("%w: the archive holds no .xml file", core.ErrBadArtifact)
	case 1:
	default:
		return nil, fmt.Errorf("%w: the archive holds %d .xml files; the workflow should upload one", core.ErrBadArtifact, len(files))
	}
	rc, err := files[0].Open()
	if err != nil {
		return nil, fmt.Errorf("%w: the archive is damaged: %v", core.ErrBadArtifact, err)
	}
	defer rc.Close()
	// Read one byte past the limit to tell "exactly full" from "too big"; the
	// archive's own size claim is not trusted.
	out, err := io.ReadAll(io.LimitReader(rc, maxXMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: the archive is damaged: %v", core.ErrBadArtifact, err)
	}
	if len(out) > maxXMLBytes {
		return nil, fmt.Errorf("%w: the result file is larger than %d MB", core.ErrBadArtifact, maxXMLBytes>>20)
	}
	return out, nil
}

// get makes one authenticated request and returns the answer's body, or an
// error that says what went wrong without ever containing the token.
func (s *Source) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("github: %s", s.clean(err.Error()))
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "cli-test-manager")

	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("github: cannot reach it: %s", s.clean(err.Error()))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, fmt.Errorf("github: the answer was cut off: %s", s.clean(err.Error()))
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("%w: the download is larger than %d MB", core.ErrBadArtifact, limit>>20)
		}
		return data, nil
	}

	// An error answer: a short message from GitHub helps, a long one does not.
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var parsed struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(msg, &parsed)
	why := s.clean(strings.TrimSpace(parsed.Message))
	if why != "" {
		why = ": " + why
	}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""):
		return nil, fmt.Errorf("%w%s", core.ErrRateLimited, retryHint(resp.Header))
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("github: the token was refused (HTTP 401)%s; check that it is valid and has not expired", why)
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("github: access denied (HTTP 403)%s; the token may lack permission to read this repository's workflow runs", why)
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("github: not found (HTTP 404); check the repository, workflow and artifact names in the manifest (a private repository also answers 404 when the token cannot see it)")
	case resp.StatusCode == http.StatusGone:
		return nil, fmt.Errorf("%w: GitHub no longer has it (HTTP 410)", core.ErrExpired)
	}
	return nil, fmt.Errorf("github: unexpected answer (HTTP %d)%s", resp.StatusCode, why)
}

// retryHint says when to try again, from what GitHub sent.
func retryHint(h http.Header) string {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return fmt.Sprintf(": try again in %d seconds", secs)
		}
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			return ": try again after " + time.Unix(unix, 0).UTC().Format("15:04:05 UTC")
		}
	}
	return ""
}

// clean removes the token from text that is about to become part of an error.
func (s *Source) clean(text string) string {
	if s.token != "" {
		text = strings.ReplaceAll(text, s.token, "[redacted]")
	}
	return text
}
