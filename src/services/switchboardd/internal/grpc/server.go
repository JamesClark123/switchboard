// Package grpc implements the daemon side of the Switchboard contract over a Unix
// domain socket (R1). It wraps the sandbox.Manager and the option manifest, and
// exposes Serve to bind the socket.
package grpc

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc"
)

// Server implements pb.SwitchboardServer.
type Server struct {
	pb.UnimplementedSwitchboardServer

	mgr           *sandbox.Manager
	hostID        string
	hostname      string
	daemonVersion string
	sbxVersion    string
	workspaceRoot string
	manifest      *pb.OptionManifest
	hub           *agent.Hub
	agents        *agent.Registry

	grpc *grpc.Server
}

// Config wires a Server.
type Config struct {
	Manager       *sandbox.Manager
	HostID        string
	DaemonVersion string
	SbxVersion    string
	WorkspaceRoot string
	// Manifest is the host's full sbx option surface (FR-014). MAY be nil when
	// introspection was unavailable, in which case launch-time validation is a
	// no-op and the editor renders no options.
	Manifest *pb.OptionManifest
	// Hub fans out live events + buffers notifications (US4). When set, manager
	// changes are published to it. Agents is the per-sandbox PTY registry.
	Hub    *agent.Hub
	Agents *agent.Registry
	// Debug, when true, logs every RPC action and error to stderr (serve --debug).
	Debug bool
}

// NewServer constructs a Server.
func NewServer(cfg Config) *Server {
	hn, _ := os.Hostname()
	sbxVersion := cfg.SbxVersion
	if sbxVersion == "" && cfg.Manifest != nil {
		sbxVersion = cfg.Manifest.GetSbxVersion()
	}
	s := &Server{
		mgr:           cfg.Manager,
		hostID:        cfg.HostID,
		hostname:      hn,
		daemonVersion: cfg.DaemonVersion,
		sbxVersion:    sbxVersion,
		workspaceRoot: cfg.WorkspaceRoot,
		manifest:      cfg.Manifest,
		hub:           cfg.Hub,
		agents:        cfg.Agents,
	}
	// Publish manager sandbox-changes onto the event hub (US4 live updates).
	if cfg.Hub != nil && cfg.Manager != nil {
		cfg.Manager.SetOnChange(cfg.Hub.PublishSandbox)
	}
	var opts []grpc.ServerOption
	if cfg.Debug {
		dbg := log.New(os.Stderr, "sxbd debug ", log.LstdFlags|log.Lmsgprefix)
		unary, stream := newDebugInterceptors(dbg.Printf)
		opts = append(opts, grpc.ChainUnaryInterceptor(unary), grpc.ChainStreamInterceptor(stream))
	}
	s.grpc = grpc.NewServer(opts...)
	pb.RegisterSwitchboardServer(s.grpc, s)
	return s
}

// Serve binds a Unix socket at socketPath and serves until ctx is cancelled. Any
// stale socket file is removed first; the parent dir is created if needed.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return err
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}

	go func() {
		<-ctx.Done()
		s.grpc.GracefulStop()
		_ = os.Remove(socketPath)
	}()

	return s.grpc.Serve(lis)
}

// ServeListener serves on an existing listener (used by tests with an in-process
// socket and by `dial-stdio`/pipe transports).
func (s *Server) ServeListener(lis net.Listener) error { return s.grpc.Serve(lis) }

// Stop gracefully stops the gRPC server.
func (s *Server) Stop() { s.grpc.GracefulStop() }

// GetDaemonInfo returns identity/capability metadata (FR-006).
func (s *Server) GetDaemonInfo(_ context.Context, _ *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{
		HostId:        s.hostID,
		Hostname:      s.hostname,
		DaemonVersion: s.daemonVersion,
		SbxVersion:    s.sbxVersion,
		WorkspaceRoot: s.workspaceRoot,
	}, nil
}

// ListSandboxes returns every sandbox known to this daemon (FR-017).
func (s *Server) ListSandboxes(_ context.Context, _ *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	all, err := s.mgr.List()
	if err != nil {
		return nil, err
	}
	return &pb.ListSandboxesResponse{Sandboxes: all}, nil
}
