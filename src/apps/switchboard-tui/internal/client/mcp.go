package client

import (
	"context"
	"errors"
	"io"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MCP gateway wrappers (feature 008). Each daemon owns one gateway manager; these
// calls act on the daemon behind this Conn only.

// McpAuthUpdate is one frame of an authorization wait: the URL to open (once),
// a progress line, or the terminal outcome.
type McpAuthUpdate struct {
	URL      string
	Message  string
	Deadline time.Time
	// CallbackPort is the loopback port of the host's OAuth callback listener (0 if
	// none was announced); a client on another machine relays it.
	CallbackPort uint32
	Done         *pb.McpAuthState // terminal frame
	Err          string           // terminal host diagnostic; the registration is unchanged
}

func (c *Conn) ListMcpServers(ctx context.Context) ([]*pb.McpServer, *pb.McpGatewaySettings, error) {
	resp, err := c.API().ListMcpServers(ctx, &pb.ListMcpServersRequest{})
	if err != nil {
		return nil, nil, err
	}
	return resp.GetServers(), resp.GetSettings(), nil
}

// ListMcpGateway is ListMcpServers with the whole response, so the gateway view
// also gets the pending replaces (SWI-21 FR-426).
func (c *Conn) ListMcpGateway(ctx context.Context) (*pb.ListMcpServersResponse, error) {
	return c.API().ListMcpServers(ctx, &pb.ListMcpServersRequest{})
}

func (c *Conn) AddMcpServer(ctx context.Context, req *pb.AddMcpServerRequest) (*pb.McpServer, error) {
	return c.API().AddMcpServer(ctx, req)
}

func (c *Conn) RemoveMcpServer(ctx context.Context, name string) ([]string, error) {
	resp, err := c.API().RemoveMcpServer(ctx, &pb.RemoveMcpServerRequest{Name: name})
	if err != nil {
		return nil, err
	}
	return resp.GetNotes(), nil
}

// AuthorizeMcpServer drains the authorization stream into onUpdate and returns the
// terminal state. Cancelling ctx cancels the daemon-side wait (FR-075). scopes and
// noScope are a one-time scope choice (SWI-21 FR-418); neither set keeps the
// recorded defaults.
func (c *Conn) AuthorizeMcpServer(ctx context.Context, name string, scopes []string, noScope bool, onUpdate func(McpAuthUpdate)) (pb.McpAuthState, error) {
	stream, err := c.API().AuthorizeMcpServer(ctx, &pb.AuthorizeMcpServerRequest{Name: name, Scopes: scopes, NoScope: noScope})
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
		u := McpAuthUpdate{URL: f.GetUrl(), Message: f.GetMessage(), Err: f.GetError(), CallbackPort: f.GetCallbackPort()}
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

// CompleteMcpAuthCallback is the paste-back fallback: it hands the daemon the
// browser's failed http://localhost:<port>/... redirect to replay on its host.
func (c *Conn) CompleteMcpAuthCallback(ctx context.Context, name, callbackURL string) error {
	_, err := c.API().CompleteMcpAuthCallback(ctx, &pb.CompleteMcpAuthCallbackRequest{Name: name, CallbackUrl: callbackURL})
	return err
}

func (c *Conn) SetMcpServerDefault(ctx context.Context, name string, on bool) (*pb.McpServer, error) {
	return c.API().SetMcpServerDefault(ctx, &pb.SetMcpServerDefaultRequest{Name: name, AttachByDefault: on})
}

func (c *Conn) SetMcpAttachMode(ctx context.Context, mode pb.McpAttachMode) (*pb.McpGatewaySettings, error) {
	return c.API().SetMcpAttachMode(ctx, &pb.SetMcpAttachModeRequest{Mode: mode})
}

// AttachMcpServer loads a registered server into an existing sandbox on this
// daemon (FR-102) and returns the updated record. The daemon bounds the load.
// DetachMcpServer unloads a server from an existing sandbox on this daemon
// (SWI-3) and returns the updated record. The registration is untouched.
func (c *Conn) DetachMcpServer(ctx context.Context, sandboxID, name string) (*pb.Sandbox, error) {
	return c.API().DetachMcpServer(ctx, &pb.DetachMcpServerRequest{SandboxId: sandboxID, Name: name})
}

func (c *Conn) AttachMcpServer(ctx context.Context, sandboxID, name string) (*pb.Sandbox, error) {
	return c.API().AttachMcpServer(ctx, &pb.AttachMcpServerRequest{SandboxId: sandboxID, Name: name})
}

// --- SWI-21: edit, save, pending replace, secret removal ---

// SaveRefusal is a refused MCP save or edit (FAILED_PRECONDITION or NOT_FOUND
// carrying an McpSaveRefusal detail): the daemon changed nothing. Message is the
// daemon's own text, shown as is.
type SaveRefusal struct {
	Reason  pb.McpSaveRefusal_Reason
	Message string
	Code    codes.Code
}

func (r *SaveRefusal) Error() string { return r.Message }

// GRPCStatus keeps the code for status.Code.
func (r *SaveRefusal) GRPCStatus() *status.Status { return status.New(r.Code, r.Message) }

// AsSaveRefusal decodes a refusal from any MCP call's error (save, definition
// read, cancel, secret removal, mark toggle). ok is false for other errors.
func AsSaveRefusal(err error) (*SaveRefusal, bool) {
	var r *SaveRefusal
	if errors.As(err, &r) {
		return r, true
	}
	st, ok := status.FromError(err)
	if !ok || (st.Code() != codes.FailedPrecondition && st.Code() != codes.NotFound) {
		return nil, false
	}
	for _, d := range st.Details() {
		if ref, ok := d.(*pb.McpSaveRefusal); ok {
			msg := ref.GetMessage()
			if msg == "" {
				msg = st.Message()
			}
			return &SaveRefusal{Reason: ref.GetReason(), Message: msg, Code: st.Code()}, true
		}
	}
	return nil, false
}

// mcpErr turns a refusal status into a *SaveRefusal, leaving other errors as
// they are.
func mcpErr(err error) error {
	if r, ok := AsSaveRefusal(err); ok {
		return r
	}
	return err
}

// GetMcpServerDefinition reads the edit form's pre-fill (FR-402); forAdd reads
// only the secret status for a name that need not be registered.
func (c *Conn) GetMcpServerDefinition(ctx context.Context, name string, forAdd bool) (*pb.McpDefinitionView, error) {
	v, err := c.API().GetMcpServerDefinition(ctx, &pb.GetMcpServerDefinitionRequest{Name: name, ForAdd: forAdd})
	if err != nil {
		return nil, mcpErr(err)
	}
	return v, nil
}

// SaveMcpServer runs one save (FR-404). It returns the outcome whenever the save
// ran, even if a host step failed; a refusal is a *SaveRefusal.
func (c *Conn) SaveMcpServer(ctx context.Context, req *pb.SaveMcpServerRequest) (*pb.McpSaveOutcome, error) {
	out, err := c.API().SaveMcpServer(ctx, req)
	if err != nil {
		return nil, mcpErr(err)
	}
	return out, nil
}

// CancelMcpPendingReplace drops a pending replace (FR-406, FR-426).
func (c *Conn) CancelMcpPendingReplace(ctx context.Context, name string, revision uint64) (*pb.CancelMcpPendingReplaceResponse, error) {
	out, err := c.API().CancelMcpPendingReplace(ctx, &pb.CancelMcpPendingReplaceRequest{Name: name, Revision: revision})
	if err != nil {
		return nil, mcpErr(err)
	}
	return out, nil
}

// RemoveMcpServerSecrets removes secrets a save stopped using (FR-413).
func (c *Conn) RemoveMcpServerSecrets(ctx context.Context, name string, secrets []string) (*pb.RemoveMcpServerSecretsResponse, error) {
	out, err := c.API().RemoveMcpServerSecrets(ctx, &pb.RemoveMcpServerSecretsRequest{Name: name, Secrets: secrets})
	if err != nil {
		return nil, mcpErr(err)
	}
	return out, nil
}
