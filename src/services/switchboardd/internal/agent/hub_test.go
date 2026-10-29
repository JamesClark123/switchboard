package agent

import (
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func drain(ch <-chan *pb.Event) (events, markers int) {
	for {
		select {
		case ev := <-ch:
			if ev.GetEventsDropped() != nil {
				markers++
			} else {
				events++
			}
		default:
			return events, markers
		}
	}
}

// A stalled subscriber gets exactly one marker; a reading one is unaffected;
// Publish never blocks; after draining, the next overflow gets a new marker.
func TestHubOverflowMarker(t *testing.T) {
	h := NewHub("host")
	stalledID, stalled, _ := h.Subscribe(false)
	_, reader, _ := h.Subscribe(false)
	defer h.Unsubscribe(stalledID)

	got := 0
	for i := 0; i < 3*subBuffer; i++ {
		h.PublishRemoved("sb") // never blocks, even with the stalled subscriber full
		n, m := drain(reader)
		got += n
		if m != 0 {
			t.Fatalf("reading subscriber got a marker")
		}
	}
	if got != 3*subBuffer {
		t.Fatalf("reading subscriber got %d events, want %d", got, 3*subBuffer)
	}
	events, markers := drain(stalled)
	if events != subBuffer-1 || markers != 1 {
		t.Fatalf("stalled: events=%d markers=%d, want %d and 1", events, markers, subBuffer-1)
	}

	// Drained: the next fan-out clears the flag and delivers again.
	h.PublishRemoved("sb")
	if events, markers := drain(stalled); events != 1 || markers != 0 {
		t.Fatalf("after drain: events=%d markers=%d", events, markers)
	}
	for i := 0; i < 2*subBuffer; i++ {
		h.PublishRemoved("sb")
	}
	if _, markers := drain(stalled); markers != 1 {
		t.Fatalf("second overflow: markers=%d, want 1", markers)
	}
}

// While a marker is pending in a partly drained channel, events stay dropped
// until the channel is empty.
func TestHubOverflowStaysUntilEmpty(t *testing.T) {
	h := NewHub("host")
	_, ch, _ := h.Subscribe(false)
	for i := 0; i < subBuffer+5; i++ {
		h.PublishRemoved("sb")
	}
	<-ch // read one: the channel is no longer full, but not empty
	h.PublishRemoved("sb")
	events, markers := drain(ch)
	if events != subBuffer-2 || markers != 1 {
		t.Fatalf("events=%d markers=%d, want %d and 1", events, markers, subBuffer-2)
	}
}
