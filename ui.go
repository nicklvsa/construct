package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nicklvsa/construct/pkg"
)

//go:embed uiassets
var uiAssets embed.FS

type uiServer struct {
	mu    sync.Mutex
	doc   *pkg.UIDoc
	token string
	bin   string
}

func runUI(args []string, o *options) error {
	if err := rejectSubcommandFlags(args, "ui"); err != nil {
		return err
	}
	fileName, rest := splitConstfileArgs(args)
	if len(rest) > 0 {
		return exitAt(2, "usage: construct ui [Constfile]")
	}
	if !fileExists(fileName) {
		return fmt.Errorf("no Constfile found (looked for %s)", fileName)
	}

	doc, err := pkg.NewUIDoc(fileName)
	if err != nil {
		return err
	}

	bin, _ := os.Executable()
	srv := &uiServer{doc: doc, token: uiNewToken(), bin: bin}

	addr := "127.0.0.1:0"
	if o.uiPort > 0 {
		addr = fmt.Sprintf("127.0.0.1:%d", o.uiPort)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: srv.routes()}
	go func() {
		_ = httpSrv.Serve(ln)
	}()

	url := fmt.Sprintf("http://%s/?t=%s", ln.Addr().String(), srv.token)
	fmt.Printf("construct ui — editing %s\n", fileName)
	fmt.Println(url)
	fmt.Println("Ctrl-C to stop")
	if !o.uiNoOpen {
		uiOpenBrowser(url)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	<-sigCh
	_ = httpSrv.Close()
	return nil
}

func uiNewToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (s *uiServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serveAsset)
	mux.HandleFunc("/api/state", s.auth(s.handleState))
	mux.HandleFunc("/api/ops", s.auth(s.handleOps))
	mux.HandleFunc("/api/undo", s.auth(s.handleUndo))
	mux.HandleFunc("/api/redo", s.auth(s.handleRedo))
	mux.HandleFunc("/api/save", s.auth(s.handleSave))
	mux.HandleFunc("/api/reload", s.auth(s.handleReload))
	mux.HandleFunc("/api/dryrun", s.auth(s.handleDryRun))
	return mux
}

func (s *uiServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Construct-Token")
		if tok == "" {
			tok = r.URL.Query().Get("t")
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *uiServer) serveAsset(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	data, err := uiAssets.ReadFile("uiassets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	w.Write(data)
}

func (s *uiServer) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	st := s.doc.State()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": st})
}

type uiOpsRequest struct {
	Ops []pkg.UIEditOp `json:"ops"`
}

func (s *uiServer) handleOps(w http.ResponseWriter, r *http.Request) {
	var req uiOpsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	applyErr := s.doc.Apply(req.Ops)
	st := s.doc.State()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    applyErr == nil,
		"error": errString(applyErr),
		"state": st,
	})
}

func (s *uiServer) handleUndo(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	did := s.doc.Undo()
	st := s.doc.State()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": did, "state": st})
}

func (s *uiServer) handleRedo(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	did := s.doc.Redo()
	st := s.doc.State()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": did, "state": st})
}

func (s *uiServer) handleSave(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	saved, conflicts, err := s.doc.Save()
	st := s.doc.State()
	s.mu.Unlock()
	if err != nil {
		if len(conflicts) > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":        false,
				"conflicts": conflicts,
				"state":     st,
			})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": saved, "state": st})
}

func (s *uiServer) handleReload(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	doc, err := pkg.NewUIDoc(s.doc.Main)
	if err == nil {
		s.doc = doc
	}
	st := s.doc.State()
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": st})
}

type uiDryRunRequest struct {
	Targets []string `json:"targets"`
}

func (s *uiServer) handleDryRun(w http.ResponseWriter, r *http.Request) {
	var req uiDryRunRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	dir := filepath.Dir(s.doc.Main)
	file := filepath.Base(s.doc.Main)
	bin := s.bin
	s.mu.Unlock()
	if bin == "" {
		http.Error(w, "dry-run unavailable (no construct binary)", http.StatusInternalServerError)
		return
	}
	args := append([]string{"--dry-run", file}, req.Targets...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	writeJSON(w, http.StatusOK, map[string]any{
		"output": string(out),
		"error":  errString(err),
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
