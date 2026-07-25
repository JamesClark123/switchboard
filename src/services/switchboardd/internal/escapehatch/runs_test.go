package escapehatch

import (
	"fmt"
	"sync"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestRunStoreAssignsSequentialIDs(t *testing.T) {
	s := NewRunStore()
	r1 := s.Create(&pb.EscapeHatchRun{SandboxId: "sb1", CommandName: "a"})
	r2 := s.Create(&pb.EscapeHatchRun{SandboxId: "sb1", CommandName: "b"})
	if r1.GetId() != "ehr-1" || r2.GetId() != "ehr-2" {
		t.Fatalf("ids not sequential: %q, %q", r1.GetId(), r2.GetId())
	}
}

func TestRunStoreCloneIsolation(t *testing.T) {
	s := NewRunStore()
	created := s.Create(&pb.EscapeHatchRun{SandboxId: "sb1", CommandName: "a"})
	created.CommandName = "mutated" // mutate the returned clone
	got, _ := s.Get(created.GetId())
	if got.GetCommandName() != "a" {
		t.Errorf("store must be isolated from caller mutation, got %q", got.GetCommandName())
	}
}

func TestRunStoreUpdate(t *testing.T) {
	s := NewRunStore()
	r := s.Create(&pb.EscapeHatchRun{SandboxId: "sb1", Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING})
	out, ok := s.Update(r.GetId(), func(run *pb.EscapeHatchRun) {
		run.Status = pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED
		run.ExitStatus = 0
	})
	if !ok || out.GetStatus() != pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED {
		t.Fatalf("update failed: ok=%v status=%v", ok, out.GetStatus())
	}
	if _, ok := s.Update("ehr-999", func(*pb.EscapeHatchRun) {}); ok {
		t.Error("update of unknown id should return ok=false")
	}
}

func TestRunStoreRingTrim(t *testing.T) {
	s := NewRunStore()
	var first string
	for i := 0; i < maxRuns+5; i++ {
		r := s.Create(&pb.EscapeHatchRun{SandboxId: "sb1"})
		if i == 0 {
			first = r.GetId()
		}
	}
	if _, ok := s.Get(first); ok {
		t.Error("oldest run should have been evicted past capacity")
	}
	if got := len(s.ListBySandbox("sb1")); got != maxRuns {
		t.Errorf("store should cap at %d, got %d", maxRuns, got)
	}
}

func TestRunStoreListFiltersBySandbox(t *testing.T) {
	s := NewRunStore()
	s.Create(&pb.EscapeHatchRun{SandboxId: "sb1"})
	s.Create(&pb.EscapeHatchRun{SandboxId: "sb2"})
	s.Create(&pb.EscapeHatchRun{SandboxId: "sb1"})
	if got := len(s.ListBySandbox("sb1")); got != 2 {
		t.Errorf("want 2 for sb1, got %d", got)
	}
	if got := len(s.ListBySandbox("")); got != 3 {
		t.Errorf("empty filter should return all, got %d", got)
	}
}

func TestRunStoreConcurrentAccess(t *testing.T) {
	s := NewRunStore()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r := s.Create(&pb.EscapeHatchRun{SandboxId: fmt.Sprintf("sb%d", n%3)})
			s.Update(r.GetId(), func(run *pb.EscapeHatchRun) {
				run.Status = pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED
			})
			s.ListBySandbox("")
		}(i)
	}
	wg.Wait()
}
