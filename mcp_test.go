package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fakeRunner(output string, code int) func(context.Context, string, []string, time.Duration) (string, int, error) {
	return func(_ context.Context, dir string, argv []string, _ time.Duration) (string, int, error) {
		return output + " | " + strings.Join(argv, " ") + " @ " + dir, code, nil
	}
}

func callLine(t *testing.T, s *mcpServer, line string) rpcResponse {
	t.Helper()
	resp := s.handleLine([]byte(line))
	if resp == nil {
		t.Fatalf("no response for %s", line)
	}
	return *resp
}

func TestMCPInitialize(t *testing.T) {
	s := &mcpServer{}
	resp := callLine(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if resp.Error != nil {
		t.Fatalf("initialize errored: %v", resp.Error.Message)
	}
	result := resp.Result.(map[string]any)
	if result["protocolVersion"] != "2025-03-26" {
		t.Errorf("protocol version not echoed: %v", result["protocolVersion"])
	}
	info := result["serverInfo"].(map[string]any)
	if info["name"] != "construct" {
		t.Errorf("serverInfo.name = %v", info["name"])
	}
}

func TestMCPInitializeUnknownVersion(t *testing.T) {
	s := &mcpServer{}
	resp := callLine(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if resp.Result.(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Error("unknown version should fall back to the latest")
	}
}

func TestMCPNotificationsIgnored(t *testing.T) {
	s := &mcpServer{}
	if resp := s.handleLine([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); resp != nil {
		t.Error("notification produced a response")
	}
}

func TestMCPToolsList(t *testing.T) {
	s := &mcpServer{}
	resp := callLine(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := resp.Result.(map[string]any)["tools"].([]map[string]any)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool["name"].(string)] = true
		if tool["inputSchema"] == nil {
			t.Errorf("tool %s missing inputSchema", tool["name"])
		}
	}
	for _, want := range []string{"list_targets", "run_targets", "graph", "lint", "run_history"} {
		if !names[want] {
			t.Errorf("tool %s missing", want)
		}
	}
}

func TestMCPCallToolRun(t *testing.T) {
	s := &mcpServer{runCmd: fakeRunner("ok", 0)}
	resp := callLine(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run_targets","arguments":{"targets":["build"],"cwd":"/repo"}}}`)
	result := resp.Result.(map[string]any)
	if result["isError"] == true {
		t.Fatalf("run_targets failed: %v", result)
	}
	text := result["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(text, "/repo") || !strings.Contains(text, "build") {
		t.Errorf("unexpected tool output: %q", text)
	}
}

func TestMCPCallToolFailureKeepsOutput(t *testing.T) {
	s := &mcpServer{runCmd: fakeRunner("Error: cannot find command", 1)}
	resp := callLine(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"lint","arguments":{}}}`)
	result := resp.Result.(map[string]any)
	if result["isError"] != true {
		t.Error("expected isError on non-zero exit")
	}
	text := result["content"].([]map[string]string)[0]["text"]
	if !strings.Contains(text, "Error: cannot find command") || !strings.Contains(text, "code 1") {
		t.Errorf("failure output lost: %q", text)
	}
}

func TestMCPCallUnknownTool(t *testing.T) {
	s := &mcpServer{}
	resp := callLine(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope"}}`)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("expected -32602 for unknown tool, got %+v", resp.Error)
	}
}

func TestMCPUnknownMethod(t *testing.T) {
	s := &mcpServer{}
	resp := callLine(t, s, `{"jsonrpc":"2.0","id":6,"method":"resources/list"}`)
	if resp.Error == nil {
		t.Error("expected error for unknown method")
	}
}

func TestMCPResponseJSONRoundTrip(t *testing.T) {
	s := &mcpServer{}
	resp := callLine(t, s, `{"jsonrpc":"2.0","id":"abc","method":"ping"}`)
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"id":"abc"`) || !strings.Contains(string(b), `"result"`) {
		t.Errorf("bad response encoding: %s", b)
	}
}
