package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const mcpOutputCap = 100_000

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpServer struct {
	file   string
	runCmd func(ctx context.Context, dir string, argv []string, timeout time.Duration) (output string, exitCode int, err error)
}

func runMCP(args []string) error {
	fileName, _ := splitConstfileArgs(args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	s := &mcpServer{
		file: fileName,
		runCmd: func(ctx context.Context, dir string, argv []string, timeout time.Duration) (string, int, error) {
			return mcpExec(ctx, exe, dir, argv, timeout)
		},
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	fmt.Fprintf(os.Stderr, "construct mcp: serving %d tool(s) on stdio\n", len(mcpTools))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		resp := s.handleLine(line)
		if resp == nil {
			continue
		}
		b, err := json.Marshal(resp)
		if err != nil {
			continue
		}
		out.Write(append(b, '\n'))
		out.Flush()
	}
	return scanner.Err()
}

func (s *mcpServer) handleLine(line []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}}
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return nil // notification
	}
	switch req.Method {
	case "initialize":
		return s.initialize(req)
	case "ping":
		return s.ok(req, map[string]any{})
	case "tools/list":
		tools := make([]map[string]any, 0, len(mcpTools))
		for _, t := range mcpTools {
			tools = append(tools, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.Schema,
			})
		}
		return s.ok(req, map[string]any{"tools": tools})
	case "tools/call":
		return s.callTool(req)
	default:
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}}
	}
}

var mcpProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

func (s *mcpServer) initialize(req rpcRequest) *rpcResponse {
	requested := "2025-06-18"
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(req.Params, &params) == nil && mcpProtocolVersions[params.ProtocolVersion] {
		requested = params.ProtocolVersion
	}
	return s.ok(req, map[string]any{
		"protocolVersion": requested,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": "construct", "version": version},
	})
}

func (s *mcpServer) ok(req rpcRequest, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func (s *mcpServer) callTool(req rpcRequest) *rpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid tools/call params"}}
	}
	for _, t := range mcpTools {
		if t.Name != params.Name {
			continue
		}
		var args mcpToolArgs
		if len(params.Arguments) > 0 {
			if err := json.Unmarshal(params.Arguments, &args); err != nil {
				return s.ok(req, map[string]any{
					"content": []map[string]string{{"type": "text", "text": "invalid arguments: " + err.Error()}},
					"isError": true,
				})
			}
		}
		text, err := t.Call(s, args)
		if err != nil && text == "" {
			text = err.Error()
		}
		return s.ok(req, map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
			"isError": err != nil,
		})
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}}
}

type mcpToolArgs struct {
	File           string   `json:"file"`
	Cwd            string   `json:"cwd"`
	Targets        []string `json:"targets"`
	Jobs           int      `json:"jobs"`
	KeepGoing      bool     `json:"keep_going"`
	DryRun         bool     `json:"dry_run"`
	Explain        bool     `json:"explain"`
	Yes            bool     `json:"yes"`
	Env            []string `json:"env"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Dot            bool     `json:"dot"`
	Command        string   `json:"command"`
	N              int      `json:"n"`
}

type mcpTool struct {
	Name        string
	Description string
	Schema      map[string]any
	Call        func(s *mcpServer, args mcpToolArgs) (string, error)
}

func schema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

var mcpTools = []mcpTool{
	{
		Name:        "list_targets",
		Description: "List the Constfile's commands with descriptions, arguments, prerequisites, and produced artifacts (JSON).",
		Schema: schema(map[string]any{
			"file": map[string]any{"type": "string", "description": "Constfile path (default: discovered in cwd)"},
			"cwd":  map[string]any{"type": "string", "description": "working directory (default: .)"},
		}),
		Call: func(s *mcpServer, a mcpToolArgs) (string, error) {
			argv := append(s.fileArg(a), "--list", "--json")
			return s.run(a, 30*time.Second, argv...)
		},
	},
	{
		Name:        "run_targets",
		Description: "Run construct targets and return their combined output. Use dry_run first to preview what would execute.",
		Schema: schema(map[string]any{
			"targets":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "target names (empty = default command)"},
			"file":            map[string]any{"type": "string"},
			"cwd":             map[string]any{"type": "string"},
			"jobs":            map[string]any{"type": "integer", "description": "max parallel commands"},
			"keep_going":      map[string]any{"type": "boolean", "description": "continue other targets when one fails"},
			"dry_run":         map[string]any{"type": "boolean", "description": "show commands without executing them"},
			"explain":         map[string]any{"type": "boolean", "description": "print why each command runs or is skipped"},
			"yes":             map[string]any{"type": "boolean", "description": "auto-approve confirm statements"},
			"env":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "variable overrides, key=value"},
			"timeout_seconds": map[string]any{"type": "integer", "description": "kill the run after N seconds (default 600)"},
		}),
		Call: func(s *mcpServer, a mcpToolArgs) (string, error) {
			var argv []string
			argv = append(argv, s.fileArg(a)...)
			if a.Jobs > 0 {
				argv = append(argv, "--jobs", fmt.Sprint(a.Jobs))
			}
			if a.KeepGoing {
				argv = append(argv, "--keep-going")
			}
			if a.DryRun {
				argv = append(argv, "--dry-run")
			}
			if a.Explain {
				argv = append(argv, "--explain")
			}
			if a.Yes {
				argv = append(argv, "--yes")
			}
			for _, kv := range a.Env {
				argv = append(argv, "-e", kv)
			}
			argv = append(argv, a.Targets...)
			timeout := 600 * time.Second
			if a.TimeoutSeconds > 0 {
				timeout = time.Duration(a.TimeoutSeconds) * time.Second
			}
			return s.run(a, timeout, argv...)
		},
	},
	{
		Name:        "graph",
		Description: "Show the dependency graph of targets as JSON (prerequisites and file deps per command).",
		Schema: schema(map[string]any{
			"targets": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"file":    map[string]any{"type": "string"},
			"cwd":     map[string]any{"type": "string"},
			"dot":     map[string]any{"type": "boolean", "description": "emit Graphviz DOT instead of JSON"},
		}),
		Call: func(s *mcpServer, a mcpToolArgs) (string, error) {
			format := "--json"
			if a.Dot {
				format = "--dot"
			}
			argv := append([]string{format, "graph"}, s.fileArg(a)...)
			argv = append(argv, a.Targets...)
			return s.run(a, 30*time.Second, argv...)
		},
	},
	{
		Name:        "lint",
		Description: "Run construct's static checks on the Constfile (JSON output): bad references, unused globals, misused keywords.",
		Schema: schema(map[string]any{
			"file": map[string]any{"type": "string"},
			"cwd":  map[string]any{"type": "string"},
		}),
		Call: func(s *mcpServer, a mcpToolArgs) (string, error) {
			argv := append([]string{"--json", "lint"}, s.fileArg(a)...)
			return s.run(a, 30*time.Second, argv...)
		},
	},
	{
		Name:        "run_history",
		Description: "Show recorded runs: per-command status, exit codes, durations, and captured output. Pass command + n for the nth-most-recent record of one command.",
		Schema: schema(map[string]any{
			"command": map[string]any{"type": "string", "description": "show this command's records instead of the summary list"},
			"n":       map[string]any{"type": "integer", "description": "with command: record index, 1 = most recent"},
			"file":    map[string]any{"type": "string"},
			"cwd":     map[string]any{"type": "string"},
		}),
		Call: func(s *mcpServer, a mcpToolArgs) (string, error) {
			argv := append([]string{"--json", "runs"}, s.fileArg(a)...)
			if a.Command != "" {
				argv = append(argv, "show", a.Command)
				if a.N > 0 {
					argv = append(argv, fmt.Sprint(a.N))
				}
			}
			return s.run(a, 30*time.Second, argv...)
		},
	},
}

func (s *mcpServer) fileArg(a mcpToolArgs) []string {
	f := a.File
	if f == "" {
		f = s.file
	}
	if f == "" || f == "Constfile" {
		return nil
	}
	return []string{f}
}

func (s *mcpServer) run(a mcpToolArgs, timeout time.Duration, argv ...string) (string, error) {
	dir := a.Cwd
	if dir == "" {
		dir = "."
	}
	output, code, err := s.runCmd(context.Background(), dir, argv, timeout)
	text := strings.TrimRight(output, "\n")
	if code != 0 {
		if text == "" {
			text = fmt.Sprintf("construct exited with code %d", code)
		} else {
			text = fmt.Sprintf("%s\n(exited with code %d)", text, code)
		}
		return text, fmt.Errorf("construct exited with code %d", code)
	}
	if text == "" {
		text = "(no output)"
	}
	return text, err
}

func mcpExec(ctx context.Context, exe, dir string, argv []string, timeout time.Duration) (string, int, error) {
	if timeout <= 0 {
		timeout = time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, exe, argv...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	code := 0
	if err != nil {
		code = 1
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			code = ee.ExitCode()
		}
		if cctx.Err() == context.DeadlineExceeded {
			return mcpTruncate(buf.String()), code, fmt.Errorf("timed out after %s", timeout)
		}
	}
	return mcpTruncate(buf.String()), code, nil
}

func mcpTruncate(s string) string {
	if len(s) <= mcpOutputCap {
		return s
	}
	return "... [output truncated]\n" + s[len(s)-mcpOutputCap:]
}
