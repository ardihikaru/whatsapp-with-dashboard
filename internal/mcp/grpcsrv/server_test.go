package grpcsrv

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/basicauth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/registry"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
	pb "github.com/ardihikaru/whatsapp-with-dashboard/proto/wsmmcp/v1"
)

// dial builds an in-memory (bufconn) client+server pair for one test.
func dial(t *testing.T) (pb.WSMMCPClient, func()) {
	t.Helper()
	pool, err := upstream.New(upstream.Config{HostsSpec: "x=http://127.0.0.1:1|http://127.0.0.1:2", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := basicauth.New("wsm-mcp", "a-password-of-sixteen-chars-plus")
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(pool)
	gs := New(&Server{Reg: reg, Version: "test"}, auth, 0)

	lis := bufconn.Listen(1024 * 1024)
	go gs.Serve(lis)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return pb.NewWSMMCPClient(conn), func() { conn.Close(); gs.Stop() }
}

func withAuth(ctx context.Context, header string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", header)
}

// TestCallWithNoCredentialsIsUnauthenticated asserts the interceptor
// refuses before any RPC method runs.
func TestCallWithNoCredentialsIsUnauthenticated(t *testing.T) {
	c, closeFn := dial(t)
	defer closeFn()
	_, err := c.ListTools(context.Background(), &pb.ListToolsRequest{})
	if status.Code(err) != 16 { // codes.Unauthenticated
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

// TestListToolsReturnsEveryRegisteredTool pins the count against the live
// registry, the gRPC equivalent of the mcpsrv tools/list test.
func TestListToolsReturnsEveryRegisteredTool(t *testing.T) {
	c, closeFn := dial(t)
	defer closeFn()
	ctx := withAuth(context.Background(), basicauth.Header("wsm-mcp", "a-password-of-sixteen-chars-plus"))
	resp, err := c.ListTools(ctx, &pb.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(resp.Tools) == 0 {
		t.Fatal("expected at least one tool")
	}
}

// TestCallToolOnAnUnknownNameIsNotFound asserts an unknown tool name is a
// gRPC NotFound, distinct from a tool-level failure (which comes back as
// is_error on a successful RPC, not a gRPC error).
func TestCallToolOnAnUnknownNameIsNotFound(t *testing.T) {
	c, closeFn := dial(t)
	defer closeFn()
	ctx := withAuth(context.Background(), basicauth.Header("wsm-mcp", "a-password-of-sixteen-chars-plus"))
	_, err := c.CallTool(ctx, &pb.CallToolRequest{Name: "no_such_tool"})
	if status.Code(err) != 5 { // codes.NotFound
		t.Fatalf("expected NotFound, got %v", err)
	}
}

// TestHealthReportsToolCount cross-checks Health's tool count against
// ListTools' own, so the two can never silently disagree.
func TestHealthReportsToolCount(t *testing.T) {
	c, closeFn := dial(t)
	defer closeFn()
	ctx := withAuth(context.Background(), basicauth.Header("wsm-mcp", "a-password-of-sixteen-chars-plus"))
	h, err := c.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	lt, err := c.ListTools(ctx, &pb.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if int(h.Tools) != len(lt.Tools) {
		t.Fatalf("Health.Tools=%d but ListTools returned %d", h.Tools, len(lt.Tools))
	}
}
