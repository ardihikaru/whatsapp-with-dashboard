// Package client is a typed Go gRPC client for wsm-mcp, for a backend
// that wants to call it directly rather than through the MCP front door.
package client

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/basicauth"
	pb "github.com/ardihikaru/whatsapp-with-dashboard/proto/wsmmcp/v1"
)

// Client wraps a gRPC connection to wsm-mcp with Basic auth metadata
// attached to every call.
type Client struct {
	conn   *grpc.ClientConn
	rpc    pb.WSMMCPClient
	header string
}

// Dial connects to addr (host:port, plaintext — wsm-mcp is only ever
// reached over WireGuard) with the given Basic credentials.
func Dial(addr, user, pass string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return &Client{conn: conn, rpc: pb.NewWSMMCPClient(conn), header: basicauth.Header(user, pass)}, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) withAuth(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", c.header)
}

// Health reports readiness and version.
func (c *Client) Health(ctx context.Context) (*pb.HealthResponse, error) {
	return c.rpc.Health(c.withAuth(ctx), &pb.HealthRequest{})
}

// ListTools lists every tool.
func (c *Client) ListTools(ctx context.Context) (*pb.ListToolsResponse, error) {
	return c.rpc.ListTools(c.withAuth(ctx), &pb.ListToolsRequest{})
}

// Call runs one tool with Go-native arguments, marshalled to JSON, and
// unmarshals a successful JSON result into out (pass nil to ignore it).
// A tool-level failure (IsError) is returned as a plain Go error.
func (c *Client) Call(ctx context.Context, name string, args any, out any) error {
	var argsJSON string
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return fmt.Errorf("marshal arguments: %w", err)
		}
		argsJSON = string(b)
	}
	resp, err := c.rpc.CallTool(c.withAuth(ctx), &pb.CallToolRequest{Name: name, ArgumentsJson: argsJSON})
	if err != nil {
		return err
	}
	if resp.IsError {
		return fmt.Errorf("wsm-mcp: %s: %s", name, resp.Error)
	}
	if out != nil && resp.ResultJson != "" {
		if err := json.Unmarshal([]byte(resp.ResultJson), out); err != nil {
			return fmt.Errorf("unmarshal result: %w", err)
		}
	}
	return nil
}
