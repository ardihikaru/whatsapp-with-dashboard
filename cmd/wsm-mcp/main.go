// Command wsm-mcp serves WSM's /api/v1 and waxum's client API as one MCP
// server (Streamable HTTP + gRPC), over a failover pool of configured
// hosts. Subcommands: serve | smoke | tools | version. See mcp/README.md
// and mcp/TUTORIAL.md.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
	wsmmcpversion "github.com/ardihikaru/whatsapp-with-dashboard/mcp"
)

// config is read from the environment — on clinodb, ~/.config/wsm-mcp/mcp.env
// (see deploy/clinodb/README.md and mcp/mcp.env.example).
type config struct {
	HostsSpec      string
	WSMUser        string
	WSMPass        string
	WaxumToken     string
	Timeout        time.Duration
	AllowDangerous bool
	DefaultPhone   string

	GRPCAddr, HTTPAddr   string
	BasicUser, BasicPass string
	MaxBodyBytes         int64
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt64(k string, def int64) int64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

func load() config {
	return config{
		HostsSpec:      os.Getenv("WSM_MCP_HOSTS"),
		WSMUser:        env("WSM_MCP_WSM_LOGIN_ID", "wsm-mcp"),
		WSMPass:        os.Getenv("WSM_MCP_WSM_SECRET"),
		WaxumToken:     os.Getenv("WSM_MCP_WAXUM_TOKEN"),
		Timeout:        30 * time.Second,
		AllowDangerous: upstream.EnvBool("WSM_MCP_ALLOW_DANGEROUS", false),
		DefaultPhone:   os.Getenv("WSM_MCP_DEFAULT_PHONE"),
		GRPCAddr:       env("WSM_MCP_GRPC_ADDR", "127.0.0.1:18811"),
		HTTPAddr:       env("WSM_MCP_HTTP_ADDR", "127.0.0.1:18812"),
		BasicUser:      env("WSM_MCP_BASIC_USER", "wsm-mcp"),
		BasicPass:      os.Getenv("WSM_MCP_BASIC_PASS"),
		MaxBodyBytes:   envInt64("WSM_MCP_MAX_BODY_BYTES", 140<<20),
	}
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(load(), log)
	case "smoke":
		err = smoke(load(), os.Args[2:])
	case "tools":
		toolsMarkdown()
	case "version":
		fmt.Println(wsmmcpversion.Version())
	default:
		err = fmt.Errorf("usage: wsm-mcp serve | smoke [tool ...] | tools | version")
	}
	if err != nil {
		log.Error("wsm-mcp", "cmd", cmd, "err", err)
		os.Exit(1)
	}
}
