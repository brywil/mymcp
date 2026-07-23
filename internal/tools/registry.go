// Package tools implements the MCP tool catalog and a registry that adapts it
// to the mcp.ToolProvider interface. Tools import mcp for its result types; the
// mcp package does not import tools (no import cycle).
package tools

import (
	"context"

	"github.com/brywil/mymcp/internal/mcp"
)

// Handler executes a tool call and returns human/model-readable text.
type Handler func(ctx context.Context, args map[string]interface{}) (string, error)

// Tool is one registered tool.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]interface{}
	ReadOnly    bool // eligible for the "ro" capability preset
	Handler     Handler
}

// Registry holds tools in registration order.
type Registry struct {
	order []string
	tools map[string]*Tool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]*Tool{}} }

// Register adds or replaces a tool.
func (r *Registry) Register(t *Tool) {
	if _, ok := r.tools[t.Name]; !ok {
		r.order = append(r.order, t.Name)
	}
	r.tools[t.Name] = t
}

// Count returns the number of registered tools.
func (r *Registry) Count() int { return len(r.order) }

// ReadOnly reports whether the named tool is read-only (for the "ro" preset).
func (r *Registry) ReadOnly(name string) bool {
	if t, ok := r.tools[name]; ok {
		return t.ReadOnly
	}
	return false
}

// ListTools implements mcp.ToolProvider.
func (r *Registry) ListTools() []mcp.ToolDef {
	defs := make([]mcp.ToolDef, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		schema := t.Schema
		if schema == nil {
			schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		defs = append(defs, mcp.ToolDef{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return defs
}

// CallTool implements mcp.ToolProvider. A handler error is reported as an MCP
// tool error (isError=true) rather than a protocol error.
func (r *Registry) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, bool) {
	t, ok := r.tools[name]
	if !ok {
		return "unknown tool: " + name, true
	}
	if args == nil {
		args = map[string]interface{}{}
	}
	out, err := t.Handler(ctx, args)
	if err != nil {
		return "error: " + err.Error(), true
	}
	return out, false
}

// --- argument helpers (JSON numbers decode as float64) ---

func argString(m map[string]interface{}, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func argBool(m map[string]interface{}, k string, def bool) bool {
	if v, ok := m[k]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func argInt(m map[string]interface{}, k string, def int) int {
	if v, ok := m[k]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return def
}

func obj(props map[string]interface{}, required ...string) map[string]interface{} {
	schema := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func strProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}
