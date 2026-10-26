package ui

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Jobs data (feature 010, research R2/R3, FR-004/FR-005, SC-007). Each host is
// loaded by its own command (hosts in parallel, each under its own deadline,
// at most jobsRunsParallel ListRuns at a time within one) and applied as it
// answers. Event.orchestrator_changed reloads just that orchestrator. Every
// call is stamped from the process-wide jobsSeq as it is issued, inside the
// command, and an orchestrator's state is replaced only by a part stamped newer
// than the one applied (its appliedSeq) — so an older reply, such as a slow host
// load's part, never overwrites a newer state. At most one event reload per
// orchestrator (and one load per host) is in flight; events meanwhile mark it
// dirty and cost one more reload when it lands. Nothing is fetched while Jobs is
// not on screen: events mark orchestrators stale and a connecting host marks
// itself, and Jobs catches up when it is shown again.
//
// A host whose link died stays HostConnected in the client (only a failed dial
// records an error), so Jobs marks it lost when its event stream ends or a Jobs
// call to it fails with Unavailable; its rows stay, dimmed, and refuse
// operations until a load of it succeeds again (R3, R12).

// jobsRunsParallel bounds a host load's concurrent ListRuns calls (R2).
const jobsRunsParallel = 4

// jobsSeq stamps every Jobs GetOrchestrator, ListRuns and ListOrchestrators call
// as it is issued (R3). Commands only take stamps; the model compares them.
var jobsSeq atomic.Uint64

// jobsHostLoad is one host's load bookkeeping.
type jobsHostLoad struct {
	flight   uint64 // token of the load in flight (which reply ends it; not an order)
	listSeq  uint64 // stamp of the last applied ListOrchestrators
	loading  bool   // a load is in flight
	answered bool   // a load has answered (until then the host shows as loading)
	dirty    bool   // a reload was asked for while one was in flight
	stale    bool   // it connected while Jobs was not on screen
	err      error  // its orchestrators could not be listed
	lost     error  // its event stream ended or a call failed Unavailable
}

// jobsOrchMeta is one orchestrator's load bookkeeping. It outlives a removed
// orchestrator, so an older reply cannot bring it back.
type jobsOrchMeta struct {
	host, id   string
	appliedSeq uint64 // stamp of the newest part applied
	flight     uint64 // token of the event reload in flight
	inFlight   bool
	dirty      bool // changed while a reload was in flight
	stale      bool // changed while Jobs was not on screen
}

// jobsHostMsg carries one host load; jobsOrchMsg one orchestrator reload (gone
// when it no longer exists). seq/listSeq are the jobsSeq stamps of the calls.
type jobsHostMsg struct {
	host    string
	flight  uint64
	listSeq uint64
	parts   []jobsPart
	err     error
}

// jobsPart is one orchestrator of a host load, stamped by its ListRuns.
type jobsPart struct {
	orch jobsOrch
	seq  uint64
}

type jobsOrchMsg struct {
	host, id string
	flight   uint64
	seq      uint64
	orch     *jobsOrch
	gone     bool
	err      error
}

// ensure makes the bookkeeping maps (a zero jobsState is valid).
func (j *jobsState) ensure() {
	if j.hosts == nil {
		j.hosts = map[string]*jobsHostLoad{}
	}
	if j.meta == nil {
		j.meta = map[string]*jobsOrchMeta{}
	}
	if j.connected == nil {
		j.connected = map[string]bool{}
	}
}

func (j *jobsState) hostLoad(host string) *jobsHostLoad {
	j.ensure()
	hs, ok := j.hosts[host]
	if !ok {
		hs = &jobsHostLoad{}
		j.hosts[host] = hs
	}
	return hs
}

func (j *jobsState) metaOf(host, id string) *jobsOrchMeta {
	j.ensure()
	key := host + "/" + id
	md, ok := j.meta[key]
	if !ok {
		md = &jobsOrchMeta{host: host, id: id}
		j.meta[key] = md
	}
	return md
}

// jobsVisible reports whether Jobs is on screen, including a form, confirm,
// overlay or settings opened from it (R3).
func (m Model) jobsVisible() bool { return m.topLevel() == screenJobs }

// jobsReachable reports whether a host's rows can be acted on now.
func (m Model) jobsReachable(host string) bool {
	_, err := m.connectedDaemon(host)
	return err == nil
}

// enterJobs switches to the screen. The first visit loads every connected host;
// later visits reload only what changed while it was not shown.
func (m Model) enterJobs(from screen) (tea.Model, tea.Cmd) {
	_ = from // esc always returns to Sandboxes (R8)
	m.screen = screenJobs
	if !m.jobs.started {
		m.jobs.started = true
		return m, m.jobsLoadAll()
	}
	var cmds []tea.Cmd
	for _, h := range m.orchHosts() {
		if hs := m.jobs.hosts[h.id]; h.d != nil && (hs == nil || hs.stale || (!hs.answered && !hs.loading)) {
			cmds = append(cmds, m.jobsLoadHost(h, false))
		}
	}
	for _, md := range m.jobsStaleMeta() {
		cmds = append(cmds, m.jobsReloadOrch(md.host, md.id))
	}
	return m, tea.Batch(cmds...)
}

// jobsStaleMeta lists the stale orchestrators in a stable order.
func (m Model) jobsStaleMeta() []*jobsOrchMeta {
	var out []*jobsOrchMeta
	for _, md := range m.jobs.meta {
		if md.stale {
			out = append(out, md)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].host+"/"+out[a].id < out[b].host+"/"+out[b].id })
	return out
}

// jobsLoadAll loads every connected host (entering Jobs the first time, `r`);
// a host whose load is still running is left to it (R2).
func (m *Model) jobsLoadAll() tea.Cmd {
	m.jobs.ensure()
	var cmds []tea.Cmd
	for _, h := range m.orchHosts() {
		if h.d != nil {
			m.jobs.connected[h.id] = true
			cmds = append(cmds, m.jobsLoadHost(h, false))
		}
	}
	return tea.Batch(cmds...)
}

// jobsLoadHost starts one host's load. While one is in flight a second is not
// stacked: with coalesce it is asked for once that one lands, otherwise dropped.
func (m *Model) jobsLoadHost(h orchHost, coalesce bool) tea.Cmd {
	j := &m.jobs
	hs := j.hostLoad(h.id)
	if h.d == nil {
		return nil
	}
	if hs.loading {
		hs.dirty = hs.dirty || coalesce
		return nil
	}
	j.flights++
	hs.flight, hs.loading, hs.stale = j.flights, true, false
	flight := j.flights
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		listSeq, parts, err := loadJobsHost(ctx, h)
		return jobsHostMsg{host: h.id, flight: flight, listSeq: listSeq, parts: parts, err: err}
	}
}

// loadJobsHost lists one host's orchestrators and reads their runs, at most
// jobsRunsParallel at a time, stamping each call as it is issued. An
// orchestrator whose runs cannot be read is still listed, with the error (its
// actions and events are known).
func loadJobsHost(ctx context.Context, h orchHost) (uint64, []jobsPart, error) {
	listSeq := jobsSeq.Add(1)
	list, err := h.d.ListOrchestrators(ctx)
	if err != nil {
		return listSeq, nil, err
	}
	out := make([]jobsPart, len(list))
	sem := make(chan struct{}, jobsRunsParallel)
	var wg sync.WaitGroup
	for i, s := range list {
		wg.Add(1)
		go func(i int, s *pb.OrchestratorSummary) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			o := s.GetOrchestrator()
			seq := jobsSeq.Add(1)
			runs, err := h.d.ListRuns(ctx, o.GetId())
			if runs == nil {
				runs = &pb.ListRunsResponse{}
			}
			out[i] = jobsPart{orch: jobsOrch{target: targetOf(h.id, h.name, o), summary: s, runs: runs, err: err}, seq: seq}
		}(i, s)
	}
	wg.Wait()
	return listSeq, out, nil
}

// jobsReloadOrch asks for one orchestrator's reload after it changed (R3): stale
// while Jobs is not shown, dirty while its own reload is in flight, otherwise a
// GetOrchestrator + ListRuns stamped from jobsSeq. A host load running meanwhile
// costs at most one extra ListRuns; the stamps decide which state is kept.
// Nothing is asked of a host that is not connected (its reconnect loads it).
func (m *Model) jobsReloadOrch(host, id string) tea.Cmd {
	if id == "" {
		return nil
	}
	j := &m.jobs
	md := j.metaOf(host, id)
	if !m.jobsVisible() {
		md.stale = true
		return nil
	}
	if md.inFlight {
		md.dirty, md.stale = true, false
		return nil
	}
	d, err := m.connectedDaemon(host)
	if err != nil {
		return nil
	}
	hostName := m.hostDisplayName(host)
	if o, ok := m.jobsOrchByKey(host + "/" + id); ok {
		hostName = o.target.hostName
	}
	j.flights++
	md.flight, md.inFlight, md.stale, md.dirty = j.flights, true, false, false
	flight := j.flights
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		seq := jobsSeq.Add(1)
		det, err := d.GetOrchestrator(ctx, id)
		if err != nil {
			return jobsOrchMsg{host: host, id: id, flight: flight, seq: seq, gone: status.Code(err) == codes.NotFound, err: err}
		}
		runs, rerr := d.ListRuns(ctx, id)
		if status.Code(rerr) == codes.NotFound {
			return jobsOrchMsg{host: host, id: id, flight: flight, seq: seq, gone: true, err: rerr}
		}
		s := det.GetSummary()
		return jobsOrchMsg{host: host, id: id, flight: flight, seq: seq, orch: &jobsOrch{target: targetOf(host, hostName, s.GetOrchestrator()), summary: s, runs: runs, err: rerr}}
	}
}

// jobsAfterChange follows an operation's success: the orchestrator it acted on
// reloads; one without a target (create, rename, remove) reloads every host —
// both only while Jobs is shown, else when it is shown again.
func (m *Model) jobsAfterChange(t orchTarget) tea.Cmd {
	if t.id != "" {
		return m.jobsReloadOrch(t.host, t.id)
	}
	if m.jobsVisible() {
		return m.jobsLoadAll()
	}
	for _, h := range m.orchHosts() {
		if h.d != nil {
			m.jobs.hostLoad(h.id).stale = true
		}
	}
	return nil
}

// putOrch adds or replaces one orchestrator.
func (j *jobsState) putOrch(o jobsOrch) {
	for i := range j.orchs {
		if j.orchs[i].target.key() == o.target.key() {
			j.orchs[i] = o
			return
		}
	}
	j.orchs = append(j.orchs, o)
}

// dropOrchs removes the orchestrators drop selects. Their bookkeeping stays, so
// an older reply cannot bring them back (dropHost forgets it).
func (j *jobsState) dropOrchs(drop func(jobsOrch) bool) {
	kept := j.orchs[:0:0]
	for _, o := range j.orchs {
		if !drop(o) {
			kept = append(kept, o)
		}
	}
	j.orchs = kept
}

// dropHost forgets a host: its rows, its loads (a reply still in flight is
// then ignored) and its orchestrators' bookkeeping.
func (j *jobsState) dropHost(host string) {
	j.dropOrchs(func(o jobsOrch) bool { return o.target.host == host })
	for k, md := range j.meta {
		if md.host == host {
			delete(j.meta, k)
		}
	}
	delete(j.hosts, host)
}

func (m Model) applyJobsHost(msg jobsHostMsg) (tea.Model, tea.Cmd) {
	j := &m.jobs
	hs := j.hosts[msg.host]
	if hs == nil || msg.flight != hs.flight {
		return m, nil // the host was dropped (or reconnected and reloaded) meanwhile
	}
	hs.loading, hs.answered, hs.err = false, true, msg.err
	if msg.err == nil {
		hs.lost = nil // it answered: the link works again
		if msg.listSeq > hs.listSeq {
			hs.listSeq = msg.listSeq
			listed := map[string]bool{}
			for _, p := range msg.parts {
				listed[p.orch.target.key()] = true
			}
			// Missing from a list newer than anything applied for them: removed.
			j.dropOrchs(func(o jobsOrch) bool {
				if o.target.host != msg.host || listed[o.target.key()] {
					return false
				}
				md := j.metaOf(o.target.host, o.target.id)
				if md.appliedSeq >= msg.listSeq {
					return false
				}
				md.appliedSeq = msg.listSeq
				return true
			})
		}
		for _, p := range msg.parts {
			md := j.metaOf(p.orch.target.host, p.orch.target.id)
			if p.seq <= md.appliedSeq {
				continue // a newer state of it was already applied
			}
			md.appliedSeq, md.stale = p.seq, false
			j.putOrch(p.orch)
			m.jobsNoteLost(msg.host, p.orch.err)
		}
	} else {
		m.jobsNoteLost(msg.host, msg.err)
	}
	var cmd tea.Cmd
	if hs.dirty {
		hs.dirty = false
		for _, h := range m.orchHosts() {
			if h.id == msg.host {
				cmd = m.jobsLoadHost(h, false)
			}
		}
	}
	m.jobsChanged()
	return m, cmd
}

// jobsNoteLost marks a host lost when a Jobs call to it failed with Unavailable:
// the client still calls a dead link connected (R3).
func (m *Model) jobsNoteLost(host string, err error) {
	if status.Code(err) == codes.Unavailable {
		m.jobs.hostLoad(host).lost = err
	}
}

// jobsStreamEnded marks a connected host lost when its event stream ended
// (eventErrMsg). A host the developer disconnected is not connected any more,
// so it is not marked.
func (m *Model) jobsStreamEnded(host string, err error) {
	for _, h := range m.orchHosts() {
		if h.id == host && h.d != nil {
			m.jobs.hostLoad(host).lost = err
			m.jobsChanged()
		}
	}
}

// jobsResubscribed clears a lost host once its event stream opens again and
// reads it afresh (now if Jobs is shown, else when it is).
func (m *Model) jobsResubscribed(host string) tea.Cmd {
	hs := m.jobs.hosts[host]
	if hs == nil || hs.lost == nil {
		return nil
	}
	hs.lost = nil
	m.jobsChanged()
	if !m.jobs.started {
		return nil
	}
	if !m.jobsVisible() {
		hs.stale = true
		return nil
	}
	for _, h := range m.orchHosts() {
		if h.id == host {
			return m.jobsLoadHost(h, true)
		}
	}
	return nil
}

func (m Model) applyJobsOrch(msg jobsOrchMsg) (tea.Model, tea.Cmd) {
	j := &m.jobs
	key := msg.host + "/" + msg.id
	md := j.meta[key]
	if md == nil {
		return m, nil // the host was dropped meanwhile
	}
	if msg.flight == md.flight {
		md.inFlight = false
	}
	if msg.seq > md.appliedSeq {
		md.appliedSeq = msg.seq
		switch {
		case msg.gone:
			// NotFound: the orchestrator was removed (R3).
			j.dropOrchs(func(o jobsOrch) bool { return o.target.key() == key })
		case msg.orch == nil:
			// It could not be read: keep what was last read, with the error.
			for i := range j.orchs {
				if j.orchs[i].target.key() == key {
					j.orchs[i].err = msg.err
				}
			}
			m.jobsNoteLost(msg.host, msg.err)
		default:
			o := *msg.orch
			if o.runs == nil {
				// Its runs could not be read: keep the last ones read.
				o.runs = &pb.ListRunsResponse{}
				if prev, ok := m.jobsOrchByKey(key); ok {
					o.runs = prev.runs
				}
			}
			j.putOrch(o)
			m.jobsNoteLost(msg.host, o.err)
		}
		m.jobsChanged()
	}
	if md.dirty && !md.inFlight {
		md.dirty = false
		return m, m.jobsReloadOrch(md.host, md.id)
	}
	return m, nil
}

// jobsSyncHosts follows the host list (a host snapshot arrived), whether or not
// Jobs has finished loading (R3):
//   - a host that connected is loaded now if Jobs is shown and has started,
//     else marked to load when it is shown;
//   - a host whose connection failed or is (re)connecting keeps its rows, which
//     dim and refuse operations (they are named on the "could not read" line);
//   - a host the developer disconnected, or removed, is dropped.
func (m *Model) jobsSyncHosts() tea.Cmd {
	j := &m.jobs
	j.ensure()
	seen := map[string]bool{}
	changed := false
	var cmds []tea.Cmd
	for _, h := range m.orchHosts() {
		seen[h.id] = true
		switch {
		case h.d != nil:
			if j.connected[h.id] {
				continue
			}
			j.connected[h.id] = true
			changed = true
			if hs := j.hosts[h.id]; hs != nil {
				hs.lost = nil // a new link
			}
			if !j.started {
				continue
			}
			if m.jobsVisible() {
				cmds = append(cmds, m.jobsLoadHost(h, true))
			} else {
				j.hostLoad(h.id).stale = true
			}
		case h.state == client.HostDisconnected && h.err == nil:
			if j.connected[h.id] || j.hasHost(h.id) {
				changed = true
			}
			delete(j.connected, h.id)
			j.dropHost(h.id)
		default:
			changed = changed || j.connected[h.id]
			j.connected[h.id] = false
		}
	}
	for _, o := range j.orchs {
		if !seen[o.target.host] {
			j.dropHost(o.target.host)
			changed = true
		}
	}
	if changed {
		m.jobsChanged()
	}
	return tea.Batch(cmds...)
}

// hasHost reports whether any row or load belongs to a host.
func (j *jobsState) hasHost(host string) bool {
	if _, ok := j.hosts[host]; ok {
		return true
	}
	for _, o := range j.orchs {
		if o.target.host == host {
			return true
		}
	}
	return false
}

// jobsChanged re-derives the rows after any data change and keeps every cursor
// on its item (R4, R11).
func (m *Model) jobsChanged() {
	j := &m.jobs
	j.sortJobs()
	j.rev++
	j.derived = deriveJobs(j.orchs, m.jobs.rev)
	m.clampQueueFilter()
	m.anchorJobsCursors()
}

// sortJobs orders the orchestrators by host display name, then name, ties by
// host entry id then id (a stable group and filter order, FR-019, FR-015).
func (j *jobsState) sortJobs() {
	sort.SliceStable(j.orchs, func(a, b int) bool {
		x, y := j.orchs[a].target, j.orchs[b].target
		if x.hostName != y.hostName {
			return x.hostName < y.hostName
		}
		if x.name != y.name {
			return x.name < y.name
		}
		return x.key() < y.key()
	})
}

// jobsUnreadable is the "could not read" line's entries (FR-004): hosts whose
// connection failed or was lost, or whose listing failed, each with its error,
// then orchestrators whose runs could not be read, with theirs. Errors are
// sanitised single lines (FR-029). Hosts the developer disconnected or never
// connected are not named, and connecting hosts are on the connecting line.
func (m Model) jobsUnreadable() []string {
	var hosts, orchs []string
	for _, h := range m.orchHosts() {
		hs := m.jobs.hosts[h.id]
		var why error
		switch {
		case h.d == nil && h.state != client.HostConnecting:
			why = h.err
		case h.d != nil && hs != nil && hs.lost != nil:
			why = hs.lost
		case h.d != nil && hs != nil:
			why = hs.err
		}
		if why != nil {
			hosts = append(hosts, sanitizeLine(h.name, 0)+" ("+errLine(why)+")")
		}
	}
	for _, o := range m.jobs.orchs {
		if o.err != nil {
			orchs = append(orchs, sanitizeLine(o.target.label(), 0)+" (runs: "+errLine(o.err)+")")
		}
	}
	sort.Strings(hosts)
	return append(hosts, orchs...)
}

// errLine is an error as one sanitised line, short enough to share a line.
func errLine(err error) string { return sanitizeLine(status.Convert(err).Message(), 60) }

// jobsConnectingHosts names the hosts still connecting (not unreadable yet).
func (m Model) jobsConnectingHosts() []string {
	var out []string
	for _, h := range m.orchHosts() {
		if h.state == client.HostConnecting {
			out = append(out, sanitizeLine(h.name, 0))
		}
	}
	sort.Strings(out)
	return out
}

// jobsLoadingHosts names the connected hosts that have not answered yet.
func (m Model) jobsLoadingHosts() []string {
	var out []string
	for _, h := range m.orchHosts() {
		if hs := m.jobs.hosts[h.id]; hs != nil && hs.loading && !hs.answered {
			out = append(out, sanitizeLine(h.name, 0))
		}
	}
	sort.Strings(out)
	return out
}

// jobsRefreshing reports whether any host load is in flight.
func (m Model) jobsRefreshing() bool {
	for _, hs := range m.jobs.hosts {
		if hs.loading {
			return true
		}
	}
	return false
}

// jobsWaiting reports whether Jobs has nothing to show yet because its first
// loads are still running.
func (m Model) jobsWaiting() bool {
	return len(m.jobs.orchs) == 0 && len(m.jobsLoadingHosts()) > 0
}

// joinNames lists names for a status line.
func joinNames(names []string) string { return strings.Join(names, ", ") }
