package pkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// GHRun mirrors the relevant fields of a GitHub Actions run.
type GHRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
	HeadSHA    string    `json:"head_sha"`
}

// GHJob mirrors the relevant fields of a GitHub Actions job.
type GHJob struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

// GHClient talks to the GitHub Actions API for one repository.
type GHClient struct {
	baseURL string
	token   string
	repo    string // owner/repo
	http    *http.Client
}

// NewGHClient builds a client. apiBase defaults to https://api.github.com and
// is overridable (CONSTRUCT_GITHUB_API) for tests and proxies.
func NewGHClient(repo, token, apiBase string) *GHClient {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &GHClient{
		baseURL: strings.TrimSuffix(apiBase, "/"),
		token:   token,
		repo:    repo,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// GHToken resolves the API token: CONSTRUCT_GITHUB_TOKEN, GITHUB_TOKEN, then
// `gh auth token`.
func GHToken() string {
	if v := os.Getenv("CONSTRUCT_GITHUB_TOKEN"); v != "" {
		return v
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		return v
	}
	if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return t
		}
	}
	return ""
}

// RepoFromGitRemote derives owner/repo from the origin remote.
func RepoFromGitRemote() (string, error) {
	out, err := exec.Command("git", "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", fmt.Errorf("could not determine the git remote (git config --get remote.origin.url): %w", err)
	}
	return ParseRepoFromRemote(string(out))
}

// ParseRepoFromRemote normalizes any common GitHub remote form into
// owner/repo.
func ParseRepoFromRemote(remote string) (string, error) {
	r := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	switch {
	case strings.HasPrefix(r, "https://") || strings.HasPrefix(r, "http://") || strings.HasPrefix(r, "ssh://"):
		u, err := url.Parse(r)
		if err != nil {
			return "", fmt.Errorf("unrecognized git remote %q", remote)
		}
		if !strings.EqualFold(u.Hostname(), "github.com") {
			return "", fmt.Errorf("remote %q is not a github.com repository", remote)
		}
		return normalizeRepoPath(u.Path)
	case strings.HasPrefix(r, "git@"):
		if i := strings.LastIndex(r, ":"); i >= 0 {
			return normalizeRepoPath(r[i+1:])
		}
	}
	return normalizeRepoPath(r)
}

func normalizeRepoPath(p string) (string, error) {
	p = strings.Trim(p, "/")
	parts := strings.Split(p, "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", fmt.Errorf("unrecognized git remote %q", p)
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}

func (c *GHClient) do(method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("github api %s %s: %s (%s)", method, path, strings.TrimSpace(string(msg)), resp.Status)
	}
	return resp.StatusCode, nil
}

// Dispatch submits a workflow_dispatch event.
func (c *GHClient) Dispatch(workflow, ref string, inputs map[string]string) error {
	path := "/repos/" + c.repo + "/actions/workflows/" + url.PathEscape(workflow) + "/dispatches"
	_, err := c.do("POST", path, map[string]any{"ref": ref, "inputs": inputs}, nil)
	return err
}

// Run fetches one run by ID.
func (c *GHClient) Run(runID int64) (GHRun, error) {
	var r GHRun
	_, err := c.do("GET", fmt.Sprintf("/repos/%s/actions/runs/%d", c.repo, runID), nil, &r)
	return r, err
}

// Jobs fetches the jobs of a run.
func (c *GHClient) Jobs(runID int64) ([]GHJob, error) {
	var res struct {
		Jobs []GHJob `json:"jobs"`
	}
	_, err := c.do("GET", fmt.Sprintf("/repos/%s/actions/runs/%d/jobs", c.repo, runID), nil, &res)
	return res.Jobs, err
}

// JobLogs fetches the plain-text log blob for a job.
func (c *GHClient) JobLogs(jobID int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/repos/%s/actions/jobs/%d/logs", c.baseURL, c.repo, jobID), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("job logs: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// Cancel requests cancellation of a run.
func (c *GHClient) Cancel(runID int64) error {
	_, err := c.do("POST", fmt.Sprintf("/repos/%s/actions/runs/%d/cancel", c.repo, runID), nil, nil)
	return err
}

// LatestDispatchRun returns the newest workflow_dispatch run created at or
// after since (the dispatch we just submitted).
func (c *GHClient) LatestDispatchRun(workflow string, since time.Time) (GHRun, error) {
	q := url.Values{}
	q.Set("event", "workflow_dispatch")
	q.Set("per_page", "10")
	path := "/repos/" + c.repo + "/actions/runs?" + q.Encode() + "&workflow_id=" + url.QueryEscape(workflow)
	var res struct {
		Runs []GHRun `json:"workflow_runs"`
	}
	if _, err := c.do("GET", path, nil, &res); err != nil {
		return GHRun{}, err
	}
	var best GHRun
	for _, r := range res.Runs {
		if r.CreatedAt.Before(since) {
			continue
		}
		if best.ID == 0 || r.CreatedAt.After(best.CreatedAt) {
			best = r
		}
	}
	if best.ID == 0 {
		return GHRun{}, fmt.Errorf("no workflow_dispatch run found yet")
	}
	return best, nil
}

// RunConclusionToExit maps a GitHub run conclusion to an exit code.
func RunConclusionToExit(c string) int {
	if c == "success" || c == "skipped" || c == "neutral" {
		return 0
	}
	return 1
}

// ---- secret redaction ----

var secretKeyRe = regexp.MustCompile(`(?i)(secret|token|password|passwd|api[_-]?key|auth|credential|private[_-]?key)`)

// IsSecretName reports whether an env key looks like it holds a secret.
func IsSecretName(key string) bool {
	return secretKeyRe.MatchString(key)
}

// RedactValues replaces occurrences of the given values in s.
func RedactValues(s string, values []string) string {
	for _, v := range values {
		if len(v) >= 3 {
			s = strings.ReplaceAll(s, v, "*****")
		}
	}
	return s
}
