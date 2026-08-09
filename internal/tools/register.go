package tools

import "time"

// Config controls which tools are registered and how they behave.
type Config struct {
	Workspace   string        // root that filesystem/exec tools are confined to
	AllowExec   bool          // enable shell-backed tools (gh, tmux/run_command)
	ExecTimeout time.Duration // per-command timeout (default 120s)
	HTTPTimeout time.Duration // per-request timeout for http/web tools (default 30s)
	TmuxSocket  string        // tmux socket path (-S); "" uses the default tmux server
	MemoryDir   string        // base dir for namespaced memory (<MemoryDir>/<principal>/); "" falls back to Workspace
	CacheDir    string        // base dir for generated scratch (web_cache, images); "" falls back to Workspace
}

// RegisterAll registers the full tool catalog into r per cfg.
//
// The catalog mirrors goclaw's native host tools so goclaw can mount them over
// MCP under their bare names. Tools that bind to goclaw's live process state
// (conversation/context, cron, Telegram notify, opencode/claude subprocess
// managers, and the Telegram-coupled web_render/show_image) intentionally stay
// native in goclaw and are not part of this server.
func RegisterAll(r *Registry, cfg Config) {
	if cfg.ExecTimeout == 0 {
		cfg.ExecTimeout = 120 * time.Second
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 30 * time.Second
	}

	// Always-on, dependency-light groups.
	(&fsTools{root: cfg.Workspace, cache: cfg.CacheDir}).register(r)
	(&sysTools{root: cfg.Workspace}).register(r)
	(&miscTools{}).register(r)
	memBase := cfg.MemoryDir
	if memBase == "" {
		memBase = cfg.Workspace
	}
	(&memoryTools{base: memBase}).register(r)
	(&httpTools{timeout: cfg.HTTPTimeout}).register(r)
	(&webSearchTools{root: cfg.Workspace, cache: cfg.CacheDir}).register(r)
	newThinkTools().register(r)

	// Shell-backed groups gated behind AllowExec.
	if cfg.AllowExec {
		(&ghTools{root: cfg.Workspace}).register(r)
		// Read-only git. Development happens in opencode; this is for answering
		// questions about a repo (including goclaw's own).
		(&gitTools{root: cfg.Workspace}).register(r)
		newTmuxTools(cfg.TmuxSocket).register(r)
	}
}
