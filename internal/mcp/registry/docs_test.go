package registry

import (
	"os"
	"path/filepath"
	"testing"

	wsmmcpversion "github.com/ardihikaru/whatsapp-with-dashboard/mcp"
)

// TestToolsMdIsCurrent asserts the checked-in mcp/tools.md equals what
// ToolsMarkdown generates right now — a tool added/changed/removed without
// regenerating it (`go run ./cmd/wsm-mcp tools > mcp/tools.md`) fails this.
func TestToolsMdIsCurrent(t *testing.T) {
	want := ToolsMarkdown(testRegistry(t), wsmmcpversion.Version())
	got, err := os.ReadFile(filepath.Join("..", "..", "..", "mcp", "tools.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("mcp/tools.md is stale — regenerate it with `go run ./cmd/wsm-mcp tools > mcp/tools.md`")
	}
}
