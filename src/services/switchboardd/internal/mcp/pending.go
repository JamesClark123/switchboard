package mcp

import (
	"context"
	"log"
	"strings"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/libs/switchboard-proto/mcprules"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Recovery backoff (research R2 Recovery); variables so tests shrink them.
var (
	recoverBackoff    = 5 * time.Second
	recoverBackoffMax = 5 * time.Minute
)

// testHookRecover runs in recoverName right after the record re-read, outside
// any m.mu/stateMu hold (nil in production).
var testHookRecover func()

// CancelPending drops a pending replace (FR-406, FR-426, research R8): under
// the lock, best-effort removal of every removal-failed secret (re-checked),
// then pending, mark and definition go. It is never refused because a removal
// failed; the response names what is still on the host or unconfirmed.
func (m *Manager) CancelPending(ctx context.Context, name string, revision uint64) (*pb.CancelMcpPendingReplaceResponse, error) {
	name = strings.TrimSpace(name)
	if !SafeArg(name) {
		return nil, ErrInvalidArgs
	}
	_, release, err := m.lockName(ctx, name, lockOpts{})
	if err != nil {
		return nil, err
	}
	defer release()
	if err := m.refuseRecord(name, false); err != nil {
		return nil, err
	}
	settings, err := m.reg.GetMcpSettings()
	if err != nil {
		return nil, err
	}
	p := settings.GetPendingReplaces()[name]
	if p == nil || revision != 0 && p.GetRevision() != revision {
		return nil, refuse(pb.McpSaveRefusal_PENDING_ENDED, "the pending replace of %s has ended or changed", name)
	}
	resp := &pb.CancelMcpPendingReplaceResponse{}
	removed := false
	for _, n := range p.GetRemovalFailedSecrets() {
		if m.secrets == nil {
			resp.Unconfirmed = append(resp.Unconfirmed, n)
			continue
		}
		removed = true
		if ok, msg := m.removeCheckedOut(n); !ok {
			if strings.Contains(msg, "; ") {
				resp.Unconfirmed = append(resp.Unconfirmed, n)
			} else {
				resp.LeftOnHost = append(resp.LeftOnHost, n)
			}
		}
	}
	settings, err = m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		delete(s.PendingReplaces, name)
		delete(s.Definitions, name)
		delete(s.Marks, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.setPending(name, false)
	m.replaceEnded(name, false)
	if removed {
		m.secrets.EmitChanged()
	}
	m.emitOutcome(nil, settings)
	return resp, nil
}

// recoverLoop resolves start-up leftovers (research R2 Recovery): each attempt
// runs one `mcp ls`, then resolves every queued name under its lock; a failed
// list or write keeps names queued and retries with backoff.
func (m *Manager) recoverLoop(ctx context.Context) {
	queue := append([]string(nil), m.recoverQueue...)
	backoff := recoverBackoff
	for len(queue) > 0 {
		lctx, cancel := context.WithTimeout(ctx, time.Duration(listTimeout))
		rows, err := m.listRows(lctx)
		cancel()
		if err == nil {
			present := map[string]bool{}
			for _, r := range rows {
				present[r.name] = true
			}
			var left []string
			for _, name := range queue {
				if err := m.recoverName(ctx, name, present[name]); err != nil {
					log.Printf("mcp: %s: recover interrupted save: %v (retrying)", name, err)
					left = append(left, name)
				}
			}
			queue = left
			if len(queue) == 0 {
				return
			}
		} else {
			log.Printf("mcp: recover interrupted saves: %v (retrying)", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, recoverBackoffMax)
	}
}

// recoverName resolves one leftover in-progress record under its lock, which it
// releases on return (so no name's lock outlives its resolution).
func (m *Manager) recoverName(ctx context.Context, name string, listed bool) error {
	_, release, err := m.lockName(ctx, name, lockOpts{})
	if err != nil {
		return err
	}
	defer release()
	settings, err := m.reg.GetMcpSettings()
	if err != nil {
		return err
	}
	if settings.GetSavesInProgress()[name] == nil {
		m.clearRecovering(name, settings.GetPendingReplaces()[name] != nil)
		return nil
	}
	if testHookRecover != nil {
		testHookRecover()
	}
	var result string
	var removeNames []string
	ended := false
	settings, err = m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		rec := s.GetSavesInProgress()[name]
		removeNames, ended, result = nil, false, ""
		if rec == nil {
			return nil
		}
		delete(s.SavesInProgress, name)
		if s.Definitions == nil {
			s.Definitions = map[string]*pb.McpServerDefinition{}
		}
		if s.PendingReplaces == nil {
			s.PendingReplaces = map[string]*pb.McpPendingReplace{}
		}
		lost := ""
		if len(rec.GetPlannedOverwrittenSecrets()) > 0 {
			lost = "; overwritten secrets cannot be restored: " + strings.Join(rec.GetPlannedOverwrittenSecrets(), ", ")
		}
		replace := !rec.GetRetry() && rec.GetPrevious() != nil
		switch {
		case !listed && rec.GetRetry():
			p := s.PendingReplaces[name]
			if p == nil {
				p = &pb.McpPendingReplace{Name: name, AttachByDefault: rec.GetAttachByDefault(), Previous: rec.GetPrevious()}
				s.PendingReplaces[name] = p
			}
			p.Definition = rec.GetDefinition()
			p.Error = "daemon stopped during the retry" + lost
			p.RemovalFailedSecrets = appendNew(p.RemovalFailedSecrets, rec.GetPlannedCreatedSecrets()...)
			p.AddStarted, p.FailedAt = true, timestamppb.Now()
			p.Revision++
			result = "pending replace kept"
		case !listed && replace:
			s.PendingReplaces[name] = &pb.McpPendingReplace{
				Name: name, AttachByDefault: rec.GetAttachByDefault(), Definition: rec.GetDefinition(), Previous: rec.GetPrevious(),
				RemovalFailedSecrets: rec.GetPlannedCreatedSecrets(), AddStarted: true,
				Error: "daemon stopped during the save" + lost, FailedAt: timestamppb.Now(), Revision: 1,
			}
			delete(s.Marks, name)
			s.Definitions[name] = proto.Clone(rec.GetPrevious()).(*pb.McpServerDefinition)
			result = "pending replace"
		case !listed:
			removeNames = rec.GetPlannedCreatedSecrets()
			result = "new server not registered"
		case replace:
			// The host may hold either definition; the next Edit flags any difference.
			prev := proto.Clone(rec.GetPrevious()).(*pb.McpServerDefinition)
			if mcprules.ClientChanged(rec.GetPrevious(), rec.GetDefinition()) {
				prev.RebindPending = true
			}
			s.Definitions[name] = prev
			ended = true
			result = "registered; definition kept"
		default:
			def := proto.Clone(rec.GetDefinition()).(*pb.McpServerDefinition)
			if mcprules.ClientChanged(rec.GetPrevious(), rec.GetDefinition()) {
				def.RebindPending = true
			}
			s.Definitions[name] = def
			setMarkIn(s, name, rec.GetAttachByDefault())
			if rec.GetRetry() {
				delete(s.PendingReplaces, name)
				ended = true
			}
			result = "registered"
		}
		return nil
	})
	if err != nil {
		return err
	}
	m.clearRecovering(name, settings.GetPendingReplaces()[name] != nil)
	if ended {
		m.replaceEnded(name, true)
	}
	if m.secrets != nil {
		for _, n := range removeNames {
			if !m.removeChecked(n) {
				log.Printf("mcp: %s: recovery could not remove secret %s", name, n)
			}
		}
		if len(removeNames) > 0 {
			m.secrets.EmitChanged()
		}
	}
	log.Printf("mcp: %s: recovered interrupted save (%s)", name, result)
	m.emitOutcome(nil, settings)
	return nil
}

// clearRecovering updates the state map once a leftover is resolved.
func (m *Manager) clearRecovering(name string, pending bool) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	s := m.st(name)
	s.recovering, s.pending = false, pending
}

// appendNew appends the names not already in list.
func appendNew(list []string, names ...string) []string {
	seen := map[string]bool{}
	for _, n := range list {
		seen[n] = true
	}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			list = append(list, n)
		}
	}
	return list
}
