package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func hello(id, name string) *pb.OrchestratorLinkFrame {
	return &pb.OrchestratorLinkFrame{Frame: &pb.OrchestratorLinkFrame_Hello_{Hello: &pb.OrchestratorLinkFrame_Hello{OrchestratorId: id, Name: name}}}
}

func TestRelayHelloRequiredAndCallResult(t *testing.T) {
	ix := newLinkIndex()
	// Not hello first ⇒ invalid.
	c, s := newPipe(context.Background())
	done := make(chan error, 1)
	go func() { done <- ix.AcceptLink(s) }()
	_ = c.Send(&pb.OrchestratorLinkFrame{Frame: &pb.OrchestratorLinkFrame_Ping_{Ping: &pb.OrchestratorLinkFrame_Ping{}}})
	if err := <-done; !errors.Is(err, ErrInvalid) {
		t.Fatalf("ping first = %v, want ErrInvalid", err)
	}
	// No link ⇒ documented unreachable text.
	if _, err := ix.Call(context.Background(), "o1", "review-bot", "sb", "orchestrator_list_actions", "{}"); !errors.Is(err, ErrLinkDown) || !strings.Contains(err.Error(), `orchestrator "review-bot" is unreachable right now`) {
		t.Fatalf("no link = %v", err)
	}

	// A proper link: hello, then call ↔ result matched by id; ping answered.
	c, s = newPipe(context.Background())
	go func() { _ = ix.AcceptLink(s) }()
	if err := c.Send(hello("o1", "review-bot")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !ix.Linked("o1") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !ix.Linked("o1") {
		t.Fatal("link should be indexed after hello")
	}
	go func() {
		for {
			fr, err := c.Recv()
			if err != nil {
				return
			}
			if call := fr.GetCall(); call != nil {
				_ = c.Send(&pb.OrchestratorLinkFrame{Frame: &pb.OrchestratorLinkFrame_Result_{Result: &pb.OrchestratorLinkFrame_Result{
					Id: call.GetId(), Ok: call.GetTool() != "fail", BodyJson: `{"echo":"` + call.GetSandboxId() + "/" + call.GetTool() + `"}`, Error: "refused: " + call.GetTool(),
				}}})
			}
		}
	}()
	body, err := ix.Call(context.Background(), "o1", "review-bot", "sb-1", "orchestrator_list_actions", "{}")
	if err != nil || body != `{"echo":"sb-1/orchestrator_list_actions"}` {
		t.Errorf("call = %q / %v", body, err)
	}
	if _, err := ix.Call(context.Background(), "o1", "review-bot", "sb-1", "fail", "{}"); err == nil || err.Error() != "refused: fail" {
		t.Errorf("refused call = %v", err)
	}
	_ = c.Send(&pb.OrchestratorLinkFrame{Frame: &pb.OrchestratorLinkFrame_Ping_{Ping: &pb.OrchestratorLinkFrame_Ping{}}})
	// The reply ping arrives on the client side (consumed by the reader above or here).
	// A second hello for the same orchestrator replaces the first stream.
	c2, s2 := newPipe(context.Background())
	go func() { _ = ix.AcceptLink(s2) }()
	_ = c2.Send(hello("o1", "review-bot"))
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ix.mu.Lock()
		l := ix.links["o1"]
		ix.mu.Unlock()
		if l != nil && l.stream == s2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ix.mu.Lock()
	cur := ix.links["o1"]
	ix.mu.Unlock()
	if cur == nil || cur.stream != s2 {
		t.Fatal("the newer hello should replace the older stream")
	}
	// Closing the orchestrator's half ends the link.
	_ = c2.CloseSend()
	deadline = time.Now().Add(2 * time.Second)
	for ix.Linked("o1") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ix.Linked("o1") {
		t.Error("a closed stream should leave the index")
	}
}

func TestRelayCallTimesOutAndCancels(t *testing.T) {
	ix := newLinkIndex()
	c, s := newPipe(context.Background())
	go func() { _ = ix.AcceptLink(s) }()
	_ = c.Send(hello("o2", "slow"))
	deadline := time.Now().Add(2 * time.Second)
	for !ix.Linked("o2") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	go func() {
		for {
			if _, err := c.Recv(); err != nil {
				return
			} // never answers
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := ix.Call(ctx, "o2", "slow", "sb", "t", "{}"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cancelled call = %v", err)
	}
	// Dropping the stream while a call waits answers it with link down.
	errc := make(chan error, 1)
	go func() { _, err := ix.Call(context.Background(), "o2", "slow", "sb", "t", "{}"); errc <- err }()
	time.Sleep(20 * time.Millisecond)
	_ = c.CloseSend()
	if err := <-errc; !errors.Is(err, ErrLinkDown) {
		t.Errorf("call on a dropped link = %v", err)
	}
}
