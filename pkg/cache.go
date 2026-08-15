package pkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	if e.stateLoaded {
		return
	}
	e.stateLoaded = true
	e.state = make(map[string]string)
	data, err := os.ReadFile(e.statePath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &e.state)
	if e.state == nil {
		e.state = make(map[string]string)
	}
}

func (e *Executor) stateLookup(name string) (string, bool) {
	e.loadState()
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
	e.state[name] = value
	if err := os.MkdirAll(e.cacheDirFor(), 0755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(e.state, "", "  ")
	_ = os.WriteFile(e.statePath(), data, 0644)
}

type fileCache map[string]map[string]string

func loadFileCache(dir string) fileCache {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fileCache{}
	}
	var fc fileCache
	if err := json.Unmarshal(data, &fc); err != nil {
		return fileCache{}
	}
	return fc
}

func (fc fileCache) save(dir string) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	data, _ := json.MarshalIndent(fc, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644)
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

func (e *Executor) cacheManifest() fileCache {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loadedCacheLocked()
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

func (e *Executor) shouldSkip(cmd *Command, resolve func(string, string) string, workDir string) (bool, string) {
	files := expandFileDeps(cmd.FileDeps, e.workDirFor(cmd, resolve, workDir))
	if len(files) == 0 {
		return false, ""
	}

	fc := e.cacheManifest()
	key := e.cacheKey(cmd)
	cached, exists := fc[key]
	if !exists {
		return false, "no cached result"
	}

	hashes := parallelHash(files)
	for i, f := range files {
		if cached[f] != hashes[i] {
			e.debugf("%s: file changed: %s\n", cmd.Name, f)
			return false, fmt.Sprintf("%s changed", f)
		}
	}
	return true, fmt.Sprintf("%d dep(s) unchanged", len(files))
}

func parallelHash(files []string) []string {
	if len(files) < 2 {
		out := make([]string, len(files))
		for i, f := range files {
			out[i] = hashFile(f)
		}
		return out
	}
	out := make([]string, len(files))
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			out[i] = hashFile(f)
		}(i, f)
	}
	wg.Wait()
	return out
}

func (e *Executor) shouldSkipProduced(cmd *Command, resolve func(string, string) string, workDir string) (bool, string) {
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
	for _, d := range expandFileDeps(cmd.FileDeps, e.workDirFor(cmd, resolve, workDir)) {
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

	e.mu.Lock()
	defer e.mu.Unlock()

	fc := e.loadedCacheLocked()
	key := e.cacheKey(cmd)
	if fc[key] == nil {
		fc[key] = make(map[string]string)
	}
	hashes := parallelHash(files)
	for i, f := range files {
		fc[key][f] = hashes[i]
	}
	fc.save(e.cacheDirFor())
}
