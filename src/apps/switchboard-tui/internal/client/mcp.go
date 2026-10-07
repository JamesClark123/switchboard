package client

import (
	"context"
	"errors"
	"io"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// MCP gateway wrappers (feature 008). Each daemon owns one gateway manager; these
// calls act on the daemon behind this Conn only.

// McpAuthUpdate is one frame of an authorization wait: the URL to open (once),
// a progress line, or the terminal outcome.
type McpAuthUpdate struct {
	URL      string
	Message  string
	Deadline time.Time
	Done     *pb.McpAuthState // terminal frame
	Err      string           // terminal host diagnostic; the registration is unchanged
}

func (c *Conn) ListMcpServers(ctx context.Context) ([]*pb.McpServer, *pb.McpGatewaySettings, error) {
	resp, err := c.api.ListMcpServers(ctx, &pb.ListMcpServersRequest{})
	if err != nil {
		return nil, nil, err
	}
	return resp.GetServers(), resp.GetSettings(), nil
}

func (c *Conn) AddMcpServer(ctx context.Context, req *pb.AddMcpServerRequest) (*pb.McpServer, error) {
	return c.api.AddMcpServer(ctx, req)
}

func (c *Conn) RemoveMcpServer(ctx context.Context, name string) ([]string, error) {
	resp, err := c.api.RemoveMcpServer(ctx, &pb.RemoveMcpServerRequest{Name: name})
	if err != nil {
		return nil, err
	}
	return resp.GetNotes(), nil
}

// AuthorizeMcpServer drains the authorization stream into onUpdate and returns the
// terminal state. Cancelling ctx cancels the daemon-side wait (FR-075).
func (c *Conn) AuthorizeMcpServer(ctx context.Context, name string, onUpdate func(McpAuthUpdate)) (pb.McpAuthState, error) {
	stream, err := c.api.AuthorizeMcpServer(ctx, &pb.AuthorizeMcpServerRequest{Name: name})
	if err != nil {
		return pb.McpAuthState_MCP_AUTH_STATE_UNSPECIFIED, err
	}
	state := pb.McpAuthState_MCP_AUTH_STATE_UNSPECIFIED
	var hostErr string
	for {
		f, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return state, err
		}
		u := McpAuthUpdate{URL: f.GetUrl(), Message: f.GetMessage(), Err: f.GetError()}
		if d := f.GetDeadlineUnix(); d > 0 {
			u.Deadline = time.Unix(d, 0)
		}
		if d := f.GetDone(); d != pb.McpAuthState_MCP_AUTH_STATE_UNSPECIFIED {
			done := d
			u.Done = &done
			state = d
		}
		if u.Err != "" {
			hostErr = u.Err
		}
		if onUpdate != nil {
			onUpdate(u)
		}
	}
	if state == pb.McpAuthState_MCP_AUTH_STATE_UNSPECIFIED && hostErr != "" {
		return pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED, errors.New(hostErr)
	}
	return state, nil
}

func (c *Conn) SetMcpServerDefault(ctx context.Context, name string, on bool) (*pb.McpServer, error) {
	return c.api.SetMcpServerDefault(ctx, &pb.SetMcpServerDefaultRequest{Name: name, AttachByDefault: on})
}

func (c *Conn) SetMcpAttachMode(ctx context.Context, mode pb.McpAttachMode) (*pb.McpGatewaySettings, error) {
	return c.api.SetMcpAttachMode(ctx, &pb.SetMcpAttachModeRequest{Mode: mode})
}
