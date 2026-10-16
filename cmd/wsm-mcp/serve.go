package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/basicauth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/grpcsrv"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/mcpsrv"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/registry"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
	wsmmcpversion "github.com/ardihikaru/whatsapp-with-dashboard/mcp"
)

// serve runs the gRPC and MCP HTTP doors until SIGINT/SIGTERM.
func serve(c config, log *slog.Logger) error {
	if c.HostsSpec == "" {
		return errors.New("WSM_MCP_HOSTS is required")
	}
	if c.WSMPass == "" {
		return errors.New("WSM_MCP_WSM_SECRET is required")
	}
	if c.WaxumToken == "" {
		return errors.New("WSM_MCP_WAXUM_TOKEN is required")
	}
	auth, err := basicauth.New(c.BasicUser, c.BasicPass)
	if err != nil {
		return err
	}
	pool, err := upstream.New(upstream.Config{
		HostsSpec: c.HostsSpec, WSMUser: c.WSMUser, WSMPass: c.WSMPass, WaxumToken: c.WaxumToken,
		Timeout: c.Timeout, AllowDangerous: c.AllowDangerous, DefaultPhone: c.DefaultPhone,
	})
	if err != nil {
		return err
	}
	reg := registry.New(pool)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	lis, err := net.Listen("tcp", c.GRPCAddr)
	if err != nil {
		return err
	}
	gs := grpcsrv.New(&grpcsrv.Server{Reg: reg, Version: wsmmcpversion.Version(), Log: log}, auth, int(c.MaxBodyBytes))
	hs := &http.Server{Addr: c.HTTPAddr, ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 120 * time.Second,
		Handler: (&mcpsrv.Server{Reg: reg, Auth: auth, Version: wsmmcpversion.Version(), Log: log, MaxBodyBytes: c.MaxBodyBytes}).Handler()}

	errc := make(chan error, 2)
	go func() { errc <- gs.Serve(lis) }()
	go func() {
		if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	log.Info("wsm-mcp up", "version", wsmmcpversion.Version(), "tools", len(reg.List()),
		"hosts", len(pool.Hosts), "grpc", c.GRPCAddr, "http", c.HTTPAddr, "allow_dangerous", c.AllowDangerous)

	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hs.Shutdown(sctx)
	done := make(chan struct{})
	go func() { gs.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		gs.Stop()
	}
	return err
}
