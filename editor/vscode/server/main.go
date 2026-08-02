package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
)

// Minimal LSP-over-stdio server. Reads Content-Length-framed JSON-RPC messages
// from stdin, dispatches to handlers, and writes responses to stdout.

const lspVersion = "0.2.0"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("construct-lsp %s\n", lspVersion)
		return
	}

	log.SetOutput(os.Stderr)
	srv := newServer()
	serve(os.Stdin, os.Stdout, srv)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func serve(in io.Reader, out io.Writer, srv *server) {
	reader := bufio.NewReader(in)
	for {
		msg, err := readMessage(reader)
		if err != nil {
			if err == io.EOF {
				return
			}
			log.Printf("read error: %v", err)
			return
		}

		var req rpcRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			log.Printf("unmarshal error: %v", err)
			continue
		}

		result, err := srv.dispatch(req.Method, req.Params)
		if err != nil {
			writeResponse(out, req.ID, nil, &rpcError{Code: -32603, Message: err.Error()})
			continue
		}

		// Notifications (no id) don't get a response.
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}

		writeResponse(out, req.ID, result, nil)
	}
}

// readMessage reads a single LSP message (Content-Length framed) from r.
func readMessage(r *bufio.Reader) ([]byte, error) {
	var contentLength int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			val := strings.TrimSpace(line[len("content-length:"):])
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("bad content-length %q: %w", val, err)
			}
			contentLength = n
		}
	}
	if contentLength == 0 {
		return nil, fmt.Errorf("missing content-length header")
	}

	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeResponse(out io.Writer, id json.RawMessage, result interface{}, errResp *rpcError) {
	var idVal interface{}
	if len(id) > 0 && string(id) != "null" {
		_ = json.Unmarshal(id, &idVal)
	}

	// JSON-RPC requires exactly one of result/error. When there's an error we
	// must omit result entirely; when there's no error, result must always be
	// present (even if null) so the client doesn't reject the response.
	var body []byte
	if errResp != nil {
		body, _ = json.Marshal(struct {
			JSONRPC string      `json:"jsonrpc"`
			ID      interface{} `json:"id"`
			Error   *rpcError   `json:"error"`
		}{"2.0", idVal, errResp})
	} else {
		body, _ = json.Marshal(struct {
			JSONRPC string      `json:"jsonrpc"`
			ID      interface{} `json:"id"`
			Result  interface{} `json:"result"`
		}{"2.0", idVal, result})
	}
	fmt.Fprintf(out, "Content-Length: %d\r\n\r\n", len(body))
	out.Write(body)
}

func writeNotification(out io.Writer, method string, params interface{}) {
	notif := rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  nil,
	}
	body, _ := json.Marshal(notif)
	if params != nil {
		n := struct {
			JSONRPC string      `json:"jsonrpc"`
			Method  string      `json:"method"`
			Params  interface{} `json:"params,omitempty"`
		}{"2.0", method, params}
		body, _ = json.Marshal(n)
	}
	fmt.Fprintf(out, "Content-Length: %d\r\n\r\n", len(body))
	out.Write(body)
}
