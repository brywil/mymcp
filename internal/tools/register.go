package tools

import "time"

// Config controls which tools are registered and how they behave.
type Config struct {
	Workspace   string        // root that filesystem tools are confined to
	AllowExec   bool          // enable run_command + git tools
	ExecTimeout time.Duration // per-command timeout (default 120s)
	HTTPTimeout time.Duration // per-request timeout for http_fetch (default 30s)
}

// RegisterAll registers the full tool catalog into r per cfg.
func RegisterAll(r *Registry, cfg Config) {
	if cfg.ExecTimeout == 0 {
		cfg.ExecTimeout = 120 * time.Second
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 30 * time.Second
	}
	(&fsTools{root: cfg.Workspace}).register(r)
	(&sysTools{}).register(r)
	(&webTools{timeout: cfg.HTTPTimeout}).register(r)
	if cfg.AllowExec {
		(&execTools{root: cfg.Workspace, timeout: cfg.ExecTimeout}).register(r)
		(&gitTools{root: cfg.Workspace, timeout: cfg.ExecTimeout}).register(r)
	}
}
