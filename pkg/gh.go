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

type GHRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
	HeadSHA    string    `json:"head_sha"`
}

type GHJob struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
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

func (c *GHClient) Dispatch(workflow, ref string, inputs map[string]string) error {
	path := "/repos/" + c.repo + "/actions/workflows/" + url.PathEscape(workflow) + "/dispatches"
	_, err := c.do("POST", path, map[string]any{"ref": ref, "inputs": inputs}, nil)
	return err
}

func (c *GHClient) Run(runID int64) (GHRun, error) {
	var r GHRun
	_, err := c.do("GET", fmt.Sprintf("/repos/%s/actions/runs/%d", c.repo, runID), nil, &r)
	return r, err
}

func (c *GHClient) Jobs(runID int64) ([]GHJob, error) {
	var res struct {
		Jobs []GHJob `json:"jobs"`
	}
	_, err := c.do("GET", fmt.Sprintf("/repos/%s/actions/runs/%d/jobs", c.repo, runID), nil, &res)
	return res.Jobs, err
}

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

func (c *GHClient) Cancel(runID int64) error {
	_, err := c.do("POST", fmt.Sprintf("/repos/%s/actions/runs/%d/cancel", c.repo, runID), nil, nil)
	return err
}

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
