// Package agent owns the daemon's agent-facing concerns: the live event hub
// (sandbox-change + notification fan-out with replay), Claude Code hook injection
// and callback handling, and per-sandbox PTY sessions for prompting/attaching.
package agent

import (
	"fmt"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxBuffer bounds the retained notification history per daemon.
const maxBuffer = 512

// Hub fans out live Events (sandbox changes + notifications) to subscribers and
// buffers NotificationEvents so a client disconnected at emit time receives them
// on reconnect (FR-026b, SC-008).
type Hub struct {
	mu     sync.Mutex
	hostID string
	buffer []*bufferedNotification
	subs   map[int]chan *pb.Event
	nextID int
	seq    int
}

type bufferedNotification struct {
	ev        *pb.NotificationEvent
	delivered bool
}

// NewHub constructs a Hub for the given host.
func NewHub(hostID string) *Hub {
	return &Hub{hostID: hostID, subs: map[int]chan *pb.Event{}}
}

// Subscribe registers a subscriber. When replayUndelivered is set, the returned
// replay slice carries the currently-undelivered notifications (oldest first) so
// the caller can re-send missed events before streaming live ones (FR-026b).
func (h *Hub) Subscribe(replayUndelivered bool) (int, <-chan *pb.Event, []*pb.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.nextID
	h.nextID++
	ch := make(chan *pb.Event, 64)
	h.subs[id] = ch

	var replay []*pb.Event
	if replayUndelivered {
		for _, b := range h.buffer {
			if !b.delivered {
				replay = append(replay, &pb.Event{Event: &pb.Event_Notification{Notification: b.ev}})
			}
		}
	}
	return id, ch, replay
}

// Unsubscribe removes and closes a subscriber's channel.
func (h *Hub) Unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[id]; ok {
		close(ch)
		delete(h.subs, id)
	}
}

// fanout delivers ev to every subscriber, dropping it for any whose buffer is
// full (a slow client must not block the daemon).
func (h *Hub) fanout(ev *pb.Event) {
	for _, ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// PublishSandbox broadcasts a sandbox state/agent-status change.
func (h *Hub) PublishSandbox(sb *pb.Sandbox) {
	if sb == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fanout(&pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: sb}})
}

// PublishRemoved broadcasts a sandbox removal.
func (h *Hub) PublishRemoved(sandboxID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fanout(&pb.Event{Event: &pb.Event_Removed{Removed: &pb.Event_SandboxRemoved{SandboxId: sandboxID}}})
}

// EmitNotification buffers and broadcasts a NotificationEvent (FR-024/025). The
// event identifies the subject sandbox and this host (FR-026).
func (h *Hub) EmitNotification(sandboxID string, kind pb.NotificationKind, message string, now time.Time) *pb.NotificationEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	ev := &pb.NotificationEvent{
		Id:        fmt.Sprintf("evt-%d", h.seq),
		SandboxId: sandboxID,
		HostId:    h.hostID,
		Kind:      kind,
		Message:   message,
		CreatedAt: timestamppb.New(now),
	}
	h.buffer = append(h.buffer, &bufferedNotification{ev: ev})
	if len(h.buffer) > maxBuffer {
		h.buffer = h.buffer[len(h.buffer)-maxBuffer:]
	}
	h.fanout(&pb.Event{Event: &pb.Event_Notification{Notification: ev}})
	return ev
}

// Ack marks the given notification ids delivered and returns how many matched
// (FR-026b: acked events are not replayed on the next reconnect).
func (h *Hub) Ack(ids []string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	acked := 0
	for _, b := range h.buffer {
		if want[b.ev.GetId()] && !b.delivered {
			b.delivered = true
			acked++
		}
	}
	return acked
}

// Undelivered returns the currently-undelivered notifications (oldest first).
func (h *Hub) Undelivered() []*pb.NotificationEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*pb.NotificationEvent
	for _, b := range h.buffer {
		if !b.delivered {
			out = append(out, b.ev)
		}
	}
	return out
}
