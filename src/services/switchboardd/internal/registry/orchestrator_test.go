package registry

import (
	"errors"
	"fmt"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestOrchestratorRoundTrip(t *testing.T) {
	r := openTemp(t)
	if _, err := r.GetOrchestrator("missing"); !errors.Is(err, ErrOrchestratorNotFound) {
		t.Fatalf("Get missing = %v", err)
	}
	if err := r.PutOrchestrator(&pb.Orchestrator{}); err == nil {
		t.Fatal("expected error putting an orchestrator with empty id")
	}
	a := &pb.Orchestrator{Id: "o-a", Name: "review-bot", HostId: "h1", CreatedAt: timestamppb.New(timeUnix(2)),
		Hosts: []*pb.OrchestratorHost{{HostId: "h2", SshTarget: "user@box"}}}
	b := &pb.Orchestrator{Id: "o-b", Name: "poller", HostId: "h1", CreatedAt: timestamppb.New(timeUnix(1))}
	for _, o := range []*pb.Orchestrator{a, b} {
		if err := r.PutOrchestrator(o); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.GetOrchestrator("o-a")
	if err != nil || got.GetName() != "review-bot" || got.GetHosts()[0].GetSshTarget() != "user@box" {
		t.Fatalf("Get = %v / %+v", err, got)
	}
	list, err := r.ListOrchestrators()
	if err != nil || len(list) != 2 || list[0].GetId() != "o-b" || list[1].GetId() != "o-a" {
		t.Fatalf("List = %v / %v", err, list)
	}
	upd, err := r.UpdateOrchestrator("o-a", func(o *pb.Orchestrator) error { o.Name = "review-bot-2"; return nil })
	if err != nil || upd.GetName() != "review-bot-2" {
		t.Fatalf("Update = %v / %q", err, upd.GetName())
	}
	if _, err := r.UpdateOrchestrator("nope", func(*pb.Orchestrator) error { return nil }); !errors.Is(err, ErrOrchestratorNotFound) {
		t.Errorf("Update missing = %v", err)
	}
	if _, err := r.UpdateOrchestrator("o-a", func(*pb.Orchestrator) error { return errors.New("refused") }); err == nil || err.Error() != "refused" {
		t.Errorf("Update mutate error should surface, got %v", err)
	}
	// A refused mutate leaves the record untouched.
	if got, _ := r.GetOrchestrator("o-a"); got.GetName() != "review-bot-2" {
		t.Errorf("name after refused mutate = %q", got.GetName())
	}
}

func TestRunsReceiptOrderAndSeq(t *testing.T) {
	r := openTemp(t)
	const orch = "11111111-2222-3333-4444-555555555555"
	var ids []string
	for i := 0; i < 25; i++ {
		seq, err := r.NextRunSeq(orch)
		if err != nil {
			t.Fatal(err)
		}
		if seq != uint64(i+1) {
			t.Fatalf("seq %d = %d", i, seq)
		}
		id := fmt.Sprintf("run-%s-%d", orch[:8], seq)
		ids = append(ids, id)
		if err := r.PutRun(&pb.Run{Id: id, OrchestratorId: orch, Action: fmt.Sprintf("a%d", i), State: pb.RunState_RUN_STATE_QUEUED}); err != nil {
			t.Fatal(err)
		}
	}
	// Another orchestrator's runs never leak into the scan.
	if _, err := r.NextRunSeq("other"); err != nil {
		t.Fatal(err)
	}
	if err := r.PutRun(&pb.Run{Id: "run-other-1", OrchestratorId: "other"}); err != nil {
		t.Fatal(err)
	}
	runs, err := r.ListRuns(orch)
	if err != nil || len(runs) != 25 {
		t.Fatalf("ListRuns = %v / %d", err, len(runs))
	}
	for i, run := range runs {
		if run.GetId() != ids[i] {
			t.Fatalf("run[%d] = %s, want %s (receipt order, zero-padded keys)", i, run.GetId(), ids[i])
		}
	}
	got, err := r.GetRun(orch, ids[9])
	if err != nil || got.GetAction() != "a9" {
		t.Fatalf("GetRun = %v / %q", err, got.GetAction())
	}
	if _, err := r.GetRun(orch, "run-xxxxxxxx-999"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("GetRun missing = %v", err)
	}
	// A key collision with a different id (same seq, different orchestrator8) is not that run.
	if _, err := r.GetRun(orch, "run-deadbeef-10"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("GetRun with a mismatched id = %v", err)
	}
	// Replace in place keeps its slot.
	if err := r.PutRun(&pb.Run{Id: ids[2], OrchestratorId: orch, Action: "a2", State: pb.RunState_RUN_STATE_COMPLETED}); err != nil {
		t.Fatal(err)
	}
	runs, _ = r.ListRuns(orch)
	if len(runs) != 25 || runs[2].GetState() != pb.RunState_RUN_STATE_COMPLETED {
		t.Errorf("replace moved or duplicated the run: %d / %v", len(runs), runs[2].GetState())
	}
	if err := r.DeleteRun(orch, ids[0]); err != nil {
		t.Fatal(err)
	}
	if runs, _ = r.ListRuns(orch); len(runs) != 24 || runs[0].GetId() != ids[1] {
		t.Errorf("after delete: %d runs, first %s", len(runs), runs[0].GetId())
	}
	// The sequence survives a reopen (monotonic across transactions and restarts).
	seq, _ := r.NextRunSeq(orch)
	if seq != 26 {
		t.Errorf("seq after 25 = %d", seq)
	}
	if err := r.PutRun(&pb.Run{Id: "bad", OrchestratorId: orch}); err == nil {
		t.Error("PutRun without a sequenced id should fail")
	}
	if RunSeq("run-abc-17") != 17 || RunSeq("nodash") != 0 || RunSeq("run-abc-x") != 0 {
		t.Error("RunSeq parsing")
	}
}

func TestDeleteOrchestratorClearsRunsAndTokens(t *testing.T) {
	r := openTemp(t)
	const orch = "aaaaaaaa-0000"
	if err := r.PutOrchestrator(&pb.Orchestrator{Id: orch, Name: "x"}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := r.NextRunSeq(orch); err != nil {
			t.Fatal(err)
		}
		if err := r.PutRun(&pb.Run{Id: fmt.Sprintf("run-aaaaaaaa-%d", i), OrchestratorId: orch}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.PutRun(&pb.Run{Id: "run-bbbbbbbb-1", OrchestratorId: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteOrchestrator(orch); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetOrchestrator(orch); !errors.Is(err, ErrOrchestratorNotFound) {
		t.Error("record should be gone")
	}
	if runs, _ := r.ListRuns(orch); len(runs) != 0 {
		t.Errorf("runs should be gone, got %d", len(runs))
	}
	if seq, _ := r.NextRunSeq(orch); seq != 1 {
		t.Errorf("seq counter should be cleared with the prefix, got %d", seq)
	}
	if runs, _ := r.ListRuns("other"); len(runs) != 1 {
		t.Errorf("other orchestrator's runs must survive, got %d", len(runs))
	}
	if err := r.DeleteOrchestrator("never-existed"); err != nil {
		t.Errorf("deleting a missing orchestrator should be nil, got %v", err)
	}

	// Worker tokens: a daemon-local bucket keyed by sandbox id.
	if _, err := r.GetWorkerToken("sb1"); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("GetWorkerToken missing = %v", err)
	}
	if err := r.PutWorkerToken("", "t"); err == nil {
		t.Error("PutWorkerToken needs both fields")
	}
	if err := r.PutWorkerToken("sb1", "deadbeef"); err != nil {
		t.Fatal(err)
	}
	if err := r.PutWorkerToken("sb2", "cafe"); err != nil {
		t.Fatal(err)
	}
	if tok, err := r.GetWorkerToken("sb1"); err != nil || tok != "deadbeef" {
		t.Errorf("GetWorkerToken = %q / %v", tok, err)
	}
	all, err := r.ListWorkerTokens()
	if err != nil || len(all) != 2 || all["sb2"] != "cafe" {
		t.Errorf("ListWorkerTokens = %v / %v", all, err)
	}
	if err := r.DeleteWorkerToken("sb1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetWorkerToken("sb1"); !errors.Is(err, ErrTokenNotFound) {
		t.Error("token should be revoked")
	}
	if err := r.DeleteWorkerToken("sb1"); err != nil {
		t.Errorf("deleting a missing token should be nil, got %v", err)
	}
	// The token never serializes into the sandbox record (I-C4).
	if err := r.Put(&pb.Sandbox{Id: "sb2", Controller: &pb.SandboxController{OrchestratorId: orch}}); err != nil {
		t.Fatal(err)
	}
	sb, _ := r.Get("sb2")
	if blob, _ := proto.Marshal(sb); contains(blob, "cafe") {
		t.Error("worker token must never appear in the Sandbox bytes")
	}
}

func contains(b []byte, s string) bool { return len(s) > 0 && string(b) != "" && indexOf(b, s) >= 0 }

func indexOf(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i
		}
	}
	return -1
}

func TestOrchestratorCorruptRecords(t *testing.T) {
	r := openTemp(t)
	if err := r.db.Update(func(tx *bolt.Tx) error {
		if e := tx.Bucket(orchestratorsBucket).Put([]byte("bad"), []byte("\xff\xff")); e != nil {
			return e
		}
		return tx.Bucket(runsBucket).Put(runKey("bad", 1), []byte("\xff\xff"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetOrchestrator("bad"); err == nil {
		t.Error("expected unmarshal error")
	}
	if _, err := r.ListOrchestrators(); err == nil {
		t.Error("expected List unmarshal error")
	}
	if _, err := r.UpdateOrchestrator("bad", func(*pb.Orchestrator) error { return nil }); err == nil {
		t.Error("expected Update unmarshal error")
	}
	if _, err := r.GetRun("bad", "run-bad-1"); err == nil {
		t.Error("expected GetRun unmarshal error")
	}
	if _, err := r.ListRuns("bad"); err == nil {
		t.Error("expected ListRuns unmarshal error")
	}
}
