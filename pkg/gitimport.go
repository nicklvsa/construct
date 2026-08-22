package pkg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type gitSource struct {
	spec    string
	url     string
	subPath string
	ref     string
	repo    string
}

type ImportLockEntry struct {
	URL  string `json:"url"`
	Ref  string `json:"ref,omitempty"`
	Rev  string `json:"rev"`
	Dir  string `json:"dir,omitempty"`
	Path string `json:"path,omitempty"`
}

type ImportLock struct {
	Imports map[string]ImportLockEntry `json:"imports"`
}

func parseGitSpec(spec string) (gitSource, error) {
	src := gitSource{spec: strings.TrimSpace(spec)}
	s := strings.Trim(src.spec, `"`)
	if s == "" {
		return src, fmt.Errorf("git import requires a repository")
	}
	if i := strings.LastIndex(s, "@"); i > 0 && i > strings.LastIndex(s, "/") {
		src.ref, s = s[i+1:], s[:i]
		if src.ref == "" {
			return src, fmt.Errorf("git import %q: empty @ref", src.spec)
		}
	}

	var tail string
	switch {
	case strings.HasPrefix(s, "https://"):
		tail = strings.TrimPrefix(s, "https://")
	case strings.HasPrefix(s, "git@"), strings.Contains(s, "://"):
		src.url = s
	default:
		if !strings.Contains(s, "/") {
			return src, fmt.Errorf("git import %q: expected owner/repo, host/owner/repo, or a URL", s)
		}
		first := s[:strings.Index(s, "/")]
		if strings.Contains(first, ".") {
			tail = s
		} else {
			tail = "github.com/" + s
		}
	}

	if tail != "" {
		parts := strings.Split(strings.TrimSuffix(tail, "/"), "/")
		if len(parts) < 3 {
			return src, fmt.Errorf("git import %q: expected host/owner/repo (or owner/repo for GitHub)", s)
		}
		repo := strings.TrimSuffix(parts[2], ".git")
		src.url = "https://" + parts[0] + "/" + parts[1] + "/" + repo
		src.subPath = strings.Join(parts[3:], "/")
		src.repo = repo
	} else if src.repo == "" {
		rest := src.url
		if i := strings.LastIndexAny(rest, "/:"); i >= 0 {
			rest = rest[i+1:]
		}
		src.repo = strings.TrimSuffix(rest, ".git")
	}
	if src.repo == "" {
		return src, fmt.Errorf("git import %q: could not determine the repository name", s)
	}
	return src, nil
}

func importRootDir(from string) string {
	dir := filepath.Clean(from)
	for {
		if filepath.Base(dir) == CacheDirName() {
			return filepath.Dir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	dir = filepath.Clean(from)
	for {
		if _, err := os.Stat(filepath.Join(dir, "Constfile")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(from)
		}
		dir = parent
	}
}

func importLockPath(root string) string {
	return filepath.Join(root, ".construct.lock")
}

func loadImportLock(path string) *ImportLock {
	lock := &ImportLock{Imports: map[string]ImportLockEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return lock
	}
	if json.Unmarshal(data, lock) != nil || lock.Imports == nil {
		lock.Imports = map[string]ImportLockEntry{}
	}
	return lock
}

func saveImportLock(path string, lock *ImportLock) error {
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func runGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}

func ensureGitImport(spec, baseDir string) (string, string, error) {
	src, err := parseGitSpec(spec)
	if err != nil {
		return "", "", err
	}
	root := importRootDir(baseDir)
	lockPath := importLockPath(root)
	lock := loadImportLock(lockPath)
	entry, locked := lock.Imports[src.spec]

	dir := filepath.Join(root, CacheDirName(), "imports", shortHash(src.spec))
	if !locked || !dirExists(filepath.Join(dir, ".git")) {
		ref := src.ref
		if locked && ref == "" {
			ref = entry.Rev
		}
		if err := gitClone(src.url, ref, dir); err != nil {
			return "", "", fmt.Errorf("import %q: %w", src.spec, err)
		}
		rev := gitRev(dir)
		if rev == "" {
			return "", "", fmt.Errorf("import %q: could not read the fetched revision", src.spec)
		}
		lock.Imports[src.spec] = ImportLockEntry{URL: src.url, Ref: src.ref, Rev: rev, Dir: dir}
		if err := saveImportLock(lockPath, lock); err != nil {
			return "", "", fmt.Errorf("import %q: could not save %s: %w", src.spec, filepath.Base(lockPath), err)
		}
		fmt.Fprintf(os.Stderr, "(import: fetched %s @ %s)\n", src.repo, shortRev(rev))
	}

	file := filepath.Join(dir, src.subPath, "Constfile")
	if _, err := os.Stat(file); err != nil {
		return "", "", fmt.Errorf("import %q: no Constfile found at %s", src.spec, filepath.Join(src.repo, src.subPath, "Constfile"))
	}
	return file, src.repo, nil
}

func gitClone(url, ref, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	os.RemoveAll(dst)

	if isCommitSHA(ref) {
		if _, err := runGit("", "clone", "--quiet", "--filter=blob:none", "--no-checkout", url, dst); err != nil {
			return err
		}
		if _, err := runGit(dst, "fetch", "--quiet", "--depth", "1", "origin", ref); err != nil {
			return err
		}
		_, err := runGit(dst, "checkout", "--quiet", "--detach", "FETCH_HEAD")
		return err
	}

	args := []string{"clone", "--quiet", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, url, dst)
	_, err := runGit("", args...)
	return err
}

func isCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	_, err := hex.DecodeString(ref)
	return err == nil
}

func gitRev(dir string) string {
	out, err := runGit(dir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func UpdateGitImports(baseDir string, specs []string) (int, error) {
	root := importRootDir(baseDir)
	lockPath := importLockPath(root)
	lock := loadImportLock(lockPath)
	if len(lock.Imports) == 0 {
		return 0, fmt.Errorf("no remote imports recorded in %s", filepath.Base(lockPath))
	}

	keys := make([]string, 0, len(lock.Imports))
	for k := range lock.Imports {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	updated := 0
	for _, k := range keys {
		if len(specs) > 0 && !slices.Contains(specs, k) && !slices.Contains(specs, trimAtRef(k)) {
			continue
		}
		entry := lock.Imports[k]
		if isCommitSHA(entry.Ref) {
			fmt.Printf("%s: pinned to a commit; nothing to update\n", k)
			continue
		}
		if entry.Dir == "" || !dirExists(filepath.Join(entry.Dir, ".git")) {
			if err := gitClone(entry.URL, firstNonEmpty(entry.Ref, entry.Rev), entry.Dir); err != nil {
				return updated, fmt.Errorf("import %q: %w", k, err)
			}
		}
		refname := entry.Ref
		if refname == "" {
			var err error
			refname, err = gitDefaultBranch(entry.URL)
			if err != nil {
				return updated, fmt.Errorf("import %q: %w", k, err)
			}
		}
		if _, err := runGit(entry.Dir, "fetch", "--quiet", "--depth", "1", "origin", refname); err != nil {
			return updated, fmt.Errorf("import %q: %w", k, err)
		}
		if _, err := runGit(entry.Dir, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
			return updated, fmt.Errorf("import %q: %w", k, err)
		}
		rev := gitRev(entry.Dir)
		if rev != "" && rev != entry.Rev {
			fmt.Printf("%s: %s -> %s\n", k, shortRev(entry.Rev), shortRev(rev))
			entry.Rev = rev
			lock.Imports[k] = entry
			updated++
		} else if rev != "" {
			fmt.Printf("%s: already at %s\n", k, shortRev(rev))
		}
	}
	if updated > 0 {
		if err := saveImportLock(lockPath, lock); err != nil {
			return updated, err
		}
		fmt.Printf("updated %d import(s); commit %s to pin the change\n", updated, filepath.Base(lockPath))
	}
	return updated, nil
}

func gitDefaultBranch(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "ls-remote", "--symref", url, "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("could not resolve the default branch: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "ref: "); ok {
			if ref, _, found := strings.Cut(rest, "\t"); found {
				return strings.TrimSpace(ref), nil
			}
		}
	}
	return "", fmt.Errorf("could not parse the default branch from ls-remote")
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

func shortRev(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

func trimAtRef(spec string) string {
	if i := strings.LastIndex(spec, "@"); i > 0 && i > strings.LastIndex(spec, "/") {
		return spec[:i]
	}
	return spec
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
