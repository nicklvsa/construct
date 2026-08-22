package pkg

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func GitChangedFiles(dir, ref string) (map[string]bool, error) {
	if _, err := runGitCmd(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return nil, fmt.Errorf("--since: unknown git ref %q in %s", ref, dir)
	}

	root, err := runGitCmd(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("--since: %s is not inside a git repository", dir)
	}

	if resolved, rerr := filepath.EvalSymlinks(strings.TrimSpace(root)); rerr == nil {
		root = resolved
	} else {
		root = strings.TrimSpace(root)
	}

	changed := map[string]bool{}
	add := func(out []byte, err error) error {
		if err != nil {
			return err
		}
		for _, name := range bytes.Split(bytes.TrimSpace(out), []byte{0}) {
			if len(name) == 0 {
				continue
			}
			abs := filepath.Join(root, filepath.FromSlash(string(name)))
			changed[abs] = true
		}
		return nil
	}
	// Tracked changes (index + working tree) vs the ref; deletions included.
	if err := add(runGitCmdRaw(dir, "diff", "--name-only", "-z", "--diff-filter=ACMRTD", ref)); err != nil {
		return nil, fmt.Errorf("--since: git diff failed: %w", err)
	}
	if err := add(runGitCmdRaw(dir, "ls-files", "--others", "--exclude-standard", "-z")); err != nil {
		return nil, fmt.Errorf("--since: git ls-files failed: %w", err)
	}
	return changed, nil
}

func runGitCmd(dir string, args ...string) (string, error) {
	out, err := runGitCmdRaw(dir, args...)
	return string(out), err
}

func runGitCmdRaw(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// AffectedCommands returns the commands affected by changed (absolute paths,
// typically from GitChangedFiles): a changed file matches a command's file
// deps, onchange globs, produces, or declaring Constfile, or any prerequisite
// is affected. baseDir is the Constfile's directory (absolute).
func AffectedCommands(data *ParsedData, changed map[string]bool, baseDir string) map[string]bool {
	affected := map[string]bool{}
	visiting := map[string]bool{}

	// Deleted files no longer expand from disk, so match globs against the
	// changed paths directly too.
	changedRels := make([]string, 0, len(changed))
	for p := range changed {
		if rel, err := filepath.Rel(baseDir, p); err == nil && !strings.HasPrefix(rel, "..") {
			changedRels = append(changedRels, filepath.ToSlash(rel))
		}
	}

	direct := func(cmd *Command) bool {
		if cmd.SourceFile != "" {
			if matchesChangedPath(absPath(baseDir, cmd.SourceFile), changed) {
				return true
			}
		}
		patterns := append(append([]string{}, cmd.FileDeps...), cmd.OnChange...)
		patterns = append(patterns, cmd.Produces...)
		for _, pattern := range patterns {
			for _, f := range expandFileDeps([]string{pattern}, baseDir) {
				if matchesChangedPath(absPath(baseDir, f), changed) {
					return true
				}
			}
			for _, rel := range changedRels {
				if globMatches(pattern, rel) {
					return true
				}
			}
		}
		return false
	}

	var visit func(name string) bool
	visit = func(name string) bool {
		if affected[name] {
			return true
		}
		if visiting[name] {
			return false // cycle guard; the parser rejects these anyway
		}
		visiting[name] = true
		defer func() { visiting[name] = false }()

		cmd, err := data.GetCommand(name)
		if err != nil || cmd == nil {
			return false
		}
		if direct(cmd) {
			affected[name] = true
			return true
		}
		for _, pre := range cmd.Prereqs {
			if visit(strings.TrimSpace(pre)) {
				affected[name] = true
				return true
			}
		}
		return false
	}
	for _, cmd := range data.Commands {
		visit(cmd.Name)
	}
	return affected
}

func absPath(baseDir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}

	return filepath.Join(baseDir, p)
}

func globMatches(pattern, rel string) bool {
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	pattern = strings.ReplaceAll(pattern, "**", "*")
	ok, err := filepath.Match(pattern, rel)

	return err == nil && ok
}

func matchesChangedPath(path string, changed map[string]bool) bool {
	for {
		if changed[path] {
			return true
		}

		switch path {
		case "", ".", "..", string(filepath.Separator):
			return false
		}

		parent := filepath.Dir(path)
		if parent == path {
			return false
		}

		path = parent
	}
}

func FilesNotWatched(data *ParsedData, baseDir string) ([]string, error) {
	watched := map[string]bool{}
	for _, cmd := range data.Commands {
		patterns := append(append([]string{}, cmd.FileDeps...), cmd.OnChange...)
		for _, f := range expandFileDeps(patterns, baseDir) {
			watched[absPath(baseDir, f)] = true
		}
	}

	for _, cmd := range data.Commands {
		for _, f := range expandFileDeps(cmd.Produces, baseDir) {
			watched[absPath(baseDir, f)] = true
		}
	}

	skip := map[string]bool{".git": true, CacheDirName(): true, "node_modules": true}
	var unwatched []string
	err := filepath.WalkDir(baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if path != baseDir && skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		if watched[path] {
			return nil
		}

		unwatched = append(unwatched, path)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return unwatched, nil
}
