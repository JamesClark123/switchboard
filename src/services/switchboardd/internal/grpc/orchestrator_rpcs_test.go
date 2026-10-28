package grpc_test

import (
	"context"
	"errors"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/orchestrator"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Without the feature-009 services configured every new RPC answers UNAVAILABLE
// with one clear message (the generated interface is satisfied either way).
func TestOrchestratorRPCsUnavailableWithoutServices(t *testing.T) {
	client, _ := startServer(t)
	ctx := context.Background()
	calls := map[string]func() error{
		"ListOrchestrators":    func() error { _, e := client.ListOrchestrators(ctx, &pb.ListOrchestratorsRequest{}); return e },
		"GetOrchestrator":      func() error { _, e := client.GetOrchestrator(ctx, &pb.OrchestratorIdRequest{}); return e },
		"CreateOrchestrator":   func() error { _, e := client.CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{}); return e },
		"RenameOrchestrator":   func() error { _, e := client.RenameOrchestrator(ctx, &pb.RenameOrchestratorRequest{}); return e },
		"RemoveOrchestrator":   func() error { _, e := client.RemoveOrchestrator(ctx, &pb.OrchestratorIdRequest{}); return e },
		"SetOrchestratorHosts": func() error { _, e := client.SetOrchestratorHosts(ctx, &pb.SetOrchestratorHostsRequest{}); return e },
		"GetWorkerBundle":      func() error { _, e := client.GetWorkerBundle(ctx, &pb.GetWorkerBundleRequest{}); return e },
		"AttachWorker":         func() error { _, e := client.AttachWorker(ctx, &pb.AttachWorkerRequest{}); return e },
		"DetachWorker":         func() error { _, e := client.DetachWorker(ctx, &pb.DetachWorkerRequest{}); return e },
		"MarkWorkerFree":       func() error { _, e := client.MarkWorkerFree(ctx, &pb.MarkWorkerFreeRequest{}); return e },
		"PutAction":            func() error { _, e := client.PutAction(ctx, &pb.PutActionRequest{}); return e },
		"DeleteAction":         func() error { _, e := client.DeleteAction(ctx, &pb.DeleteActionRequest{}); return e },
		"PutTimedEvent":        func() error { _, e := client.PutTimedEvent(ctx, &pb.PutTimedEventRequest{}); return e },
		"DeleteTimedEvent":     func() error { _, e := client.DeleteTimedEvent(ctx, &pb.DeleteTimedEventRequest{}); return e },
		"FireTimedEvent":       func() error { _, e := client.FireTimedEvent(ctx, &pb.FireTimedEventRequest{}); return e },
		"CallAction":           func() error { _, e := client.CallAction(ctx, &pb.CallActionRequest{}); return e },
		"ListRuns":             func() error { _, e := client.ListRuns(ctx, &pb.ListRunsRequest{}); return e },
		"GetRun":               func() error { _, e := client.GetRun(ctx, &pb.GetRunRequest{}); return e },
		"CancelRun":            func() error { _, e := client.CancelRun(ctx, &pb.CancelRunRequest{}); return e },
		"RebuildReasoningSandbox": func() error {
			st, e := client.RebuildReasoningSandbox(ctx, &pb.RebuildReasoningSandboxRequest{})
			if e != nil {
				return e
			}
			_, e = st.Recv()
			return e
		},
		"AttachController":    func() error { _, e := client.AttachController(ctx, &pb.AttachControllerRequest{}); return e },
		"DetachController":    func() error { _, e := client.DetachController(ctx, &pb.DetachControllerRequest{}); return e },
		"UpdateWorker":        func() error { _, e := client.UpdateWorker(ctx, &pb.UpdateWorkerRequest{}); return e },
		"DeliverWorkerPrompt": func() error { _, e := client.DeliverWorkerPrompt(ctx, &pb.DeliverWorkerPromptRequest{}); return e },
		"PutLane":             func() error { _, e := client.PutLane(ctx, &pb.PutLaneRequest{}); return e },
		"DeleteLane":          func() error { _, e := client.DeleteLane(ctx, &pb.DeleteLaneRequest{}); return e },
		"SetWorkerLanes":      func() error { _, e := client.SetWorkerLanes(ctx, &pb.SetWorkerLanesRequest{}); return e },
		"SetLaneMembers":      func() error { _, e := client.SetLaneMembers(ctx, &pb.SetLaneMembersRequest{}); return e },
		"CheckLaneChange":     func() error { _, e := client.CheckLaneChange(ctx, &pb.CheckLaneChangeRequest{}); return e },
		"DismissDroppedWorkerTags": func() error {
			_, e := client.DismissDroppedWorkerTags(ctx, &pb.OrchestratorIdRequest{})
			return e
		},
		"DeliverWorkerStep": func() error { _, e := client.DeliverWorkerStep(ctx, &pb.DeliverWorkerStepRequest{}); return e },
		"OrchestratorLink": func() error {
			st, e := client.OrchestratorLink(ctx)
			if e != nil {
				return e
			}
			_, e = st.Recv()
			return e
		},
	}
	if len(calls) != 32 { // 24 from feature 009 + FireTimedEvent (SWI-4) + 6 lane RPCs (SWI-18) + DeliverWorkerStep (SWI-17)
		t.Fatalf("expected the 32 orchestrator RPCs, have %d", len(calls))
	}
	for name, call := range calls {
		err := call()
		if st, _ := status.FromError(err); st.Code() != codes.Unavailable || st.Message() != "orchestrator support not configured" {
			t.Errorf("%s without services = %v, want UNAVAILABLE 'orchestrator support not configured'", name, err)
		}
	}
}

func TestOrchStatusMapping(t *testing.T) {
	cases := map[error]codes.Code{
		nil:                              codes.OK,
		orchestrator.ErrNotFound:         codes.NotFound,
		registry.ErrNotFound:             codes.NotFound,
		registry.ErrOrchestratorNotFound: codes.NotFound,
		registry.ErrRunNotFound:          codes.NotFound,
		orchestrator.ErrNameTaken:        codes.AlreadyExists,
		&orchestrator.AlreadyControlledError{Name: "a"}:                 codes.AlreadyExists,
		orchestrator.ErrInvalid:                                         codes.InvalidArgument,
		orchestrator.ErrPrecondition:                                    codes.FailedPrecondition,
		&orchestrator.UnreachableError{Host: "h", Err: "ssh: no route"}: codes.Unavailable,
		orchestrator.ErrForbidden:                                       codes.PermissionDenied,
		errors.New("plain"):                                             codes.Internal,
		status.Error(codes.Aborted, "kept"):                             codes.Aborted,
	}
	for in, want := range cases {
		got := status.Code(sbgrpc.OrchStatusForTest(in))
		if got != want {
			t.Errorf("orchStatus(%v) = %v, want %v", in, got, want)
		}
	}
	if msg := sbgrpc.OrchStatusForTest(&orchestrator.AlreadyControlledError{Name: "review-bot"}).Error(); !contains(msg, "controlled by review-bot") {
		t.Errorf("ALREADY_EXISTS message = %q", msg)
	}
	if msg := sbgrpc.OrchStatusForTest(&orchestrator.UnreachableError{Host: "box", Err: "Connection refused"}).Error(); !contains(msg, "Connection refused") {
		t.Errorf("UNAVAILABLE message should carry ssh's words: %q", msg)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
