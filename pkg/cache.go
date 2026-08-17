package pkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const cacheDir = ".construct-cache"

func CacheDirName() string {
	return cacheDir
}

func (e *Executor) cacheDirFor() string {
	if e.baseDir != "" {
		return filepath.Join(e.baseDir, cacheDir)
	}
	return cacheDir
}

func (e *Executor) statePath() string {
	return filepath.Join(e.cacheDirFor(), "state.json")
}

func LoadRunHistory(dir string) map[string][]RunRecord {
	data, err := os.ReadFile(filepath.Join(dir, "run-state.json"))
	if err != nil {
		return nil
	}
	var hist map[string][]RunRecord
	if err := json.Unmarshal(data, &hist); err != nil {
		return nil
	}
	return hist
}

func LastRecord(hist map[string][]RunRecord) map[string]RunRecord {
	out := make(map[string]RunRecord, len(hist))
	for name, recs := range hist {
		if len(recs) > 0 {
			out[name] = recs[len(recs)-1]
		}
	}
	return out
}

func SaveRunHistory(dir string, hist map[string][]RunRecord) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(hist, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "run-state.json"), data, 0644)
}

func (e *Executor) recordRun(name string, rec RunRecord) {
	if !e.recordRuns {
		return
	}
	e.mu.Lock()
	if e.runRecords == nil {
		e.runRecords = make(map[string]RunRecord)
	}
	e.runRecords[name] = rec
	e.mu.Unlock()
}

func (e *Executor) saveRunRecords() {
	if !e.recordRuns {
		return
	}
	e.mu.Lock()
	rec := e.runRecords
	e.mu.Unlock()
	if rec == nil {
		return
	}
	dir := e.cacheDirFor()
	hist := LoadRunHistory(dir)
	if hist == nil {
		hist = make(map[string][]RunRecord)
	}
	for name, r := range rec {
		recs := hist[name]
		recs = append(recs, r)
		if len(recs) > 50 {
			recs = recs[len(recs)-50:]
		}
		hist[name] = recs
	}
	SaveRunHistory(dir, hist)
}

func (e *Executor) loadState() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stateLoaded {
		return
	}
	e.stateLoaded = true
	e.state = make(map[string]string)
	data, err := os.ReadFile(e.statePath())
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, &e.state); err != nil {
		fmt.Fprintf(os.Stderr, "warning: ignoring corrupt state file %s: %v\n", e.statePath(), err)
	}
	if e.state == nil {
		e.state = make(map[string]string)
	}
}

func (e *Executor) stateLookup(name string) (string, bool) {
	e.loadState()
	e.mu.Lock()
	defer e.mu.Unlock()
	v, ok := e.state[name]
	return v, ok
}

func (e *Executor) resolveGlobalValue(s string) string {
	s = resolveStateRefsWith(s, e.stateLookup)
	s = resolveVarRefs(s, func(name string) (string, bool) {
		v, ok := LookupVariableIndexed(e.StructuredParse, name, "global")
		if !ok {
			return "", false
		}
		return v.String(), true
	})
	return resolveEnvRefsWith(s, func(name string) string { return os.Getenv(name) })
}

func (e *Executor) setRuntimeState(name, value string) {
	e.loadState()
	e.mu.Lock()
	e.state[name] = value
	e.stateDirty = true
	e.mu.Unlock()
}

func (e *Executor) flushState() {
	e.mu.Lock()
	dirty := e.stateDirty
	state := e.state
	e.stateDirty = false
	e.mu.Unlock()
	if !dirty || state == nil {
		return
	}
	if err := saveJSONFile(e.statePath(), state); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save state to %s: %v\n", e.statePath(), err)
		e.mu.Lock()
		e.stateDirty = true // retry on the next flush
		e.mu.Unlock()
	}
}

// saveJSONFile writes v as indented JSON, creating the parent directory.
func saveJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

type fileCache map[string]map[string]string

func loadFileCache(dir string) fileCache {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fileCache{}
	}
	var fc fileCache
	if err := json.Unmarshal(data, &fc); err != nil || fc == nil {
		return fileCache{}
	}
	return fc
}

func (fc fileCache) save(dir string) error {
	return saveJSONFile(filepath.Join(dir, "manifest.json"), fc)
}

func (e *Executor) hashFiles(files []string) []string {
	e.mu.Lock()
	if e.hashMemo == nil {
		e.hashMemo = make(map[string]string, len(files))
	}
	out := make([]string, len(files))
	var missing []int
	for i, f := range files {
		if h, ok := e.hashMemo[f]; ok {
			out[i] = h
		} else {
			missing = append(missing, i)
		}
	}
	e.mu.Unlock()

	if len(missing) > 0 {
		paths := make([]string, len(missing))
		for k, i := range missing {
			paths[k] = files[i]
		}
		hashes := parallelHash(paths)
		e.mu.Lock()
		for k, i := range missing {
			// Don't memoize failed hashes (""): a transiently unreadable file
			// should be retried on the next check, not cached as empty forever.
			if hashes[k] != "" {
				e.hashMemo[paths[k]] = hashes[k]
			}
			out[i] = hashes[k]
		}
		e.mu.Unlock()
	}
	return out
}

func (e *Executor) invalidateHashes(paths []string) {
	if len(paths) == 0 {
		return
	}
	e.mu.Lock()
	for _, p := range paths {
		delete(e.hashMemo, p)
	}
	e.mu.Unlock()
}

func hashFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func expandFileDeps(patterns []string, workDir string) []string {
	wd := workDir
	if wd == "" {
		wd = "."
	}
	var files []string
	for _, pattern := range patterns {
		full := filepath.Join(wd, pattern)
		matches, err := filepath.Glob(full)
		if err != nil || len(matches) == 0 {
			files = append(files, full)
			continue
		}
		files = append(files, matches...)
	}
	return files
}

func (e *Executor) loadedCacheLocked() fileCache {
	if !e.cacheLoaded {
		e.cache = loadFileCache(e.cacheDirFor())
		e.cacheLoaded = true
	}
	return e.cache
}

func (e *Executor) cacheKey(cmd *Command) string {
	name := cmd.Name
	if cmd.SourceFile != "" {
		name += "@" + filepath.Base(cmd.SourceFile)
	}
	parts := []string{name}
	for _, arg := range cmd.Arguments {
		v := arg.Default
		if e.flagSet != nil {
			v, _ = e.flagSet.GetString(cmd.Name + ":" + arg.Name)
		}
		parts = append(parts, arg.Name+"="+v)
	}
	snapshot := e.StructuredParse.GlobalVariableSnapshot()
	if cmd.cacheGlobalsExact {
		for _, g := range cmd.cacheGlobals {
			parts = append(parts, "var:"+g+"="+snapshot[g])
		}
	} else {
		for name, val := range snapshot {
			parts = append(parts, "var:"+name+"="+val)
		}
	}
	sort.Strings(parts[1:])
	return strings.Join(parts, "|")
}

func (e *Executor) workDirFor(cmd *Command, resolve func(string, string) string, workDir string) string {
	wd := e.resolveWorkDir(resolve(workDir, cmd.Name))
	if wd == "" {
		wd = e.baseDir
	}
	return wd
}

func (e *Executor) shouldSkip(cmd *Command, files []string) (bool, string) {
	if len(files) == 0 {
		return false, ""
	}

	// Snapshot the cached hashes under the lock: updateCache may write the
	// shared maps concurrently from parallel prereqs (hashFiles takes e.mu
	// itself, so the lock must not be held across the call).
	e.mu.Lock()
	key := e.cacheKey(cmd)
	cached, exists := e.loadedCacheLocked()[key]
	var cachedHashes map[string]string
	if exists {
		cachedHashes = make(map[string]string, len(cached))
		maps.Copy(cachedHashes, cached)
	}
	e.mu.Unlock()
	if !exists {
		return false, "no cached result"
	}

	hashes := e.hashFiles(files)
	for i, f := range files {
		if cachedHashes[f] != hashes[i] {
			e.debugf("%s: file changed: %s\n", cmd.Name, f)
			return false, fmt.Sprintf("%s changed", f)
		}
	}
	return true, fmt.Sprintf("%d dep(s) unchanged", len(files))
}

// parallelHash hashes files concurrently with a bounded worker pool so a wide
// glob (e.g. deps *.go) can't open thousands of file descriptors at once.
func parallelHash(files []string) []string {
	out := make([]string, len(files))
	workers := min(len(files), runtime.NumCPU())
	if workers < 2 {
		for i, f := range files {
			out[i] = hashFile(f)
		}
		return out
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(files) {
					return
				}
				out[i] = hashFile(files[i])
			}
		}()
	}
	wg.Wait()
	return out
}

func (e *Executor) shouldSkipProduced(cmd *Command, resolve func(string, string) string, workDir string, depFiles []string) (bool, string) {
	artifacts := expandFileDeps(cmd.Produces, e.workDirFor(cmd, resolve, workDir))
	if len(artifacts) == 0 {
		return false, ""
	}
	var newest time.Time
	for _, a := range artifacts {
		info, err := os.Stat(a)
		if err != nil || info.IsDir() {
			return false, fmt.Sprintf("missing artifact %s", a) // must build
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	for _, d := range depFiles {
		info, err := os.Stat(d)
		if err != nil {
			return false, fmt.Sprintf("missing dep %s", d)
		}
		if info.ModTime().After(newest) {
			return false, fmt.Sprintf("%s is newer than the artifacts", d)
		}
	}
	return true, "artifacts up to date"
}

func (e *Executor) updateCache(cmd *Command, resolve func(string, string) string, workDir string) {
	files := expandFileDeps(cmd.FileDeps, e.workDirFor(cmd, resolve, workDir))
	hashes := e.hashFiles(files)

	e.mu.Lock()
	defer e.mu.Unlock()

	fc := e.loadedCacheLocked()
	key := e.cacheKey(cmd)
	if fc[key] == nil {
		fc[key] = make(map[string]string)
	}
	for i, f := range files {
		fc[key][f] = hashes[i]
	}

	e.cacheDirty = true
}

func (e *Executor) flushCache() {
	e.mu.Lock()
	dirty := e.cacheDirty
	fc := e.cache
	e.cacheDirty = false
	e.mu.Unlock()
	if dirty && fc != nil {
		if err := fc.save(e.cacheDirFor()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save the cache manifest: %v\n", err)
			e.mu.Lock()
			e.cacheDirty = true // retry on the next flush
			e.mu.Unlock()
		}
	}
}
