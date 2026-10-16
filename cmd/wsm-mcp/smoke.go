package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/registry"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
)

// smoke runs a small set of read-only tools (default: wsm_hosts_status and
// wsm_resolve_session) and prints only ok/byte-count per tool, never the
// result bodies — see CLAUDE.md's logging rule. Intended for
// `docker exec wsm-mcp wsm-mcp smoke` after a deploy.
func smoke(c config, args []string) error {
	if c.HostsSpec == "" {
		return fmt.Errorf("WSM_MCP_HOSTS is required")
	}
	pool, err := upstream.New(upstream.Config{
		HostsSpec: c.HostsSpec, WSMUser: c.WSMUser, WSMPass: c.WSMPass, WaxumToken: c.WaxumToken,
		Timeout: c.Timeout, AllowDangerous: c.AllowDangerous, DefaultPhone: c.DefaultPhone,
	})
	if err != nil {
		return err
	}
	reg := registry.New(pool)
	names := args
	if len(names) == 0 {
		names = []string{"wsm_hosts_status", "wsm_resolve_session"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	failed := false
	for _, n := range names {
		start := time.Now()
		out, err := reg.Call(ctx, n, nil)
		ms := time.Since(start).Milliseconds()
		if err != nil {
			failed = true
			fmt.Printf("%-30s ok=false ms=%d err=%v\n", n, ms, err)
			continue
		}
		b, _ := json.Marshal(out)
		fmt.Printf("%-30s ok=true  ms=%d bytes=%d\n", n, ms, len(b))
	}
	if failed {
		return fmt.Errorf("one or more smoke tools failed")
	}
	slog.Info("smoke ok", "tools", len(names))
	return nil
}
