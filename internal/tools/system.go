package tools

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// sysTools provides host/system information.
type sysTools struct{}

func (s *sysTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "date_now",
		Description: "Current date and time (RFC3339, local).",
		ReadOnly:    true,
		Handler:     s.dateNow,
	})
	r.Register(&Tool{
		Name:        "system_status",
		Description: "Host name, OS/arch, CPUs, and load/uptime where available.",
		ReadOnly:    true,
		Handler:     s.status,
	})
}

func (s *sysTools) dateNow(_ context.Context, _ map[string]interface{}) (string, error) {
	return time.Now().Format(time.RFC3339), nil
}

func (s *sysTools) status(_ context.Context, _ map[string]interface{}) (string, error) {
	host, _ := os.Hostname()
	var b strings.Builder
	fmt.Fprintf(&b, "host: %s\n", host)
	fmt.Fprintf(&b, "os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "cpus: %d\n", runtime.NumCPU())
	if up, err := os.ReadFile("/proc/uptime"); err == nil {
		fmt.Fprintf(&b, "uptime: %s", strings.Fields(string(up))[0]+"s")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
