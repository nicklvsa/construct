package pkg

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (e *Executor) runBuiltin(ctx *execContext, stmt BodyStatement) error {
	ignoreErr := stmt.Tolerant
	parts := splitArgs(stmt.BuiltinArgs)
	release := e.acquire()
	err := e.builtinExec(ctx, stmt.Shell, parts)
	release()
	code := 0
	if err != nil {
		code = 1
	}
	e.setLastResult(ctx, code, "")
	if err != nil && !ignoreErr {
		return &CommandError{Cmd: stmt.Shell + " " + strings.Join(parts, " "), ExitCode: 1, Stderr: err.Error(), File: ctx.srcFile, Line: stmt.SourceLine}
	}
	return nil
}

func (e *Executor) builtinDir(ctx *execContext) string {
	if ctx.workDir != "" {
		if d := e.resolveWorkDir(e.resolveBodyValue(ctx, ctx.workDir, ctx.target.Name)); d != "" {
			return d
		}
	}
	if e.baseDir != "" {
		return e.baseDir
	}
	return "."
}

func (e *Executor) builtinPath(ctx *execContext, p string) string {
	p = e.resolveBodyValue(ctx, p, ctx.target.Name)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.builtinDir(ctx), p)
}

func (e *Executor) builtinExec(ctx *execContext, name string, args []string) error {
	switch name {
	case "cp":
		return e.builtinCp(ctx, args)
	case "rm":
		return e.builtinRm(ctx, args)
	case "mkdir":
		return e.builtinMkdir(ctx, args)
	case "touch":
		return e.builtinTouch(ctx, args)
	case "download":
		return e.builtinDownload(ctx, args)
	case "extract":
		return e.builtinExtract(ctx, args)
	}
	return fmt.Errorf("unknown builtin %q", name)
}

func (e *Executor) builtinCp(ctx *execContext, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("cp requires a source and a destination")
	}
	src := e.builtinPath(ctx, args[0])
	dst := e.builtinPath(ctx, args[1])
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("cp: %w", err)
	}
	if info.IsDir() {
		if st, err := os.Stat(dst); err == nil && st.IsDir() {
			dst = filepath.Join(dst, filepath.Base(src))
		}
		return copyDir(src, dst)
	}
	if st, err := os.Stat(dst); err == nil && st.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}
	return copyFile(src, dst)
}

func (e *Executor) builtinRm(ctx *execContext, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("rm requires a path")
	}
	base, _ := filepath.Abs(e.baseDir)
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		p := e.builtinPath(ctx, a)
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if base != "" && (abs == base || strings.HasPrefix(base, abs+string(os.PathSeparator))) {
			return fmt.Errorf("refusing to remove %q (the base directory or an ancestor)", a)
		}
		if err := os.RemoveAll(abs); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) builtinMkdir(ctx *execContext, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("mkdir requires a path")
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if err := os.MkdirAll(e.builtinPath(ctx, a), 0755); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) builtinTouch(ctx *execContext, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("touch requires a path")
	}
	for _, a := range args {
		p := e.builtinPath(ctx, a)
		if _, err := os.Stat(p); err == nil {
			now := time.Now()
			if err := os.Chtimes(p, now, now); err != nil {
				return err
			}
			continue
		}
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		f.Close()
	}
	return nil
}

func (e *Executor) builtinDownload(ctx *execContext, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("download requires a URL and a destination")
	}
	req, err := http.NewRequestWithContext(e.effectiveRunCtx(ctx), http.MethodGet, args[0], nil)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", args[0], err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("download %s: %s", args[0], resp.Status)
	}
	dst := e.builtinPath(ctx, args[1])
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if e.quiet || !termIsTTY(os.Stdout) || resp.ContentLength <= 0 {
		_, err = io.Copy(out, resp.Body)
		return err
	}
	_, err = copyProgress(out, resp.Body, resp.ContentLength)
	return err
}

func copyProgress(dst io.Writer, src io.Reader, total int64) (int64, error) {
	written := int64(0)
	lastPct := -1
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written, werr
			}
			written += int64(n)
			pct := int(written * 100 / total)
			if pct != lastPct {
				fmt.Fprintf(os.Stdout, "\r[download] %3d%%", pct)
				lastPct = pct
			}
		}
		if err == io.EOF {
			fmt.Fprintln(os.Stdout, "\r[download] 100%")
			return written, nil
		}
		if err != nil {
			return written, err
		}
	}
}

func (e *Executor) builtinExtract(ctx *execContext, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("extract requires an archive and a destination directory")
	}
	archive := e.builtinPath(ctx, args[0])
	dir := e.builtinPath(ctx, args[1])
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	lower := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archive, dir)
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gr.Close()
		return extractTar(gr, dir)
	case strings.HasSuffix(lower, ".tar.bz2"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		return extractTar(bzip2.NewReader(f), dir)
	case strings.HasSuffix(lower, ".tar"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		return extractTar(f, dir)
	}
	return fmt.Errorf("extract: unsupported archive %q (supported: .zip, .tar, .tar.gz, .tgz, .tar.bz2)", archive)
}

func safeExtractPath(dir, name string) (string, error) {
	target := filepath.Join(dir, filepath.FromSlash(name))
	cleanDir := filepath.Clean(dir)
	if target != cleanDir && !strings.HasPrefix(target, cleanDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry escapes the destination directory: %s", name)
	}
	return target, nil
}

func extractZip(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		target, err := safeExtractPath(dir, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		_, cErr := io.Copy(out, rc)
		rc.Close()
		cErr2 := out.Close()
		if cErr != nil {
			return cErr
		}
		if cErr2 != nil {
			return cErr2
		}
	}
	return nil
}

func extractTar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeExtractPath(dir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		return copyFile(p, target)
	})
}

// withLock holds an exclusive advisory lock for the duration of fn. A
// positive maxWait bounds how long acquiring the lock can take.
func (e *Executor) withLock(ctx *execContext, name string, maxWait time.Duration, fn func() error) error {
	dir := filepath.Join(e.cacheDirFor(), "locks")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	runCtx := e.effectiveRunCtx(ctx)
	var deadline time.Time
	if maxWait > 0 {
		deadline = time.Now().Add(maxWait)
	}
	waited := false
	for {
		if tryFlock(f) {
			break
		}
		if !waited {
			waited = true
			if maxWait > 0 {
				fmt.Fprintf(os.Stderr, "(%s waiting for lock %q, bounded by %s...)\n", ctx.target.Name, name, maxWait)
			} else {
				fmt.Fprintf(os.Stderr, "(%s waiting for lock %q...)\n", ctx.target.Name, name)
			}
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for lock %q", maxWait, name)
		}
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer unlockFlock(f)
	return fn()
}
