package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicklvsa/construct/pkg"
)

const uiTestConstfile = `var greeting = hello

hello {
    $ echo hi
}

run < hello {
    $ echo run
}
`

func newUITestServer(t *testing.T) (*uiServer, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Constfile")
	if err := os.WriteFile(path, []byte(uiTestConstfile), 0644); err != nil {
		t.Fatal(err)
	}
	doc, err := pkg.NewUIDoc(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &uiServer{doc: doc, token: "testtoken"}
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return s, ts
}

func uiDo(t *testing.T, ts *httptest.Server, method, path, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Construct-Token", token)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func uiStateOf(t *testing.T, body map[string]any) *pkg.UIState {
	t.Helper()
	raw, err := json.Marshal(body["state"])
	if err != nil {
		t.Fatal(err)
	}
	var st pkg.UIState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	return &st
}

func TestUIServerTokenEnforced(t *testing.T) {
	_, ts := newUITestServer(t)
	resp, _ := uiDo(t, ts, "GET", "/api/state", "", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("no token = %d, want 403", resp.StatusCode)
	}
	// Tokens are header-only: query strings leak into history and logs.
	resp, _ = uiDo(t, ts, "GET", "/api/state?t=testtoken", "", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("query token = %d, want 403", resp.StatusCode)
	}
}

func TestUIServerServesIndex(t *testing.T) {
	_, ts := newUITestServer(t)
	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content type = %q", ct)
	}
}

func TestUIServerStateAndOps(t *testing.T) {
	s, ts := newUITestServer(t)
	resp, body := uiDo(t, ts, "GET", "/api/state", s.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state = %d", resp.StatusCode)
	}
	st := uiStateOf(t, body)
	if len(st.Files) != 1 || len(st.Files[0].Commands) != 2 {
		t.Fatalf("state = %+v", st.Files)
	}
	if st.Files[0].Commands[1].Header.Prereqs[0] != "hello" {
		t.Errorf("prereqs = %v", st.Files[0].Commands[1].Header.Prereqs)
	}

	newBody := "$ echo changed"
	resp, body = uiDo(t, ts, "POST", "/api/ops", s.token, uiOpsRequest{
		Ops: []pkg.UIEditOp{{
			File: st.Files[0].Path, Kind: "setBody", Name: "hello", Body: &newBody,
		}},
	})
	if resp.StatusCode != http.StatusOK || body["ok"] != true {
		t.Fatalf("ops = %d %v", resp.StatusCode, body["error"])
	}
	st = uiStateOf(t, body)
	if st.Files[0].Commands[0].Body != "$ echo changed" {
		t.Errorf("body after op = %q", st.Files[0].Commands[0].Body)
	}
	if !st.Dirty || !st.CanUndo {
		t.Error("dirty/undo flags not set after op")
	}

	// rejected op rolls back and reports the error
	resp, body = uiDo(t, ts, "POST", "/api/ops", s.token, uiOpsRequest{
		Ops: []pkg.UIEditOp{{
			File: st.Files[0].Path, Kind: "deleteCommand", Name: "hello",
		}},
	})
	if resp.StatusCode != http.StatusOK || body["ok"] != false {
		t.Fatalf("dangling delete should be rejected: %d %v", resp.StatusCode, body)
	}
	st = uiStateOf(t, body)
	if len(st.Files[0].Commands) != 2 {
		t.Errorf("rejected op changed state: %d commands", len(st.Files[0].Commands))
	}

	// undo
	resp, body = uiDo(t, ts, "POST", "/api/undo", s.token, nil)
	if resp.StatusCode != http.StatusOK || body["ok"] != true {
		t.Fatalf("undo = %d %v", resp.StatusCode, body)
	}
	st = uiStateOf(t, body)
	if st.Files[0].Commands[0].Body != "    $ echo hi" {
		t.Errorf("body after undo = %q", st.Files[0].Commands[0].Body)
	}
}

func TestUIServerSaveAndConflict(t *testing.T) {
	s, ts := newUITestServer(t)
	mainPath := s.doc.Main

	newBody := "$ echo changed"
	_, body := uiDo(t, ts, "POST", "/api/ops", s.token, uiOpsRequest{
		Ops: []pkg.UIEditOp{{
			File: mainPath, Kind: "setBody", Name: "hello", Body: &newBody,
		}},
	})
	if body["ok"] != true {
		t.Fatalf("op failed: %v", body["error"])
	}

	resp, body := uiDo(t, ts, "POST", "/api/save", s.token, nil)
	if resp.StatusCode != http.StatusOK || body["ok"] != true {
		t.Fatalf("save = %d %v", resp.StatusCode, body)
	}
	disk, _ := os.ReadFile(mainPath)
	if !bytes.Contains(disk, []byte("$ echo changed")) {
		t.Error("saved file missing edit")
	}

	// edit again, then touch the file externally
	_, body = uiDo(t, ts, "POST", "/api/ops", s.token, uiOpsRequest{
		Ops: []pkg.UIEditOp{{
			File: mainPath, Kind: "setBody", Name: "hello", Body: &newBody,
		}},
	})
	_ = body
	if err := os.WriteFile(mainPath, []byte(uiTestConstfile+"\n# external\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resp, body = uiDo(t, ts, "POST", "/api/save", s.token, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict = %d %v", resp.StatusCode, body)
	}
}

func TestUIServerDryRunUnavailable(t *testing.T) {
	s, ts := newUITestServer(t)
	resp, _ := uiDo(t, ts, "POST", "/api/dryrun", s.token, uiDryRunRequest{Targets: []string{"run"}})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("dryrun without binary = %d, want 500", resp.StatusCode)
	}
}

func TestUIRunUIErrors(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := runUI([]string{"--bogus"}, &options{}); err == nil {
		t.Error("unknown flag accepted")
	}
	if err := runUI(nil, &options{}); err == nil {
		t.Error("missing Constfile accepted")
	}
	cf := filepath.Join(dir, "Constfile")
	if err := os.WriteFile(cf, []byte("a {\n    $ x\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runUI([]string{cf, "extra"}, &options{}); err == nil {
		t.Error("extra args accepted")
	}
}
