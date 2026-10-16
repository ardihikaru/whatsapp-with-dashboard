// Package grpcsrv serves wsm-mcp's tool registry over gRPC (wsmmcp.v1.WSMMCP)
// — the same registry internal/mcp/mcpsrv serves as MCP Streamable HTTP.
// Same shape as hikaru-mcp's internal/grpcsrv: one generic
// Health/ListTools/CallTool service, not one RPC per tool.
package grpcsrv

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/basicauth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/registry"
	pb "github.com/ardihikaru/whatsapp-with-dashboard/proto/wsmmcp/v1"
)

// MaxRecvMsgSize is overridable (WSM_MCP_MAX_BODY_BYTES) so base64 media
// arguments to CallTool fit — see New.
const defaultMaxRecvMsgSize = 1 << 20

// Server implements pb.WSMMCPServer.
type Server struct {
	pb.UnimplementedWSMMCPServer
	Reg     *registry.Registry
	Version string
	Log     *slog.Logger
}

// New builds a grpc.Server with Basic auth on every call and reflection
// registered (so grpcurl needs no .proto file — see mcp/TUTORIAL.md).
func New(s *Server, auth *basicauth.Checker, maxRecvMsgSize int) *grpc.Server {
	if maxRecvMsgSize <= 0 {
		maxRecvMsgSize = defaultMaxRecvMsgSize
	}
	g := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			if v := md.Get("authorization"); len(v) == 0 || !auth.Check(v[0]) {
				return nil, status.Error(codes.Unauthenticated, "basic auth required")
			}
			return h(ctx, req)
		}),
		grpc.MaxRecvMsgSize(maxRecvMsgSize),
	)
	pb.RegisterWSMMCPServer(g, s)
	reflection.Register(g)
	return g
}

// Health reports readiness.
func (s *Server) Health(ctx context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Ok: true, Version: s.Version, Tools: int32(len(s.Reg.List()))}, nil
}

// ListTools lists every tool.
func (s *Server) ListTools(context.Context, *pb.ListToolsRequest) (*pb.ListToolsResponse, error) {
	out := &pb.ListToolsResponse{}
	for _, t := range s.Reg.List() {
		schema, _ := json.Marshal(t.InputSchema())
		ann, _ := json.Marshal(t.Annotations())
		out.Tools = append(out.Tools, &pb.ToolInfo{
			Name: t.Name, Description: t.Desc, InputSchemaJson: string(schema), AnnotationsJson: string(ann),
			Route: t.Route, WaxumMethod: t.WaxumMethod, Danger: t.Danger,
		})
	}
	return out, nil
}

// CallTool runs one tool. Tool failures come back as is_error (the caller
// can read and retry); only an unknown tool is a gRPC NotFound. Arguments
// and results are never logged (CLAUDE.md: logging records tool name,
// host, duration, ok only).
func (s *Server) CallTool(ctx context.Context, in *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	if !s.Reg.Has(in.Name) {
		return nil, status.Errorf(codes.NotFound, "unknown tool %q", in.Name)
	}
	start := time.Now()
	out, err := s.Reg.Call(ctx, in.Name, []byte(in.ArgumentsJson))
	ms := time.Since(start).Milliseconds()
	if err != nil {
		if s.Log != nil {
			s.Log.Info("tool", "door", "grpc", "tool", in.Name, "ms", ms, "ok", false)
		}
		return &pb.CallToolResponse{IsError: true, Error: err.Error(), DurationMs: ms}, nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if s.Log != nil {
		s.Log.Info("tool", "door", "grpc", "tool", in.Name, "ms", ms, "ok", true)
	}
	return &pb.CallToolResponse{ResultJson: string(b), DurationMs: ms}, nil
}
