// Package mcp implements a minimal, dependency-free MCP (Model Context Protocol)
// server over the Streamable HTTP transport: JSON-RPC 2.0 messages POSTed to a
// single endpoint, answered with application/json. This is the subset the
// llama.cpp web UI's MCP client (and other HTTP MCP clients) need for tool use.
package mcp

import (
	"context"
	"encoding/json"
)

// DefaultProtocolVersion is echoed when a client does not request one.
const DefaultProtocolVersion = "2025-06-18"

// JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Request is a JSON-RPC 2.0 request or notification. ID is kept raw so it can
// be echoed back verbatim (it may be a number, string, or absent).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether the message has no ID (no response expected).
func (r Request) IsNotification() bool { return len(r.ID) == 0 }

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func okResponse(id json.RawMessage, result interface{}) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Result: result}
}

func errResponse(id json.RawMessage, code int, msg string) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

// --- initialize ---

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// InitializeResult is returned from the "initialize" method.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
}

// Capabilities advertises optional server features.
type Capabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability advertises tool support.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// ServerInfo identifies the server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// --- tools ---

// ToolDef is a tool as advertised by tools/list.
type ToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// ToolsListResult is returned from tools/list.
type ToolsListResult struct {
	Tools []ToolDef `json:"tools"`
}

type callToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// ToolContent is one content block in a tool result.
type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CallToolResult is returned from tools/call.
type CallToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ToolProvider is the tool backend the server dispatches to. Implemented by
// the tools registry; declared here so the mcp package does not import tools
// (avoids an import cycle).
type ToolProvider interface {
	ListTools() []ToolDef
	CallTool(ctx context.Context, name string, args map[string]interface{}) (text string, isError bool)
	ReadOnly(name string) bool
}

// Authorizer decides, per client-certificate CN, whether a principal is known
// and whether it may call a given tool. Implemented by policy.Enforcer.
type Authorizer interface {
	Known(cn string) bool
	Allow(cn, tool string, readOnly bool) bool
}
