package worker

import (
	"context"
	"fmt"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// API is the worker side as an orchestrator's runtime calls it (research R3/R6):
// the five worker-side RPCs plus the sandbox list and the event stream it reads
// status from. A remote host is served by Remote over the orchestrator's dialed
// connection; the orchestrator's own host by Local, in-process, so the dispatcher
// never special-cases locality (R2).
type API interface {
	ListSandboxes(ctx context.Context) ([]*pb.Sandbox, error)
	AttachController(ctx context.Context, req *pb.AttachControllerRequest) (*pb.Sandbox, error)
	DetachController(ctx context.Context, req *pb.DetachControllerRequest) (*pb.Sandbox, error)
	UpdateWorker(ctx context.Context, req *pb.UpdateWorkerRequest) (*pb.Sandbox, error)
	DeliverWorkerPrompt(ctx context.Context, req *pb.DeliverWorkerPromptRequest) (*pb.DeliverWorkerPromptResponse, error)
	// DeliverWorkerStep types one step of a run's sequence and runs its submit
	// check (SWI-17, contracts/worker-step.md). ErrStepSent when the step was
	// typed before (read controller.run.step_sent_at from the sandbox record);
	// ErrUnsupported when the worker's daemon predates it.
	DeliverWorkerStep(ctx context.Context, req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error)
	// Subscribe opens the host's live event stream (no replay; the orchestrator
	// re-reads records on reconnect instead).
	Subscribe(ctx context.Context) (EventStream, error)
	// OpenLink opens the call relay; the caller sends `hello` first (R6).
	OpenLink(ctx context.Context) (LinkStream, error)
}

// EventStream is the receive side of a Subscribe stream.
type EventStream interface {
	Recv() (*pb.Event, error)
}

// LinkStream is one OrchestratorLink stream as the orchestrator holds it.
type LinkStream interface {
	Send(*pb.OrchestratorLinkFrame) error
	Recv() (*pb.OrchestratorLinkFrame, error)
	// CloseSend ends the orchestrator's half; the server side then returns.
	CloseSend() error
}

// Remote is API over a dialed daemon's generated stub.
func Remote(api pb.SwitchboardClient) API { return remoteAPI{api: api} }

type remoteAPI struct{ api pb.SwitchboardClient }

func (r remoteAPI) ListSandboxes(ctx context.Context) ([]*pb.Sandbox, error) {
	resp, err := r.api.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetSandboxes(), nil
}

func (r remoteAPI) AttachController(ctx context.Context, req *pb.AttachControllerRequest) (*pb.Sandbox, error) {
	return r.api.AttachController(ctx, req)
}

func (r remoteAPI) DetachController(ctx context.Context, req *pb.DetachControllerRequest) (*pb.Sandbox, error) {
	return r.api.DetachController(ctx, req)
}

func (r remoteAPI) UpdateWorker(ctx context.Context, req *pb.UpdateWorkerRequest) (*pb.Sandbox, error) {
	return r.api.UpdateWorker(ctx, req)
}

func (r remoteAPI) DeliverWorkerPrompt(ctx context.Context, req *pb.DeliverWorkerPromptRequest) (*pb.DeliverWorkerPromptResponse, error) {
	return r.api.DeliverWorkerPrompt(ctx, req)
}

// DeliverWorkerStep maps the two codes the orchestrator acts on to sentinels:
// ALREADY_EXISTS to ErrStepSent, and UNIMPLEMENTED (an older daemon) to
// ErrUnsupported. Any other status passes through.
func (r remoteAPI) DeliverWorkerStep(ctx context.Context, req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
	resp, err := r.api.DeliverWorkerStep(ctx, req)
	switch status.Code(err) {
	case codes.AlreadyExists:
		return nil, fmt.Errorf("%w: %s", ErrStepSent, status.Convert(err).Message())
	case codes.Unimplemented:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, status.Convert(err).Message())
	}
	return resp, err
}

func (r remoteAPI) Subscribe(ctx context.Context) (EventStream, error) {
	return r.api.Subscribe(ctx, &pb.SubscribeRequest{ReplayUndelivered: false})
}

func (r remoteAPI) OpenLink(ctx context.Context) (LinkStream, error) {
	return r.api.OrchestratorLink(ctx)
}
