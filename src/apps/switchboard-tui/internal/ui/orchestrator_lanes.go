package ui

import (
	"context"
	"errors"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Lanes (SWI-18, contracts/lanes-ui.md). One lanesPanel per surface: the
// orchestrator detail hosts it as the Lanes view (and its worker picker from the
// Workers view), the Jobs screen as the Manage lanes dialog over the action form.
// The panel holds no copy of the detail: it reads its surface's (lanesDetail),
// so a host reload is seen at once. Every guarded write goes through laneWrite:
// CheckLaneChange first, a confirm when the open-lane guard fires, the write
// with the confirmed list, a second confirm when the daemon still refuses.

// lanesPanel is a surface's lane list, lane form and picker.
type lanesPanel struct {
	cursor int
	// The lane form: create (formLane "") or rename/describe.
	form     *huh.Form
	formVals *laneFormVals
	formLane string
	// pickAfterForm reopens the worker picker once a lane made from its empty
	// state is saved (FR-306).
	pickAfterForm bool
	picker        *lanePicker
	// stale is the daemon's message of a stale save (FR-307a): the next detail
	// load reloads the picker from it and shows the message.
	stale  string
	status string
	// saving is set while the lane form's PutLane is out: keys but esc wait.
	saving bool
}

// laneFormVals backs the lane form.
type laneFormVals struct{ name, description string }

// manageLanes is the Jobs screen's Manage lanes dialog (FR-317): the panel for
// the action form's orchestrator, with the detail it reads.
type manageLanes struct {
	open    bool
	target  orchTarget
	detail  *pb.OrchestratorDetail
	loading bool
	panel   lanesPanel
}

// manageDetailMsg carries the dialog's detail load.
type manageDetailMsg struct {
	key    string
	detail *pb.OrchestratorDetail
	err    error
}

// lanes is a surface's panel.
func (m *Model) lanes(s orchSurface) *lanesPanel {
	if s == surfaceJobs {
		return &m.jobs.manage.panel
	}
	return &m.orchDetail.lanes
}

// lanesDetail is the detail a surface's panel reads.
func (m Model) lanesDetail(s orchSurface) *pb.OrchestratorDetail {
	if s == surfaceJobs {
		return m.jobs.manage.detail
	}
	return m.orchDetail.detail
}

// lanesTarget is the orchestrator a surface's panel acts on.
func (m Model) lanesTarget(s orchSurface) orchTarget {
	if s == surfaceJobs {
		return m.jobs.manage.target
	}
	return m.detailTarget()
}

// hostHasLanes reports whether host's daemon is connected and serves lanes.
func (m Model) hostHasLanes(host string) bool {
	d, err := m.connectedDaemon(host)
	return err == nil && d != nil && d.HasCapability("lanes")
}

// tooOldForLanes is the local refusal against a daemon without lanes (FR-318a).
func (m Model) tooOldForLanes(host string) string {
	return "the daemon on " + sanitizeLine(m.hostLabel(host), 0) + " predates lanes — update with u"
}

// hostLabel names a host entry: its display name, else its daemon's host id.
func (m Model) hostLabel(host string) string {
	if n := m.hostDisplayName(host); n != "" {
		return n
	}
	return m.daemonHostID(host)
}

// laneByID finds a lane of an orchestrator.
func laneByID(rec *pb.Orchestrator, id string) *pb.Lane {
	for _, l := range rec.GetLanes() {
		if l.GetId() == id {
			return l
		}
	}
	return nil
}

// laneNames are the names of lane ids, in the record's order; unknown ids are
// skipped.
func laneNames(rec *pb.Orchestrator, ids []string) []string {
	var out []string
	for _, l := range rec.GetLanes() {
		if slices.Contains(ids, l.GetId()) {
			out = append(out, l.GetName())
		}
	}
	return out
}

// laneMembers are the workers in a lane.
func laneMembers(det *pb.OrchestratorDetail, laneID string) []*pb.WorkerView {
	var out []*pb.WorkerView
	for _, w := range det.GetWorkers() {
		if slices.Contains(w.GetLaneIds(), laneID) {
			out = append(out, w)
		}
	}
	return out
}

// modeWord is a lane mode in one word.
func modeWord(mode pb.LaneMode) string {
	if mode == pb.LaneMode_LANE_MODE_INCLUSIVE {
		return "inclusive"
	}
	return "exclusive"
}

// --- keys ---

// updateLanesKey routes a key to a surface's panel. handled is false for keys
// the panel leaves to its host (←/→, tab, q, the host's own shortcuts).
func (m Model) updateLanesKey(s orchSurface, msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	p := m.lanes(s)
	if p.form != nil {
		switch msg.String() {
		case "esc":
			p.form, p.formVals, p.status = nil, nil, ""
			p.pickAfterForm, p.saving = false, false
			return m, nil, true
		}
		if p.saving {
			return m, nil, true // the save is out
		}
		switch msg.String() {
		case "ctrl+s":
			mm, cmd := m.submitLaneForm(s)
			return mm, cmd, true
		}
		f, cmd := p.form.Update(msg)
		if ff, ok := f.(*huh.Form); ok {
			p.form = ff
		}
		if p.form.State == huh.StateCompleted {
			mm, cmd := m.submitLaneForm(s)
			return mm, cmd, true
		}
		return m, cmd, true
	}
	if p.picker != nil {
		mm, cmd := m.updateLanePickerKey(s, msg)
		return mm, cmd, true
	}
	rec := m.lanesDetail(s).GetSummary().GetOrchestrator()
	lanes := rec.GetLanes()
	switch msg.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
		return m, nil, true
	case "down", "j":
		if p.cursor < len(lanes)-1 {
			p.cursor++
		}
		return m, nil, true
	case "a":
		if t := m.lanesTarget(s); !m.hostHasLanes(t.host) {
			p.status = m.tooOldForLanes(t.host)
			return m, nil, true
		}
		return m, m.openLaneForm(s, nil), true
	case "D":
		if len(rec.GetDroppedWorkerTags()) == 0 {
			return m, nil, true
		}
		return m, m.dismissTagsCmd(s, m.lanesTarget(s)), true
	}
	if len(lanes) == 0 || p.cursor >= len(lanes) {
		return m, nil, msg.String() == "e" || msg.String() == "w" || msg.String() == "d"
	}
	l := lanes[p.cursor]
	switch msg.String() {
	case "e":
		return m, m.openLaneForm(s, l), true
	case "w":
		m.openMembersPicker(s, l)
		return m, nil, true
	case "d":
		return m, m.deleteLaneCmd(s, l), true
	}
	return m, nil, false
}

// openLaneForm opens the lane form, pre-filled for an existing lane.
func (m *Model) openLaneForm(s orchSurface, l *pb.Lane) tea.Cmd {
	p := m.lanes(s)
	v := &laneFormVals{name: l.GetName(), description: l.GetDescription()}
	p.formVals, p.formLane, p.status = v, l.GetId(), ""
	p.form = m.laneForm(v)
	return p.form.Init()
}

// laneForm builds the lane form over v (rebuilt after a refusal, keeping v).
func (m Model) laneForm(v *laneFormVals) *huh.Form {
	return m.newOrchForm(nil,
		huh.NewInput().Key("name").Title("Name").Description("letters, digits, - and _; \"open\" is reserved").Value(&v.name),
		huh.NewInput().Key("description").Title("Description").Description("optional; one line, never sent to agents").Value(&v.description),
	)
}

// laneFormMsg is a lane form's outcome.
type laneFormMsg struct {
	sink orchSurface
	key  string // the orchestrator it was saved on
	lane *pb.Lane
	err  error
}

func (m Model) submitLaneForm(s orchSurface) (Model, tea.Cmd) {
	p := m.lanes(s)
	t := m.lanesTarget(s)
	od, err := m.connectedDaemon(t.host)
	if err != nil {
		p.status = err.Error()
		p.form = m.laneForm(p.formVals)
		return m, p.form.Init()
	}
	v, laneID, id, key := *p.formVals, p.formLane, t.id, t.key()
	p.saving = true
	return m, func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		l, err := od.PutLane(ctx, id, laneID, strings.TrimSpace(v.name), strings.TrimSpace(v.description))
		return laneFormMsg{sink: s, key: key, lane: l, err: err}
	}
}

func (m Model) applyLaneForm(msg laneFormMsg) (tea.Model, tea.Cmd) {
	p := m.lanes(msg.sink)
	if p.formVals == nil || !p.saving || m.lanesTarget(msg.sink).key() != msg.key {
		// Closed meanwhile, or another orchestrator's form: the footer only.
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
		} else {
			m.status = "saved lane " + msg.lane.GetName()
		}
		return m, nil
	}
	p.saving = false
	if msg.err != nil {
		// The form stays open with every value and the daemon's text.
		p.status = msg.err.Error()
		p.form = m.laneForm(p.formVals)
		return m, p.form.Init()
	}
	p.form, p.formVals, p.status = nil, nil, ""
	if p.pickAfterForm && p.picker != nil {
		// Back to the worker picker; the reload shows the new lane, ticked.
		p.picker.ticked[msg.lane.GetId()] = true
	}
	p.pickAfterForm = false
	return m.applyOrchDone(orchDoneMsg{status: "saved lane " + msg.lane.GetName(), reload: true, sink: msg.sink, target: m.lanesTarget(msg.sink)})
}

// dismissTagsCmd clears the dropped-tag notice (FR-318b).
func (m Model) dismissTagsCmd(s orchSurface, t orchTarget) tea.Cmd {
	od, err := m.connectedDaemon(t.host)
	if err != nil {
		return errCmd(s, err)
	}
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := od.DismissDroppedWorkerTags(ctx, t.id); err != nil {
			return orchDoneMsg{err: err, sink: s}
		}
		return orchDoneMsg{status: "dismissed the dropped worker tags", reload: true, sink: s, target: t}
	}
}

// --- the picker ---

// openWorkerPicker is `l` on a Workers row (FR-306).
func (m *Model) openWorkerPicker(s orchSurface, w *pb.WorkerView) {
	rec := m.lanesDetail(s).GetSummary().GetOrchestrator()
	p := newLanePicker(pickWorkerLanes, "Lanes of "+workerName(w), w.GetSandboxId(), laneRows(rec), w.GetLaneIds())
	p.openedPending = w.GetLanesPending()
	m.lanes(s).picker = p
}

// openMembersPicker is `w` on a Lanes row (FR-307).
func (m *Model) openMembersPicker(s orchSurface, l *pb.Lane) {
	det := m.lanesDetail(s)
	var ids []string
	for _, w := range laneMembers(det, l.GetId()) {
		ids = append(ids, w.GetSandboxId())
	}
	m.lanes(s).picker = newLanePicker(pickLaneMembers, "Members of "+l.GetName(), l.GetId(), memberRows(det), ids)
}

func (m Model) updateLanePickerKey(s orchSurface, msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.lanes(s)
	pk := p.picker
	switch pk.update(msg) {
	case pickClose:
		p.picker = nil
	case pickAdd:
		p.pickAfterForm = true
		return m, m.openLaneForm(s, nil)
	case pickSave:
		pk.status = ""
		w, ok := m.pickerWrite(s)
		if !ok {
			return m, nil
		}
		pk.busy = true
		return m, laneWriteCmd(w, nil, true)
	}
	return m, nil
}

// pickerWrite is the guarded write of the open picker's save.
func (m Model) pickerWrite(s orchSurface) (laneWrite, bool) {
	p := m.lanes(s)
	pk := p.picker
	t := m.lanesTarget(s)
	od, err := m.connectedDaemon(t.host)
	if err != nil {
		pk.status = err.Error()
		return laneWrite{}, false
	}
	sel := pk.selected()
	w := laneWrite{sink: s, target: t, closePicker: true}
	switch pk.mode {
	case pickWorkerLanes:
		req := func() *pb.SetWorkerLanesRequest {
			return &pb.SetWorkerLanesRequest{OrchestratorId: t.id, SandboxId: pk.subject, LaneIds: sel, ExpectedLaneIds: pk.opened, ExpectedLanesPending: pk.openedPending}
		}
		w.check = func(ctx context.Context) (*pb.LaneChangeImpact, error) {
			return od.CheckLaneChange(ctx, &pb.CheckLaneChangeRequest{OrchestratorId: t.id, Change: &pb.CheckLaneChangeRequest_SetWorkerLanes{SetWorkerLanes: req()}})
		}
		w.save = func(ctx context.Context, g *pb.LaneGuardConfirm) error {
			r := req()
			r.Guard = g
			_, err := od.SetWorkerLanes(ctx, r)
			return err
		}
		w.done = "saved lanes of " + strings.TrimPrefix(pk.title, "Lanes of ")
	case pickLaneMembers:
		req := func() *pb.SetLaneMembersRequest {
			return &pb.SetLaneMembersRequest{OrchestratorId: t.id, LaneId: pk.subject, SandboxIds: sel, ExpectedSandboxIds: pk.opened}
		}
		w.check = func(ctx context.Context) (*pb.LaneChangeImpact, error) {
			return od.CheckLaneChange(ctx, &pb.CheckLaneChangeRequest{OrchestratorId: t.id, Change: &pb.CheckLaneChangeRequest_SetLaneMembers{SetLaneMembers: req()}})
		}
		w.save = func(ctx context.Context, g *pb.LaneGuardConfirm) error {
			r := req()
			r.Guard = g
			_, err := od.SetLaneMembers(ctx, r)
			return err
		}
		w.done = "saved members of " + strings.TrimPrefix(pk.title, "Members of ")
	default:
		return laneWrite{}, false
	}
	return w, true
}

// refreshLanes follows a surface's detail reload: the cursor is clamped and an
// open picker gets fresh rows — a full reload after a stale save, else ticks
// for rows that are gone are dropped. A picker whose subject is gone closes.
func (m *Model) refreshLanes(s orchSurface) {
	p := m.lanes(s)
	det := m.lanesDetail(s)
	rec := det.GetSummary().GetOrchestrator()
	if n := len(rec.GetLanes()); p.cursor >= n {
		p.cursor = max(n-1, 0)
	}
	pk := p.picker
	if pk == nil {
		return
	}
	var rows []pickRow
	var fresh []string
	pending, found := false, false
	switch pk.mode {
	case pickWorkerLanes:
		rows = laneRows(rec)
		for _, w := range det.GetWorkers() {
			if w.GetSandboxId() == pk.subject {
				found, fresh, pending = true, w.GetLaneIds(), w.GetLanesPending()
			}
		}
	case pickLaneMembers:
		rows = memberRows(det)
		found = laneByID(rec, pk.subject) != nil
		for _, w := range laneMembers(det, pk.subject) {
			fresh = append(fresh, w.GetSandboxId())
		}
	default:
		return
	}
	if !found {
		p.picker, p.stale = nil, ""
		p.status = "closed the picker: it is no longer on " + sanitizeLine(rec.GetName(), 0)
		return
	}
	if p.stale != "" {
		pk.reload(rows, fresh, pending)
		pk.status, p.stale = p.stale, ""
		return
	}
	pk.refreshRows(rows)
}

// refreshRows replaces the rows keeping ticks and the cursor by id.
func (p *lanePicker) refreshRows(rows []pickRow) {
	ticked := []string{}
	for _, r := range rows {
		if p.ticked[r.id] {
			ticked = append(ticked, r.id)
		}
	}
	opened, pending, busy := p.opened, p.openedPending, p.busy
	p.reload(rows, ticked, pending)
	p.opened, p.busy = opened, busy
}

// --- delete ---

// deleteLaneCmd checks a lane delete and opens its confirm (FR-305).
func (m Model) deleteLaneCmd(s orchSurface, l *pb.Lane) tea.Cmd {
	t := m.lanesTarget(s)
	od, err := m.connectedDaemon(t.host)
	if err != nil {
		return errCmd(s, err)
	}
	name := l.GetName()
	req := func(g *pb.LaneGuardConfirm) *pb.DeleteLaneRequest {
		return &pb.DeleteLaneRequest{OrchestratorId: t.id, LaneId: l.GetId(), Guard: g}
	}
	w := laneWrite{sink: s, target: t, done: "deleted lane " + name, onPanel: true,
		check: func(ctx context.Context) (*pb.LaneChangeImpact, error) {
			return od.CheckLaneChange(ctx, &pb.CheckLaneChangeRequest{OrchestratorId: t.id, Change: &pb.CheckLaneChangeRequest_DeleteLane{DeleteLane: req(nil)}})
		},
		save: func(ctx context.Context, g *pb.LaneGuardConfirm) error {
			r := req(g)
			_, err := od.DeleteLane(ctx, r.GetOrchestratorId(), r.GetLaneId(), r.GetGuard())
			return err
		},
		confirm: func(imp *pb.LaneChangeImpact) confirmState { return deleteLaneConfirm(name, imp) },
	}
	return laneWriteCmd(w, nil, true)
}

// deleteLaneConfirm lists what a delete changes; an unused lane gets a plain
// confirm.
func deleteLaneConfirm(name string, imp *pb.LaneChangeImpact) confirmState {
	var body []string
	if len(imp.GetWorkers()) > 0 {
		body = append(body, "Workers leave it: "+capNames(imp.GetWorkers()))
	}
	if len(imp.GetActions()) > 0 {
		uses := make([]string, 0, len(imp.GetActions()))
		for _, a := range imp.GetActions() {
			uses = append(uses, a.GetAction()+" ("+modeWord(a.GetMode())+")")
		}
		body = append(body, "Actions using it: "+capNames(uses))
	}
	if len(imp.GetActionsLeftUnlaned()) > 0 {
		body = append(body, "Left un-laned: "+capNames(imp.GetActionsLeftUnlaned()))
	}
	if g := guardText(imp); g != "" {
		body = append(body, "No open-lane worker will be left for: "+g)
	}
	if n := imp.GetReroutedRuns(); n > 0 {
		body = append(body, plural(int(n), "queued run", "queued runs")+" will be re-routed")
	}
	if len(body) == 0 {
		body = []string{"No worker or action uses it."}
	}
	return confirmState{title: "Delete lane " + name + "?", body: wrapBody(body), warning: "The lane is removed from every worker and action."}
}

// --- the guarded write ---

// laneWrite is one guarded lane write (FR-308a).
type laneWrite struct {
	sink   orchSurface
	target orchTarget
	// check runs CheckLaneChange (nil: no check).
	check func(ctx context.Context) (*pb.LaneChangeImpact, error)
	// save performs the write with the confirmed guard list.
	save func(ctx context.Context, g *pb.LaneGuardConfirm) error
	// confirm, when set, always opens a confirm built from the impact (delete,
	// detach); otherwise one opens only when the guard fires.
	confirm func(imp *pb.LaneChangeImpact) confirmState
	// done is the footer text on success; closePicker closes the panel's picker,
	// closeForm the surface's open form. A refusal lands on the picker, on the
	// panel (onPanel), else on the surface's line.
	done        string
	closePicker bool
	closeForm   bool
	onPanel     bool
	// returnTo, when returnSet, is where its confirms return; else the
	// sink's screen.
	returnTo  screen
	returnSet bool
	// list marks a write started from the sandbox list: its outcome goes the
	// list's way (statusMsg / errMsg), busyID's row spinning once confirmed.
	list   bool
	busyID string
}

// back is the screen a write's confirms return to.
func (w laneWrite) back() screen {
	if w.returnSet {
		return w.returnTo
	}
	return surfaceScreen(w.sink)
}

// laneResultMsg is a guarded write's step: an impact to confirm (checked), or
// the write's outcome.
type laneResultMsg struct {
	w       laneWrite
	impact  *pb.LaneChangeImpact
	checked bool
	err     error
}

// laneWriteCmd runs a write's check (when check is set) or its save with g.
func laneWriteCmd(w laneWrite, g *pb.LaneGuardConfirm, check bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if check && w.check != nil {
			imp, err := w.check(ctx)
			if err != nil {
				return laneResultMsg{w: w, err: err}
			}
			if w.confirm != nil || guardText(imp) != "" {
				return laneResultMsg{w: w, impact: imp, checked: true}
			}
		}
		return laneResultMsg{w: w, err: w.save(ctx, g)}
	}
}

// guardOf is the confirmation of an impact's guard list.
func guardOf(imp *pb.LaneChangeImpact) *pb.LaneGuardConfirm {
	return &pb.LaneGuardConfirm{UnlanedActions: imp.GetUnlanedActions(), UnlanedRuns: imp.GetUnlanedRuns()}
}

// guardText names what the guard fires for: "build, deploy (and 2 queued
// runs)", or "" when it does not fire.
func guardText(imp *pb.LaneChangeImpact) string {
	acts, runs := imp.GetUnlanedActions(), len(imp.GetUnlanedRuns())
	switch {
	case len(acts) > 0 && runs > 0:
		return capNames(acts) + " (and " + plural(runs, "queued run", "queued runs") + ")"
	case len(acts) > 0:
		return capNames(acts)
	case runs > 0:
		return plural(runs, "queued run", "queued runs")
	}
	return ""
}

// guardConfirm is the guard's own confirm.
func guardConfirm(imp *pb.LaneChangeImpact) confirmState {
	return confirmState{
		title:   "No open-lane worker left",
		body:    wrapBody([]string{"No open-lane worker will be left to run: " + guardText(imp) + ". Save anyway?"}),
		warning: "Un-laned work waits until an open-lane worker is attached.",
	}
}

// wrapBody keeps confirm lines on the modal (they are cut at its width).
func wrapBody(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, sanitizeLine(l, 0))
	}
	return out
}

// atOrigin reports whether the user is still where a guarded write started:
// on its orchestrator, with its form, picker or Manage lanes dialog open. A
// result arriving anywhere else changes nothing but the footer.
func (m Model) atOrigin(w laneWrite) bool {
	if w.list {
		return true
	}
	t := m.lanesTarget(w.sink)
	switch {
	case w.closeForm:
		op := m.opState(w.sink)
		if op.form == nil {
			return false
		}
		t = op.formTarget
	case w.sink == surfaceJobs && !m.jobs.manage.open:
		return false
	case w.closePicker && m.lanes(w.sink).picker == nil:
		return false
	}
	return t.key() == w.target.key()
}

// laneConfirm opens a write's confirm only while the user is still on the
// screen it started from (no other confirm showing); otherwise the confirm is
// dropped with a footer note, and the user starts the change again.
func (m Model) laneConfirm(w laneWrite, st confirmState) (tea.Model, tea.Cmd) {
	here := m.atOrigin(w)
	if p := m.lanes(w.sink); here && p.picker != nil && !w.list {
		p.picker.busy = false
	}
	if m.screen != w.back() || !here {
		m.status = "lane check for " + sanitizeLine(w.target.name, 0) + " came back after you left — not applied; start it again"
		return m, nil
	}
	if v, ok := m.opState(w.sink).formVals.(*actionFormVals); ok && w.closeForm {
		// A declined confirm returns to an editable form (a completed huh form
		// takes no more keys).
		cmd := m.rebuildActionForm(w.sink, v)
		mm, ccmd := m.enterConfirm(st)
		return mm, tea.Batch(cmd, ccmd)
	}
	return m.enterConfirm(st)
}

// applyLaneResult advances a guarded write.
func (m Model) applyLaneResult(msg laneResultMsg) (tea.Model, tea.Cmd) {
	w := msg.w
	p := m.lanes(w.sink)
	if msg.checked {
		st := guardConfirm(msg.impact)
		if w.confirm != nil {
			st = w.confirm(msg.impact)
		}
		st.returnTo = w.back()
		st.onConfirm = laneWriteCmd(w, guardOf(msg.impact), false)
		if w.list {
			st.sandboxID, st.verb = w.busyID, "detaching"
		}
		return m.laneConfirm(w, st)
	}
	if w.list {
		if imp, ok := client.ImpactFromError(msg.err); ok && guardText(imp) != "" {
			st := w.confirm(imp)
			st.returnTo, st.onConfirm = w.back(), laneWriteCmd(w, guardOf(imp), false)
			st.sandboxID, st.verb = w.busyID, "detaching"
			delete(m.busy, w.busyID)
			return m.laneConfirm(w, st)
		}
		if msg.err != nil {
			return m.Update(errMsg{msg.err})
		}
		return m.Update(statusMsg(w.done))
	}
	here := m.atOrigin(w)
	if msg.err == nil {
		if w.closePicker && here {
			p.picker = nil
		}
		return m.applyOrchDone(orchDoneMsg{status: w.done, reload: true, closeForm: w.closeForm && here, sink: w.sink, target: w.target})
	}
	if imp, ok := client.ImpactFromError(msg.err); ok && guardText(imp) != "" {
		// The daemon's list changed since the confirm: confirm the new one.
		st := guardConfirm(imp)
		st.returnTo = w.back()
		st.onConfirm = laneWriteCmd(w, guardOf(imp), false)
		return m.laneConfirm(w, st)
	}
	if !here {
		m.status = "error: " + sanitizeLine(w.target.name, 0) + ": " + msg.err.Error()
		return m, nil
	}
	m.status = "error: " + msg.err.Error()
	text := msg.err.Error()
	stale := errors.Is(msg.err, client.ErrLaneStale) || status.Code(msg.err) == codes.Aborted
	switch {
	case p.picker != nil && w.closePicker && stale:
		p.stale = text
	case p.picker != nil && w.closePicker:
		p.picker.busy, p.picker.status = false, text
	case w.onPanel:
		p.status = text
	case m.onSurface(w.sink):
		// Off its screen the refusal is on the footer only.
		m.opState(w.sink).status = text
	}
	if v, ok := m.opState(w.sink).formVals.(*actionFormVals); ok && w.closeForm {
		// The form stays open with every value (rebuilt: an enter-completed huh
		// form takes no more keys); a missing lane reloads its options (FR-316a).
		cmd := m.rebuildActionForm(w.sink, v)
		if status.Code(msg.err) == codes.NotFound {
			return m, tea.Batch(cmd, m.actionLanesCmd(w.sink, w.target))
		}
		return m, cmd
	}
	if stale || status.Code(msg.err) == codes.NotFound {
		return m.applyOrchDone(orchDoneMsg{reload: true, sink: w.sink, target: w.target, status: m.status})
	}
	return m, nil
}

// actionLanesMsg carries an orchestrator re-read for an open action form.
type actionLanesMsg struct {
	sink orchSurface
	key  string
	rec  *pb.Orchestrator
}

// actionLanesCmd re-reads the orchestrator for the action form's lane options.
func (m Model) actionLanesCmd(s orchSurface, t orchTarget) tea.Cmd {
	od, err := m.connectedDaemon(t.host)
	if err != nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		det, err := od.GetOrchestrator(ctx, t.id)
		if err != nil {
			return nil
		}
		return actionLanesMsg{sink: s, key: t.key(), rec: det.GetSummary().GetOrchestrator()}
	}
}

func (m Model) applyActionLanes(msg actionLanesMsg) (tea.Model, tea.Cmd) {
	op := m.opState(msg.sink)
	v, ok := op.formVals.(*actionFormVals)
	if !ok || op.formTarget.key() != msg.key {
		return m, nil
	}
	op.formTarget.rec = msg.rec
	v.keepLanes(msg.rec)
	return m, m.rebuildActionForm(msg.sink, v)
}

// --- the Jobs dialog ---

// openManageLanes is ctrl+l on the action form (FR-317).
func (m Model) openManageLanes() (tea.Model, tea.Cmd) {
	t := m.jobs.formTarget
	if !m.hostHasLanes(t.host) {
		m.jobs.status = m.tooOldForLanes(t.host)
		return m, nil
	}
	m.jobs.manage = manageLanes{open: true, target: t, loading: true}
	return m, m.manageLoadCmd()
}

// manageLoadCmd loads the dialog's detail.
func (m Model) manageLoadCmd() tea.Cmd {
	t := m.jobs.manage.target
	od, err := m.connectedDaemon(t.host)
	if err != nil {
		return func() tea.Msg { return manageDetailMsg{key: t.key(), err: err} }
	}
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		det, err := od.GetOrchestrator(ctx, t.id)
		return manageDetailMsg{key: t.key(), detail: det, err: err}
	}
}

func (m Model) applyManageDetail(msg manageDetailMsg) (tea.Model, tea.Cmd) {
	mg := &m.jobs.manage
	if !mg.open || mg.target.key() != msg.key {
		return m, nil
	}
	mg.loading = false
	if msg.err != nil {
		mg.panel.status = msg.err.Error()
		return m, nil
	}
	mg.detail = msg.detail
	mg.target.rec = msg.detail.GetSummary().GetOrchestrator()
	m.refreshLanes(surfaceJobs)
	if v, ok := m.jobs.formVals.(*actionFormVals); ok {
		// The action form's lane options follow (FR-317): by id, deleted
		// lanes' ticks dropped, renamed lanes renamed.
		m.jobs.formTarget.rec = mg.target.rec
		v.keepLanes(mg.target.rec)
	}
	return m, nil
}

// updateManageKey routes keys while the dialog is open; esc closes it back to
// the action form with every value intact.
func (m Model) updateManageKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := &m.jobs.manage.panel
	if msg.String() == "esc" && p.form == nil && p.picker == nil {
		return m.closeManageLanes()
	}
	if msg.String() == "r" && p.form == nil && p.picker == nil {
		m.jobs.manage.loading = true
		return m, m.manageLoadCmd()
	}
	mm, cmd, _ := m.updateLanesKey(surfaceJobs, msg)
	return mm, cmd
}

// closeManageLanes returns to the action form, rebuilt with its lane options.
func (m Model) closeManageLanes() (tea.Model, tea.Cmd) {
	m.jobs.manage = manageLanes{}
	if v, ok := m.jobs.formVals.(*actionFormVals); ok {
		return m, m.rebuildActionForm(surfaceJobs, v)
	}
	return m, nil
}

// --- rendering ---

// droppedTagsLine is the dropped-tag notice (FR-318b), or "".
func droppedTagsLine(rec *pb.Orchestrator) string {
	tags := rec.GetDroppedWorkerTags()
	if len(tags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		parts = append(parts, t.GetAction()+" (#"+t.GetTag()+")")
	}
	return "worker tags dropped on upgrade: " + capNames(parts) + " — D dismiss"
}

// lanesView renders a surface's panel: its form, its picker or the list.
func (m Model) lanesView(s orchSurface) []string {
	p := m.lanes(s)
	if p.form != nil {
		title := "New lane"
		if p.formLane != "" {
			title = "Edit lane"
		}
		lines := []string{sectionStyle.Render(title), "", p.form.View()}
		if p.status != "" {
			lines = append(lines, "", statusErrStyle.Render(sanitizeTerminalText(p.status)))
		}
		return lines
	}
	if p.picker != nil {
		return []string{p.picker.view(m.bodyWidth(), m.bodyHeight())}
	}
	det := m.lanesDetail(s)
	rec := det.GetSummary().GetOrchestrator()
	var lines []string
	if n := droppedTagsLine(rec); n != "" {
		lines = append(lines, m.fitLine(agentWaitStyle.Render(sanitizeLine(n, 0))))
	}
	if len(rec.GetLanes()) == 0 {
		lines = append(lines, dimStyle.Render("no lanes yet — a to add one"))
	}
	half := max((m.bodyWidth()-6)/2, 12)
	var rows []string
	for i, l := range rec.GetLanes() {
		cursor := "  "
		if i == p.cursor {
			cursor = cursorBarStyle.Render("> ")
		}
		head := cursor + fitCol(l.GetName(), 16)
		if l.GetDescription() != "" {
			head += dimStyle.Render("  " + sanitizeLine(l.GetDescription(), 0))
		}
		var members []string
		for _, w := range laneMembers(det, l.GetId()) {
			members = append(members, workerName(w))
		}
		var uses []string
		for _, a := range rec.GetActions() {
			if slices.Contains(a.GetLaneIds(), l.GetId()) {
				uses = append(uses, a.GetName()+" ("+modeWord(a.GetLaneMode())+")")
			}
		}
		mem, act := "(none)", "(none)"
		if len(members) > 0 {
			mem = capNames(members)
		}
		if len(uses) > 0 {
			act = capNames(uses)
		}
		rows = append(rows, m.fitLine(head),
			m.fitLine(dimStyle.Render("    "+fitCol("members: "+mem, half)+"  "+sanitizeLine("actions: "+act, 0))))
	}
	// Windowed around the cursor (two lines a lane), under the detail's or the
	// dialog's header, the notice and the status.
	lines = append(lines, windowLines(rows, 2*p.cursor+1, m.bodyHeight()-len(lines)-6)...)
	if p.status != "" {
		lines = append(lines, "", statusErrStyle.Render(sanitizeTerminalText(p.status)))
	}
	return lines
}

// lanesHelp is the panel's help.
func (m Model) lanesHelp(s orchSurface) helpBindings {
	p := m.lanes(s)
	switch {
	case p.form != nil:
		return helpBindings{hkey("tab", "next"), hkey("ctrl+s", "save"), hkey("esc", "cancel")}
	case p.picker != nil:
		return helpBindings{hkey("space", "toggle"), hkey("enter", "save"), hkey("esc", "cancel")}
	}
	hb := helpBindings{hkey("a", "add"), hkey("e", "edit"), hkey("w", "members"), hkey("d", "delete")}
	if len(m.lanesDetail(s).GetSummary().GetOrchestrator().GetDroppedWorkerTags()) > 0 {
		hb = append(hb, hkey("D", "dismiss tags"))
	}
	return hb
}

// viewManageLanes is the Jobs dialog.
func (m Model) viewManageLanes() string {
	mg := m.jobs.manage
	lines := []string{sectionStyle.Render("Manage lanes") + dimStyle.Render("  "+sanitizeLine(mg.target.label(), 0)), ""}
	switch {
	case mg.detail == nil && mg.loading:
		lines = append(lines, m.spinner.View()+dimStyle.Render(" loading…"))
	case mg.detail == nil:
		if mg.panel.status != "" {
			lines = append(lines, statusErrStyle.Render(sanitizeTerminalText(mg.panel.status)))
		}
	default:
		lines = append(lines, m.lanesView(surfaceJobs)...)
	}
	return strings.Join(lines, "\n")
}
