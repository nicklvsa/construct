package pkg

import (
	"bytes"
	"context"
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

const maxLogBytes = 8 << 20

type GHRun struct {
	ID         int64     `json:"id"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
}

type GHJob struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type GHClient struct {
	baseURL string
	token   string
	repo    string // owner/repo
	http    *http.Client
}

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

func RepoFromGitRemote() (string, error) {
	out, err := exec.Command("git", "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", fmt.Errorf("could not determine the git remote (git config --get remote.origin.url): %w", err)
	}
	return ParseRepoFromRemote(string(out))
}

func ParseRepoFromRemote(remote string) (string, error) {
	r := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	switch {
	case strings.HasPrefix(r, "https://") || strings.HasPrefix(r, "http://") || strings.HasPrefix(r, "ssh://"):
		u, err := url.Parse(r)
		if err != nil {
			return "", fmt.Errorf("unrecognized git remote %q: %w", remote, err)
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

func (c *GHClient) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
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
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode github response %s %s (status %d): %w", method, path, resp.StatusCode, err)
		}
		return resp.StatusCode, nil
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("github api %s %s: %s (%s)", method, path, strings.TrimSpace(string(msg)), resp.Status)
	}
	return resp.StatusCode, nil
}

func (c *GHClient) Dispatch(ctx context.Context, workflow, ref string, inputs map[string]string) error {
	path := "/repos/" + c.repo + "/actions/workflows/" + url.PathEscape(workflow) + "/dispatches"
	_, err := c.do(ctx, "POST", path, map[string]any{"ref": ref, "inputs": inputs}, nil)
	return err
}

func (c *GHClient) Run(ctx context.Context, runID int64) (GHRun, error) {
	var r GHRun
	_, err := c.do(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs/%d", c.repo, runID), nil, &r)
	return r, err
}

func (c *GHClient) Jobs(ctx context.Context, runID int64) ([]GHJob, error) {
	var res struct {
		Jobs []GHJob `json:"jobs"`
	}
	_, err := c.do(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs/%d/jobs", c.repo, runID), nil, &res)
	return res.Jobs, err
}

func (c *GHClient) JobLogs(ctx context.Context, jobID int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/actions/jobs/%d/logs", c.baseURL, c.repo, jobID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
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
	return io.ReadAll(io.LimitReader(resp.Body, maxLogBytes))
}

func (c *GHClient) Cancel(ctx context.Context, runID int64) error {
	_, err := c.do(ctx, "POST", fmt.Sprintf("/repos/%s/actions/runs/%d/cancel", c.repo, runID), nil, nil)
	return err
}

func (c *GHClient) LatestDispatchRun(ctx context.Context, workflow string, since time.Time) (GHRun, error) {
	q := url.Values{}
	q.Set("event", "workflow_dispatch")
	q.Set("per_page", "10")
	path := "/repos/" + c.repo + "/actions/runs?" + q.Encode() + "&workflow_id=" + url.QueryEscape(workflow)
	var res struct {
		Runs []GHRun `json:"workflow_runs"`
	}
	if _, err := c.do(ctx, "GET", path, nil, &res); err != nil {
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

func RunConclusionToExit(c string) int {
	if c == "success" || c == "skipped" || c == "neutral" {
		return 0
	}
	return 1
}

var secretKeyRe = regexp.MustCompile(`(?i)(secret|token|password|passwd|api[_-]?key|auth|credential|private[_-]?key)`)

func IsSecretName(key string) bool {
	return secretKeyRe.MatchString(key)
}

func RedactValues(s string, values []string) string {
	for _, v := range values {
		if len(v) >= 3 {
			s = strings.ReplaceAll(s, v, "*****")
		}
	}
	return s
}
