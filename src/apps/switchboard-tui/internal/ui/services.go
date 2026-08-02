package ui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/forward"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// servicesState backs the per-sandbox service screen (feature 006, FR-045).
//
// This is the ONLY surface for starting and stopping services — there is no global
// cross-sandbox screen (clarification Q2). The sandbox list carries a running-count
// indicator so "what's up right now?" is still answerable at a glance.
type servicesState struct {
	sandboxID string
	sandbox   string
	host      string
	list      list.Model
	detail    string // captured output / failure detail of the highlighted service
	status    string
}

// enterServices opens the service screen for a sandbox and loads its declared set.
func (m Model) enterServices(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	m.servicesView = servicesState{
		sandboxID: sb.GetId(),
		sandbox:   sb.GetDisplayName(),
		host:      host,
		list:      newItemList("Services", "service", "services", m.bodyWidth(), m.bodyHeight()),
	}
	m.screen = screenServices
	return m, m.listServicesCmd(m.daemonForHost(host), sb.GetId())
}

// listServicesCmd fetches a sandbox's declared services and their instance state.
func (m Model) listServicesCmd(d Daemon, sandboxID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		rows, err := d.ListServices(ctx, sandboxID)
		if err != nil {
			return errMsg{err}
		}
		return servicesLoadedMsg{sandboxID: sandboxID, services: rows}
	}
}

// servicesLoadedMsg carries the loaded service rows back into Update.
type servicesLoadedMsg struct {
	sandboxID string
	services  []*pb.SandboxService
}

// applyServices populates the screen from loaded rows, in declaration order.
func (m Model) applyServices(msg servicesLoadedMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenServices || m.servicesView.sandboxID != msg.sandboxID {
		return m, nil
	}
	items := make([]list.Item, 0, len(msg.services))
	for _, row := range msg.services {
		items = append(items, serviceItem(row))
	}
	m.servicesView.list.SetItems(items)
	// A reconnect (or a first load after one) hands back services the daemon still
	// has running; give them listeners on this machine again (US5).
	m = m.reestablishForwards(msg.services, msg.sandboxID)
	return m, nil
}

// serviceItem renders one declared service and whatever its instance is doing.
func serviceItem(row *pb.SandboxService) listItem {
	d, inst := row.GetDeclared(), row.GetInstance()
	title := serviceStateIcon(inst.GetState()) + " " + d.GetName() +
		dimStyle.Render("  "+serviceStateText(inst.GetState()))
	if addr := localAddress(inst); addr != "" {
		title += "  " + lipgloss.NewStyle().Foreground(colRunning).Render(addr)
	}
	desc := truncate(d.GetCommand(), 56) + dimStyle.Render(fmt.Sprintf("  :%d %s", d.GetListenPort(), serviceLocation(d)))
	return listItem{
		id:      d.GetName(),
		title:   title,
		desc:    desc,
		filter:  d.GetName(),
		payload: row,
	}
}

// localAddress is the address on the DEVELOPER'S machine, shown only while the
// service is actually reachable. A stopped or failed service must never leave an
// address on screen that no longer works (FR-050).
func localAddress(inst *pb.ServiceInstance) string {
	if inst.GetState() != pb.ServiceState_SERVICE_STATE_RUNNING || inst.GetLocalPort() == 0 {
		return ""
	}
	return fmt.Sprintf("127.0.0.1:%d", inst.GetLocalPort())
}

func serviceLocation(d *pb.KitService) string {
	if d.GetLocation() == pb.ServiceLocation_SERVICE_LOCATION_ON_HOST {
		return "on host"
	}
	return "in sandbox"
}

func serviceStateText(st pb.ServiceState) string {
	switch st {
	case pb.ServiceState_SERVICE_STATE_STARTING:
		return "starting"
	case pb.ServiceState_SERVICE_STATE_RUNNING:
		return "running"
	case pb.ServiceState_SERVICE_STATE_FAILED:
		return "failed"
	default:
		return "stopped"
	}
}

func serviceStateIcon(st pb.ServiceState) string {
	switch st {
	case pb.ServiceState_SERVICE_STATE_RUNNING:
		return lipgloss.NewStyle().Foreground(colRunning).Render("●")
	case pb.ServiceState_SERVICE_STATE_STARTING:
		return lipgloss.NewStyle().Foreground(colWarn).Render("◆")
	case pb.ServiceState_SERVICE_STATE_FAILED:
		return lipgloss.NewStyle().Foreground(colError).Render("✗")
	default:
		return dimStyle.Render("○")
	}
}

func (m Model) servicesHelp() helpBindings {
	return helpBindings{
		hkey("↑/↓", "select"),
		hkey("s", "start/stop"),
		hkey("o", "open"),
		hkey("enter", "output"),
		hkey("esc", "back"),
	}
}

// updateServicesKey drives the screen: s toggles start/stop, o opens (or copies)
// the local address, enter shows the captured output.
func (m Model) updateServicesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.servicesView = servicesState{}
		m.screen = screenList
		return m, nil
	case "s":
		return m.toggleService()
	case "o":
		return m.openService()
	case "enter":
		if row, ok := m.selectedService(); ok {
			m.servicesView.detail = serviceDetail(row)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.servicesView.list, cmd = m.servicesView.list.Update(msg)
	return m, cmd
}

// selectedService returns the highlighted row.
func (m Model) selectedService() (*pb.SandboxService, bool) {
	it, ok := m.servicesView.list.SelectedItem().(listItem)
	if !ok {
		return nil, false
	}
	row, ok := it.payload.(*pb.SandboxService)
	return row, ok
}

// toggleService starts a stopped service or stops a live one. Nothing starts
// without this deliberate keystroke (FR-045).
func (m Model) toggleService() (tea.Model, tea.Cmd) {
	row, ok := m.selectedService()
	if !ok {
		return m, nil
	}
	d := m.daemonForHost(m.servicesView.host)
	sandboxID, name := m.servicesView.sandboxID, row.GetDeclared().GetName()
	starting := IsTerminalServiceState(row.GetInstance().GetState())

	m.servicesView.status = ""
	return m, func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		var err error
		if starting {
			_, err = d.StartService(ctx, sandboxID, name)
		} else {
			_, err = d.StopService(ctx, sandboxID, name)
		}
		if err != nil {
			return errMsg{err}
		}
		return servicesRefreshMsg{sandboxID: sandboxID}
	}
}

// servicesRefreshMsg asks the screen to reload after a start/stop.
type servicesRefreshMsg struct{ sandboxID string }

// openService opens a running website service in the browser, or shows a copyable
// address for anything else (FR-050).
func (m Model) openService() (tea.Model, tea.Cmd) {
	row, ok := m.selectedService()
	if !ok {
		return m, nil
	}
	addr := localAddress(row.GetInstance())
	if addr == "" {
		m.servicesView.status = "service is not running"
		return m, nil
	}
	if !row.GetDeclared().GetIsWebsite() {
		// Not a website: there is nothing sensible to hand a browser, so show the
		// address instead. A database or gRPC service is fully forwarded — only the
		// browser action is withheld.
		m.servicesView.status = "address: " + addr + " (copy it)"
		return m, nil
	}
	url := "http://" + addr
	if err := openBrowser(url); err != nil {
		m.servicesView.status = "could not open a browser — " + url
		return m, nil
	}
	m.servicesView.status = "opened " + url
	return m, nil
}

// browserRunner launches the URL opener. Overridable in tests so opening a service
// does not spawn a real browser.
var browserRunner = func(cmd *exec.Cmd) error { return cmd.Start() }

// openBrowser opens a URL with the platform's default handler.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return browserRunner(cmd)
}

// serviceDetail renders the highlighted service's diagnostics: where it runs, its
// address, why it failed, and its captured output (FR-051).
func serviceDetail(row *pb.SandboxService) string {
	d, inst := row.GetDeclared(), row.GetInstance()
	var b strings.Builder
	b.WriteString(dimStyle.Render("command: ") + d.GetCommand() + "\n")
	b.WriteString(dimStyle.Render("runs:    ") + serviceLocation(d) +
		dimStyle.Render(fmt.Sprintf(" · listens on :%d", d.GetListenPort())) + "\n")
	b.WriteString(dimStyle.Render("state:   ") + serviceStateText(inst.GetState()))
	if addr := localAddress(inst); addr != "" {
		b.WriteString(dimStyle.Render("  at ") + addr)
	}
	b.WriteString("\n")
	if detail := inst.GetFailureDetail(); detail != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colError).Render("reason:  "+detail) + "\n")
	}
	if out := inst.GetOutput(); out != "" {
		label := "output:"
		if inst.GetOutputTruncated() {
			label = "output (truncated — showing the most recent):"
		}
		b.WriteString(dimStyle.Render(label) + "\n" + out)
	}
	return b.String()
}

// viewServices renders the list, the selected service's detail, and any status line.
func (m Model) viewServices() string {
	header := sectionStyle.Render("Services · "+m.servicesView.sandbox) + "\n"
	body := m.servicesView.list.View()
	if m.servicesView.status != "" {
		body += "\n" + dimStyle.Render(m.servicesView.status)
	}
	if m.servicesView.detail != "" {
		body += "\n\n" + m.servicesView.detail
	}
	return header + body
}

// IsTerminalServiceState reports whether a service is at rest (so `s` starts it
// rather than stopping it). An unset instance — never started this session — counts
// as stopped.
func IsTerminalServiceState(st pb.ServiceState) bool {
	switch st {
	case pb.ServiceState_SERVICE_STATE_RUNNING, pb.ServiceState_SERVICE_STATE_STARTING:
		return false
	}
	return true
}

// runningServicesBadge renders the sandbox-list indicator for a sandbox with live
// services (FR-045). This is the only cross-sandbox surface the feature adds.
func (m Model) runningServicesBadge(sandboxID string) string {
	n := 0
	for _, inst := range m.serviceInstances {
		if inst.GetSandboxId() == sandboxID && inst.GetState() == pb.ServiceState_SERVICE_STATE_RUNNING {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(colRunning).Render(fmt.Sprintf("⇄ %d", n))
}

// handleServiceInstance folds a live instance update into the model and keeps this
// machine's listeners in step with it (FR-052).
//
// The forward is opened when a service becomes reachable and closed the moment it
// is not, which is what keeps the displayed address honest: a stopped or crashed
// service loses its address in the same update that changes its state, rather than
// leaving a dead `127.0.0.1:…` on screen.
func (m Model) handleServiceInstance(inst *pb.ServiceInstance) (tea.Model, tea.Cmd) {
	if m.serviceInstances == nil {
		m.serviceInstances = map[string]*pb.ServiceInstance{}
	}
	rearm := func() tea.Cmd {
		if m.sub != nil {
			return m.recvCmd(m.sub)
		}
		return nil
	}

	switch inst.GetState() {
	case pb.ServiceState_SERVICE_STATE_RUNNING:
		if m.forwards != nil && inst.GetLocalPort() == 0 {
			// Reachable on the daemon side: give it an address on this machine.
			if port, err := m.forwards.Open(m.openerFor(inst.GetSandboxId()), inst.GetSandboxId(), inst.GetId()); err == nil {
				inst = cloneWithLocalPort(inst, port)
			} else {
				m.servicesView.status = "no free local port: " + err.Error()
			}
		}
	default:
		if m.forwards != nil {
			m.forwards.Close(inst.GetId())
		}
	}

	m.serviceInstances[inst.GetId()] = inst
	m.refreshListItems() // the sandbox-row running indicator

	if m.screen == screenServices && m.servicesView.sandboxID == inst.GetSandboxId() {
		return m, tea.Batch(rearm(), m.listServicesCmd(m.daemonForHost(m.servicesView.host), inst.GetSandboxId()))
	}
	return m, rearm()
}

// cloneWithLocalPort returns a copy of inst carrying the port this machine bound.
// The daemon learns the same port from the relay's `open` frame, so both sides
// agree on the address without an extra round trip.
func cloneWithLocalPort(inst *pb.ServiceInstance, port uint32) *pb.ServiceInstance {
	out := proto.Clone(inst).(*pb.ServiceInstance)
	out.LocalPort = port
	return out
}

// openerFor returns the relay opener for a sandbox's host. Forwards ride the
// connection the client already holds, so a remote-host sandbox needs no second SSH
// session (research R1).
func (m Model) openerFor(sandboxID string) forward.Opener {
	host := m.activeHost
	for _, sb := range m.sandboxes {
		if sb.GetId() == sandboxID && sb.GetHostId() != "" {
			host = sb.GetHostId()
			break
		}
	}
	if d, ok := m.daemonForHost(host).(forward.Opener); ok {
		return d
	}
	return nil
}

// hostDisconnected tears down every forward belonging to a host and marks its
// running services unreachable (FR-050, US5-2).
//
// The SERVICES are untouched: they are daemon-owned and keep running on the far
// side. What is lost is only this machine's access path, and saying so plainly
// beats leaving a `127.0.0.1:…` on screen that silently goes nowhere.
func (m Model) hostDisconnected(hostID string) Model {
	if m.forwards == nil {
		return m
	}
	for id, inst := range m.serviceInstances {
		if !m.instanceOnHost(inst, hostID) {
			continue
		}
		m.forwards.Close(id)
		if inst.GetState() == pb.ServiceState_SERVICE_STATE_RUNNING {
			unreachable := proto.Clone(inst).(*pb.ServiceInstance)
			unreachable.State = pb.ServiceState_SERVICE_STATE_FAILED
			unreachable.FailureReason = pb.ServiceFailureReason_SERVICE_FAILURE_REASON_HOST_UNREACHABLE
			unreachable.FailureDetail = "the connection to this sandbox's host was lost; the service is still running there"
			unreachable.LocalPort = 0
			m.serviceInstances[id] = unreachable
		}
	}
	m.refreshListItems()
	return m
}

// instanceOnHost reports whether an instance's sandbox lives on hostID.
func (m Model) instanceOnHost(inst *pb.ServiceInstance, hostID string) bool {
	for _, sb := range m.sandboxes {
		if sb.GetId() == inst.GetSandboxId() {
			return sb.GetHostId() == hostID || (sb.GetHostId() == "" && hostID == m.activeHost)
		}
	}
	return false
}

// reestablishForwards re-opens listeners for services the daemon still reports as
// RUNNING after a reconnect.
//
// The local port may differ from the one the developer saw before — the client is
// the allocator, and it is starting fresh. That is the documented trade-off of
// R1's design; the service itself was never restarted.
func (m Model) reestablishForwards(rows []*pb.SandboxService, sandboxID string) Model {
	if m.forwards == nil {
		return m
	}
	for _, row := range rows {
		inst := row.GetInstance()
		if inst.GetState() != pb.ServiceState_SERVICE_STATE_RUNNING {
			continue
		}
		if m.forwards.Port(inst.GetId()) != 0 {
			continue // already forwarded
		}
		if port, err := m.forwards.Open(m.openerFor(sandboxID), sandboxID, inst.GetId()); err == nil {
			m.serviceInstances[inst.GetId()] = cloneWithLocalPort(inst, port)
		}
	}
	return m
}
