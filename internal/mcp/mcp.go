// Package mcp discovers MCP tool definitions from the MCP servers configured
// in the opencode config (JSONC). Local servers are spawned over stdio,
// remote servers are queried over streamable HTTP, using the MCP JSON-RPC
// protocol (initialize + tools/list).
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Server describes one entry of the "mcp" section of the opencode config.
type Server struct {
	Name      string
	Type      string // "local" (stdio) or "remote" (HTTP)
	Command   []string
	Env       map[string]string
	URL       string
	Enabled   bool
}

// Tool is a single MCP tool definition as returned by tools/list.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// JSONDefinition renders the tool as a compact OpenAI-style function
// definition, approximating what the model provider receives at startup.
func (t Tool) JSONDefinition() string {
	fn := map[string]any{"name": t.Name}
	if t.Description != "" {
		fn["description"] = t.Description
	}
	if len(t.InputSchema) > 0 {
		fn["parameters"] = json.RawMessage(t.InputSchema)
	}
	b, err := json.Marshal(map[string]any{
		"type":     "function",
		"function": fn,
	})
	if err != nil {
		return ""
	}
	return string(b)
}

// LoadServers reads the "mcp" section of an opencode JSONC config file.
func LoadServers(configPath string) ([]Server, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg struct {
		MCP map[string]struct {
			Type        string            `json:"type"`
			Command     []string          `json:"command"`
			Environment map[string]string `json:"environment"`
			URL         string            `json:"url"`
			Enabled     *bool             `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(stripJSONC(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", configPath, err)
	}
	var servers []Server
	for name, entry := range cfg.MCP {
		enabled := true
		if entry.Enabled != nil {
			enabled = *entry.Enabled
		}
		typ := entry.Type
		if typ == "" {
			typ = "local"
		}
		servers = append(servers, Server{
			Name:    name,
			Type:    typ,
			Command: entry.Command,
			Env:     entry.Environment,
			URL:     entry.URL,
			Enabled: enabled,
		})
	}
	return servers, nil
}

// ListTools returns the tools advertised by the server.
func (s Server) ListTools(ctx context.Context) ([]Tool, error) {
	if s.Type == "remote" {
		return s.listToolsHTTP(ctx)
	}
	return s.listToolsStdio(ctx)
}

type toolRef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// --- stdio (local) servers ---

type stdioClient struct {
	dec *json.Decoder
	in  io.Writer
}

func (c *stdioClient) write(msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.in.Write(append(b, '\n'))
	return err
}

// request sends a JSON-RPC request and blocks until the response with the
// matching id arrives, skipping interleaved notifications.
func (c *stdioClient) request(ctx context.Context, method string, params any, id int, out any) error {
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp rpcResponse
		if err := c.dec.Decode(&resp); err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		var rid int
		if err := json.Unmarshal(resp.ID, &rid); err != nil || rid != id {
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("%s: rpc error %d: %s", method, resp.Error.Code, resp.Error.Message)
		}
		if out != nil {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("%s: decode result: %w", method, err)
			}
		}
		return nil
	}
}

func (s Server) listToolsStdio(ctx context.Context) ([]Tool, error) {
	if len(s.Command) == 0 {
		return nil, fmt.Errorf("server %q has no command", s.Name)
	}
	cmd := exec.CommandContext(ctx, s.Command[0], s.Command[1:]...)
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+expandHome(v))
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", s.Name, err)
	}
	defer func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	client := &stdioClient{dec: json.NewDecoder(bufio.NewReader(stdout)), in: stdin}
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := client.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "opencode-context-analyzer", "version": "0.1.0"},
	}, 1, &initResult); err != nil {
		return nil, err
	}
	_ = client.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})

	var result struct {
		Tools []toolRef `json:"tools"`
	}
	if err := client.request(ctx, "tools/list", map[string]any{}, 2, &result); err != nil {
		return nil, err
	}
	tools := make([]Tool, 0, len(result.Tools))
	for _, t := range result.Tools {
		tools = append(tools, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return tools, nil
}

// --- remote (HTTP) servers ---

func (s Server) httpRequest(ctx context.Context, method string, id int, params any, sessionID string) (*http.Response, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// readMCPResponse decodes a JSON-RPC response from a body that is either
// plain JSON or a streamable-HTTP SSE stream.
func readMCPResponse(body io.Reader, out any) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '{' {
		return json.Unmarshal(trimmed, out)
	}
	var last []byte
	for _, line := range strings.Split(string(trimmed), "\n") {
		if strings.HasPrefix(line, "data:") {
			last = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if last == nil {
		return fmt.Errorf("no JSON-RPC response in event stream")
	}
	return json.Unmarshal(last, out)
}

func (s Server) listToolsHTTP(ctx context.Context) ([]Tool, error) {
	if s.URL == "" {
		return nil, fmt.Errorf("server %q has no url", s.Name)
	}
	resp, err := s.httpRequest(ctx, "initialize", 1, map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "opencode-context-analyzer", "version": "0.1.0"},
	}, "")
	if err != nil {
		return nil, err
	}
	var initResp rpcResponse
	if err := readMCPResponse(resp.Body, &initResp); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	resp.Body.Close()
	if initResp.Error != nil {
		return nil, fmt.Errorf("initialize: rpc error %d: %s", initResp.Error.Code, initResp.Error.Message)
	}
	sessionID := resp.Header.Get("Mcp-Session-Id")

	resp, err = s.httpRequest(ctx, "tools/list", 2, map[string]any{}, sessionID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var listResp rpcResponse
	if err := readMCPResponse(resp.Body, &listResp); err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	if listResp.Error != nil {
		return nil, fmt.Errorf("tools/list: rpc error %d: %s", listResp.Error.Code, listResp.Error.Message)
	}
	var result struct {
		Tools []toolRef `json:"tools"`
	}
	if err := json.Unmarshal(listResp.Result, &result); err != nil {
		return nil, fmt.Errorf("tools/list: decode result: %w", err)
	}
	tools := make([]Tool, 0, len(result.Tools))
	for _, t := range result.Tools {
		tools = append(tools, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return tools, nil
}

func expandHome(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// DefaultConfigPath returns the default opencode config location.
func DefaultConfigPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	}
	return ""
}

// DefaultTimeout is the per-server tool-discovery timeout.
func DefaultTimeout() time.Duration {
	return 60 * time.Second
}

// stripJSONC removes // line comments and trailing commas so the result is
// valid JSON for encoding/json. It is string-aware: comment markers and
// commas inside string literals are left untouched.
func stripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	i := 0
	for i < len(data) {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			i++
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
			i++
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
		default:
			out = append(out, c)
			i++
		}
	}
	return stripTrailingCommas(out)
}

// stripTrailingCommas removes a comma when only whitespace follows it before
// a closing brace or bracket.
func stripTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == ',':
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}
