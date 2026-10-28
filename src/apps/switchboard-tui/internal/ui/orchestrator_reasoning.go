package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The Reasoning view (feature 009, user story 7, FR-145–FR-147): the
// orchestrator's reasoning sandbox — its state, the saved setup, and the two
// operations on it: `e` edit the setup through the launch browser (folders,
// kits) ending in a rebuild, and `b` rebuild with the saved setup. Rebuilds
// stream into the sbx output log (`l`) like a launch.

// reasoningRebuild is one in-flight rebuild, keyed by orchestrator id.
type reasoningRebuild struct {
	id, name string
	latest   string
	ch       chan tea.Msg
}

// reasoningRebuildMsg asks for a rebuild (the confirm dialog and the setup
// browser both produce it); nil setup = the saved one.
type reasoningRebuildMsg struct {
	id, host, name string
	setup          *pb.ReasoningSetup
}

type reasoningProgressMsg struct {
	id     string
	update client.LaunchUpdate
}

type reasoningDoneMsg struct {
	id  string
	sb  *pb.Sandbox
	err error
}

// reasoningLogKey is the sbx-output log key of an orchestrator's rebuilds.
func reasoningLogKey(orchID string) string { return "reasoning:" + orchID }

// reasoningWorker is the roster entry of the reasoning sandbox, if listed.
func (d *orchDetailState) reasoningWorker() *pb.WorkerView {
	for _, w := range d.detail.GetWorkers() {
		if w.GetReasoning() {
			return w
		}
	}
	return nil
}

// reasoningSetupLines describes a saved setup (FR-147: empty = default context).
func reasoningSetupLines(setup *pb.ReasoningSetup) []string {
	if setup == nil || (len(setup.GetSources()) == 0 && setup.GetConfig().GetName() == "" && len(setup.GetKits()) == 0 && len(setup.GetMcpServers()) == 0) {
		return []string{"default: empty context (no folders, default agent)"}
	}
	var lines []string
	if len(setup.GetSources()) > 0 {
		var names []string
		for _, s := range setup.GetSources() {
			names = append(names, filepath.Base(s.GetPath()))
		}
		mode := "duplicate"
		if setup.GetConfig().GetSeedingMode() == pb.SeedingMode_SEEDING_MODE_CLONE {
			mode = "clone"
		}
		lines = append(lines, "folders: "+strings.Join(names, ", ")+" ("+mode+")")
	} else {
		lines = append(lines, "folders: none (empty workspace)")
	}
	if n := setup.GetConfig().GetName(); n != "" {
		lines = append(lines, "configuration: "+n)
	}
	if n := len(setup.GetKits()); n > 0 {
		lines = append(lines, fmt.Sprintf("kits: %d", n))
	}
	if len(setup.GetMcpServers()) > 0 {
		lines = append(lines, "MCP servers: "+strings.Join(setup.GetMcpServers(), ", "))
	}
	return lines
}

func (m Model) updateReasoningViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	switch msg.String() {
	case "b":
		if _, busy := m.rebuilds[d.id]; busy {
			d.status = "a rebuild is already in progress"
			return m, nil
		}
		req := reasoningRebuildMsg{id: d.id, host: d.host, name: d.name}
		body := []string{"The current reasoning sandbox is destroyed and a new one is created from the saved setup:"}
		for _, l := range reasoningSetupLines(d.detail.GetSummary().GetOrchestrator().GetReasoningSetup()) {
			body = append(body, "  "+sanitizeLine(l, 0))
		}
		body = append(body, "Its conversation and workspace are lost.")
		return m.enterConfirm(confirmState{
			title:     fmt.Sprintf("Rebuild the reasoning sandbox of %s?", d.name),
			body:      body,
			warning:   "Refused while a run is on it.",
			returnTo:  screenOrchDetail,
			onConfirm: func() tea.Msg { return req },
		})
	case "e":
		if _, busy := m.rebuilds[d.id]; busy {
			d.status = "a rebuild is already in progress"
			return m, nil
		}
		return m.enterReasoningSetup()
	case "l":
		return m.enterOpLog(&pb.Sandbox{Id: reasoningLogKey(d.id)})
	}
	return m, nil
}

// reasoningFormSubmit: the view opens no huh form; kept for the dispatcher.
func (m Model) reasoningFormSubmit() (tea.Model, tea.Cmd) {
	m.orchDetail.form, m.orchDetail.formVals, m.orchDetail.formKind = nil, nil, ""
	return m, nil
}

// enterReasoningSetup opens the launch browser on the orchestrator's host with
// the saved folders pre-selected; enter (or ctrl+s) rebuilds with the
// selection, kits from K (FR-146).
func (m Model) enterReasoningSetup() (tea.Model, tea.Cmd) {
	d := m.orchDetail
	setup := d.detail.GetSummary().GetOrchestrator().GetReasoningSetup()
	m.screen = screenLaunch
	m.launch = launchState{
		selected:   map[string]bool{},
		srcRepo:    map[string]bool{},
		name:       newNameInput(),
		targetHost: d.host,
		reasoning:  &reasoningTarget{id: d.id, host: d.host, name: d.name},
		cloneMode:  setup.GetConfig().GetSeedingMode() == pb.SeedingMode_SEEDING_MODE_CLONE,
	}
	for _, s := range setup.GetSources() {
		m.launch.selected[s.GetPath()] = true
		m.launch.srcRepo[s.GetPath()] = s.GetIsRepo()
		m.launch.order = append(m.launch.order, s.GetPath())
	}
	return m.startBrowse(m.launchRootFor(d.host))
}

// startReasoningRebuildFromSetup turns the browser's selection into a setup
// and starts the rebuild. Zero folders is the default empty context.
func (m Model) startReasoningRebuildFromSetup() (tea.Model, tea.Cmd) {
	rt := m.launch.reasoning
	kitRefs, err := m.launch.selectedKitRefs()
	if err != nil {
		m.launch.progress = ""
		m.launch.loadErr = "kit error: " + err.Error()
		return m, nil
	}
	mode := pb.SeedingMode_SEEDING_MODE_DUPLICATE
	if m.launch.cloneMode {
		mode = pb.SeedingMode_SEEDING_MODE_CLONE
	}
	setup := &pb.ReasoningSetup{Sources: m.selectedSources(), Config: &pb.ConfigSnapshot{SeedingMode: mode}, Kits: kitRefs}
	m.screen = screenOrchDetail
	return m.startReasoningRebuild(reasoningRebuildMsg{id: rt.id, host: rt.host, name: rt.name, setup: setup})
}

// startReasoningRebuild runs RebuildReasoningSandbox in the background,
// streaming its lines into the sbx output log and a status badge.
func (m Model) startReasoningRebuild(msg reasoningRebuildMsg) (tea.Model, tea.Cmd) {
	if m.rebuilds == nil {
		m.rebuilds = map[string]*reasoningRebuild{}
	}
	if _, busy := m.rebuilds[msg.id]; busy {
		m.orchDetail.status = "a rebuild is already in progress"
		return m, nil
	}
	od, err := m.connectedDaemon(msg.host)
	if err != nil {
		m.orchDetail.status = err.Error()
		m.status = "error: " + err.Error()
		return m, nil
	}
	key := reasoningLogKey(msg.id)
	ch := make(chan tea.Msg, 32)
	m.rebuilds[msg.id] = &reasoningRebuild{id: msg.id, name: msg.name, latest: "starting…", ch: ch}
	m.beginOpLog(key, "reasoning sandbox of "+msg.name, "rebuild")
	m.lastOpLogID = key
	if m.orchDetail.id == msg.id {
		m.orchDetail.status = "rebuilding the reasoning sandbox… (l shows the output)"
	}
	id, setup := msg.id, msg.setup
	go func() {
		sb, err := od.RebuildReasoningSandbox(context.Background(), id, setup, func(u client.LaunchUpdate) {
			ch <- reasoningProgressMsg{id: id, update: u}
		})
		ch <- reasoningDoneMsg{id: id, sb: sb, err: err}
		close(ch)
	}()
	return m, waitForMsg(ch)
}

func (m Model) handleReasoningProgress(msg reasoningProgressMsg) (tea.Model, tea.Cmd) {
	rb, ok := m.rebuilds[msg.id]
	if !ok {
		return m, nil
	}
	key := reasoningLogKey(msg.id)
	switch {
	case msg.update.Copy != nil:
		total := msg.update.Copy.GetBytesTotal()
		pct := 0
		if total > 0 {
			pct = int(100 * msg.update.Copy.GetBytesCopied() / total)
		}
		rb.latest = fmt.Sprintf("copying %d%% %s", pct, sanitizeLine(filepath.Base(msg.update.Copy.GetCurrentPath()), 0))
		m.setOpLatest(key, rb.latest)
	case msg.update.LogLine != "":
		// The rebuild's output is the sandbox runtime's, drawn as text: colour is
		// dropped and each \r progress redraw becomes a line of its own (FR-024,
		// FR-029).
		for _, line := range strings.Split(sanitizeTerminalText(msg.update.LogLine), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			rb.latest = line
			m.appendOpLog(key, line)
		}
	}
	if m.screen == screenOrchDetail && m.orchDetail.id == msg.id {
		m.orchDetail.status = "rebuilding… " + rb.latest
	}
	return m, waitForMsg(rb.ch)
}

func (m Model) handleReasoningDone(msg reasoningDoneMsg) (tea.Model, tea.Cmd) {
	delete(m.rebuilds, msg.id)
	key := reasoningLogKey(msg.id)
	m.finishOpLog(key, msg.err)
	onDetail := m.screen == screenOrchDetail && m.orchDetail.id == msg.id
	if msg.err != nil {
		m.err = msg.err
		m.status = "rebuild failed: " + msg.err.Error() + m.opLogHint(key)
		if onDetail {
			m.orchDetail.status = msg.err.Error()
		}
		return m, nil
	}
	m.err = nil
	m.status = "reasoning sandbox rebuilt: " + msg.sb.GetDisplayName()
	if onDetail {
		m.orchDetail.status = m.status
		return m, m.orchDetailLoadCmd()
	}
	return m, nil
}

// renderReasoningView shows the sandbox, its readiness and the saved setup.
func (m Model) renderReasoningView() []string {
	d := m.orchDetail
	sum := d.detail.GetSummary()
	w := d.reasoningWorker()
	name := "reasoning-" + short(d.id)
	if w != nil && w.GetDisplayName() != "" {
		name = sanitizeLine(w.GetDisplayName(), 32)
	}
	var lines []string
	switch rb, busy := m.rebuilds[d.id]; {
	case busy:
		lines = append(lines, name+"  "+agentWorkStyle.Render("rebuilding…")+dimStyle.Render("  "+rb.latest))
	case w != nil && sum.GetReasoningReady():
		line := name + "  " + stateBadge(w.GetState()) + " " + availabilityBadge(w.GetAvailability(), w.GetAgentStatus())
		if w.GetRunAction() != "" {
			line += dimStyle.Render("  (" + sanitizeLine(w.GetRunAction(), 40) + " · run " + sanitizeLine(w.GetRunId(), 0) + ")")
		}
		lines = append(lines, line)
	case sum.GetReasoningError() != "":
		lines = append(lines, name+"  "+statusErrStyle.Render("error: "+sanitizeLine(sum.GetReasoningError(), m.bodyWidth()-20)), dimStyle.Render("b rebuilds it"))
	default:
		lines = append(lines, name+"  "+agentWorkStyle.Render("coming up…"))
	}
	lines = append(lines, "", sectionStyle.Render("Setup"))
	for _, l := range reasoningSetupLines(sum.GetOrchestrator().GetReasoningSetup()) {
		lines = append(lines, m.fitLine("  "+sanitizeLine(l, 0)))
	}
	lines = append(lines, "", dimStyle.Render("e edit the setup (folders, K kits; enter rebuilds) · b rebuild with this setup · l output"))
	return lines
}
