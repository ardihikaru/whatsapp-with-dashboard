package main

import (
	"fmt"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/registry"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
	wsmmcpversion "github.com/ardihikaru/whatsapp-with-dashboard/mcp"
)

// toolsMarkdown prints the tool reference; mcp/tools.md is generated with
// `go run ./cmd/wsm-mcp tools > mcp/tools.md` — kept current by
// registry.TestToolsMdIsCurrent. No real hosts/credentials are needed to
// list tools, so this builds a placeholder pool rather than requiring
// WSM_MCP_HOSTS at generation time.
func toolsMarkdown() {
	pool, err := upstream.New(upstream.Config{HostsSpec: "placeholder=http://127.0.0.1:1|http://127.0.0.1:2", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		panic(err)
	}
	reg := registry.New(pool)
	fmt.Print(registry.ToolsMarkdown(reg, wsmmcpversion.Version()))
}
