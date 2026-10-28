package worker

import (
	"context"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
)

// Local serves the worker side of THIS daemon to an orchestrator it hosts,
// in-process (research R2): the same API a remote host offers over its link,
// so the orchestrator's runtime never special-cases locality.
func Local(svc *Service, hub *agent.Hub) API { return localAPI{svc: svc, hub: hub} }

type localAPI struct {
	svc *Service
	hub *agent.Hub
}

func (l localAPI) ListSandboxes(context.Context) ([]*pb.Sandbox, error) { return l.svc.mgr.List() }

func (l localAPI) AttachController(ctx context.Context, req *pb.AttachControllerRequest) (*pb.Sandbox, error) {
	return l.svc.Attach(ctx, req)
}

func (l localAPI) DetachController(ctx context.Context, req *pb.DetachControllerRequest) (*pb.Sandbox, error) {
	return l.svc.Detach(ctx, req.GetSandboxId(), req.GetOrchestratorId(), req.GetForce())
}

func (l localAPI) UpdateWorker(ctx context.Context, req *pb.UpdateWorkerRequest) (*pb.Sandbox, error) {
	return l.svc.Update(ctx, req)
}

func (l localAPI) DeliverWorkerPrompt(ctx context.Context, req *pb.DeliverWorkerPromptRequest) (*pb.DeliverWorkerPromptResponse, error) {
	return l.svc.DeliverResponse(ctx, req)
}

func (l localAPI) DeliverWorkerStep(ctx context.Context, req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
	return l.svc.DeliverStep(ctx, req)
}

// hubStream adapts a hub subscription to an EventStream; the subscription ends
// with the context.
type hubStream struct {
	ch  <-chan *pb.Event
	ctx context.Context
}

func (h *hubStream) Recv() (*pb.Event, error) {
	select {
	case ev, ok := <-h.ch:
		if !ok {
			return nil, context.Canceled
		}
		return ev, nil
	case <-h.ctx.Done():
		return nil, h.ctx.Err()
	}
}

func (l localAPI) Subscribe(ctx context.Context) (EventStream, error) {
	id, ch, _ := l.hub.Subscribe(false)
	go func() {
		<-ctx.Done()
		l.hub.Unsubscribe(id)
	}()
	return &hubStream{ch: ch, ctx: ctx}, nil
}

// OpenLink opens an in-process link: the server half is accepted by the relay
// exactly as a remote stream would be.
func (l localAPI) OpenLink(ctx context.Context) (LinkStream, error) {
	client, server := newPipe(ctx)
	go func() { _ = l.svc.AcceptLink(server) }()
	return client, nil
}
