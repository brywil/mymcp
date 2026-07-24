package mcp

import "context"

// principalKey is the context key under which the authenticated principal (the
// matching bearer-token name) is carried into a tool call. Tools that need
// per-caller behavior (e.g. namespaced memory) read it via PrincipalFrom.
type principalKeyT struct{}

var principalKey principalKeyT

// WithPrincipal returns ctx carrying the authenticated principal name.
func WithPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, principalKey, principal)
}

// PrincipalFrom returns the principal carried in ctx, or "" if none.
func PrincipalFrom(ctx context.Context) string {
	if p, ok := ctx.Value(principalKey).(string); ok {
		return p
	}
	return ""
}
