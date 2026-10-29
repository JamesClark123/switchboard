package orchestrator

import (
	"errors"
	"fmt"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// Store is the registry surface the orchestrator side uses (research R9): the
// `orchestrators` and `orchestrator_runs` buckets. *registry.Registry
// implements it; the interface keeps the service testable against the real
// bbolt file without the sandbox buckets mattering.
type Store interface {
	PutOrchestrator(*pb.Orchestrator) error
	GetOrchestrator(id string) (*pb.Orchestrator, error)
	ListOrchestrators() ([]*pb.Orchestrator, error)
	UpdateOrchestrator(id string, mutate func(*pb.Orchestrator) error) (*pb.Orchestrator, error)
	DeleteOrchestrator(id string) error
	NextRunSeq(orchID string) (uint64, error)
	PutRun(*pb.Run) error
	GetRun(orchID, runID string) (*pb.Run, error)
	ListRuns(orchID string) ([]*pb.Run, error)
	DeleteRun(orchID, runID string) error
	// SWI-18 (research R7): the record and its runs in one transaction.
	UpdateOrchestratorRuns(id string, fn func(rec *pb.Orchestrator, runs registry.RunTx) error) (*pb.Orchestrator, error)
	ViewOrchestratorRuns(id string, fn func(rec *pb.Orchestrator, queued []*pb.Run) error) error
}

// RunTx is the registry's in-transaction run bucket.
type RunTx = registry.RunTx

// notFound maps the registry's absent-key sentinels onto ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, registry.ErrOrchestratorNotFound) {
		return fmt.Errorf("%w: orchestrator", ErrNotFound)
	}
	if errors.Is(err, registry.ErrRunNotFound) {
		return fmt.Errorf("%w: run", ErrNotFound)
	}
	return err
}

// finished reports whether a run is in a terminal state.
func finished(r *pb.Run) bool {
	switch r.GetState() {
	case pb.RunState_RUN_STATE_COMPLETED, pb.RunState_RUN_STATE_FAILED, pb.RunState_RUN_STATE_CANCELLED:
		return true
	}
	return false
}

// inFlight reports whether a run is RUNNING, AWAITING_INPUT or AWAITING_REVIEW.
func inFlight(r *pb.Run) bool {
	switch r.GetState() {
	case pb.RunState_RUN_STATE_RUNNING, pb.RunState_RUN_STATE_AWAITING_INPUT, pb.RunState_RUN_STATE_AWAITING_REVIEW:
		return true
	}
	return false
}

// trimHistory deletes the oldest finished runs beyond historyKeep (I-R4):
// queued and in-flight runs, and closed runs with step work left, are never
// trimmed.
func (s *Service) trimHistory(orchID string) error {
	runs, err := s.reg.ListRuns(orchID)
	if err != nil {
		return err
	}
	var done []*pb.Run
	for _, r := range runs {
		// A run still holding its sandbox, or halted, is kept (FR-213).
		if finished(r) && !stepActive(r) {
			done = append(done, r)
		}
	}
	for len(done) > historyKeep {
		if err := s.reg.DeleteRun(orchID, done[0].GetId()); err != nil {
			return err
		}
		done = done[1:]
	}
	return nil
}
