package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// Per-view keys, forms and renderers of the orchestrator detail (feature 009):
// Actions (FR-129/FR-135–FR-140), Queue (FR-131), the mark-free dialog (FR-156),
// Events (FR-130/FR-157, user story 5) and Reasoning (FR-145–FR-147, user
// story 7). Forms follow the 008 huh pattern: heap-allocated values, ctrl+s
// saves, esc cancels; a daemon refusal is shown verbatim and keeps the form open.

// --- form value holders ---

// actionFormVals binds the action editor.
type actionFormVals struct {
	name, description, body, workerTag string
	kind                               string // "prompt" | "reason"
	args                               string // one per line: name [*] [description] [= default]
	original                           string // the name being edited ("" = new)
}

// callFormVals binds a call form: one value per declared argument.
type callFormVals struct {
	action *pb.Action
	values map[string]*string
}

// markFreeVals binds the mark-free dialog.
type markFreeVals struct {
	outcome   string // "completed" | "failed"
	note      string
	sandboxID string
	host      string // host entry id of the orchestrator's daemon
	orchID    string
}

// eventFormVals binds the timed-event editor.
type eventFormVals struct {
	id, name, action string
	intervalMinutes  string
	args             string // name = value, one per line
	enabled, dedupe  bool
}

// --- Actions view ---

func (m Model) updateActionsViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	actions := m.orchRec().GetActions()
	var cur *pb.Action
	if len(actions) > 0 {
		cur = actions[d.cursors[viewActions]]
	}
	switch msg.String() {
	case "a":
		return m.openActionForm(nil)
	case "e":
		if cur != nil {
			return m.openActionForm(cur)
		}
	case "d":
		if cur == nil {
			return m, nil
		}
		od := m.daemonForHost(d.host)
		id, name := d.id, cur.GetName()
		return m.enterConfirm(confirmState{
			title:    "Delete action " + name + "?",
			body:     []string{"Timed events calling it are disabled; queued runs keep their composed prompt."},
			returnTo: screenOrchDetail,
			onConfirm: func() tea.Msg {
				ctx, cancel := ctxTimeout()
				defer cancel()
				disabled, err := od.DeleteAction(ctx, id, name)
				if err != nil {
					return orchDoneMsg{err: err}
				}
				st := "deleted " + name
				if len(disabled) > 0 {
					st += " · disabled events: " + strings.Join(disabled, ", ")
				}
				return orchDoneMsg{status: st, reload: true}
			},
		})
	case "enter":
		if cur != nil {
			return m.openCallForm(cur)
		}
	}
	return m, nil
}

// actionKindOptions are the action types.
func actionKindOptions() []huh.Option[string] {
	return []huh.Option[string]{huh.NewOption("prompt — runs on a free worker", "prompt"), huh.NewOption("reason — runs on the reasoning sandbox", "reason")}
}

// openActionForm opens the editor for a new action or an existing one.
func (m Model) openActionForm(a *pb.Action) (tea.Model, tea.Cmd) {
	v := &actionFormVals{kind: "prompt"}
	if a != nil {
		v.name, v.description, v.body, v.workerTag, v.original = a.GetName(), a.GetDescription(), a.GetBody(), a.GetWorkerTag(), a.GetName()
		if a.GetType() == pb.ActionType_ACTION_TYPE_REASON {
			v.kind = "reason"
		}
		var lines []string
		for _, arg := range a.GetArguments() {
			lines = append(lines, argLine(arg))
		}
		v.args = strings.Join(lines, "\n")
	}
	d := &m.orchDetail
	d.formVals, d.formKind = v, "action"
	d.form = huh.NewForm(huh.NewGroup(
		huh.NewInput().Key("name").Title("Name").Description("lowercase letters, digits and hyphens; saving an existing name replaces it").Value(&v.name),
		huh.NewInput().Key("description").Title("Description").Value(&v.description),
		huh.NewSelect[string]().Key("kind").Title("Type").Options(actionKindOptions()...).Value(&v.kind),
		huh.NewText().Key("body").Title("Body").Lines(5).Description("{name} placeholders name declared arguments; {{ is a literal {").Value(&v.body),
		huh.NewInput().Key("tag").Title("Worker tag (prompt only)").Description("only workers tagged #<tag> are eligible; blank = any worker").Value(&v.workerTag),
		huh.NewText().Key("args").Title("Arguments").Lines(4).Description("one per line: name [*] [description] [= default]  — * marks required").Value(&v.args),
	)).WithTheme(huhTheme()).WithShowHelp(false).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(kitFormKeyMap())
	return m, d.form.Init()
}

// argNameRe mirrors the daemon's argument-name rule so a bad line is refused
// before the round trip.
var argNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// argLine renders a declared argument on one editor line.
func argLine(arg *pb.ActionArgument) string {
	line := arg.GetName()
	if arg.GetRequired() {
		line += " *"
	}
	if arg.GetDescription() != "" {
		line += " " + arg.GetDescription()
	}
	if arg.GetDefaultValue() != "" {
		line += " = " + arg.GetDefaultValue()
	}
	return line
}

// parseArgLines turns editor lines back into declared arguments.
func parseArgLines(text string) ([]*pb.ActionArgument, error) {
	var out []*pb.ActionArgument
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		arg := &pb.ActionArgument{}
		if i := strings.Index(line, " = "); i >= 0 {
			arg.DefaultValue = strings.TrimSpace(line[i+3:])
			line = strings.TrimSpace(line[:i])
		} else if strings.HasSuffix(line, " =") {
			line = strings.TrimSpace(strings.TrimSuffix(line, " ="))
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return nil, fmt.Errorf("argument line %q needs a name", raw)
		}
		if !argNameRe.MatchString(fields[0]) {
			return nil, fmt.Errorf("argument name %q must be letters, digits and underscores, not starting with a digit", fields[0])
		}
		arg.Name = fields[0]
		rest := fields[1:]
		if len(rest) > 0 && rest[0] == "*" {
			arg.Required = true
			rest = rest[1:]
		}
		arg.Description = strings.Join(rest, " ")
		out = append(out, arg)
	}
	return out, nil
}

// openCallForm builds a form from the action's declared arguments (FR-129).
func (m Model) openCallForm(a *pb.Action) (tea.Model, tea.Cmd) {
	v := &callFormVals{action: a, values: map[string]*string{}}
	var fields []huh.Field
	for _, arg := range a.GetArguments() {
		val := arg.GetDefaultValue()
		v.values[arg.GetName()] = &val
		title := arg.GetName()
		if arg.GetRequired() {
			title += " *"
		}
		fields = append(fields, huh.NewInput().Key(arg.GetName()).Title(title).Description(arg.GetDescription()).Value(v.values[arg.GetName()]))
	}
	if len(fields) == 0 {
		fields = append(fields, huh.NewNote().Title("No arguments").Description("ctrl+s calls "+a.GetName()))
	}
	d := &m.orchDetail
	d.formVals, d.formKind = v, "call "+a.GetName()
	d.form = huh.NewForm(huh.NewGroup(fields...)).WithTheme(huhTheme()).WithShowHelp(false).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(kitFormKeyMap())
	return m, d.form.Init()
}

// --- Queue view ---

// queueRows flattens the three groups into the view's row list.
func (m Model) queueRows() []*pb.Run {
	r := m.orchDetail.runs
	var out []*pb.Run
	out = append(out, r.GetQueued()...)
	out = append(out, r.GetInFlight()...)
	out = append(out, r.GetHistory()...)
	return out
}

func (m Model) updateQueueViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	rows := m.queueRows()
	if len(rows) == 0 {
		return m, nil
	}
	run := rows[d.cursors[viewQueue]]
	switch msg.String() {
	case "enter":
		od := m.daemonForHost(d.host)
		id, runID := d.id, run.GetId()
		return m, func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			got, err := od.GetRun(ctx, id, runID)
			if err != nil {
				return orchDoneMsg{err: err}
			}
			return runPromptMsg{run: got}
		}
	case "x":
		if run.GetState() != pb.RunState_RUN_STATE_QUEUED {
			d.status = "only a queued run can be cancelled"
			return m, nil
		}
		od := m.daemonForHost(d.host)
		id, runID := d.id, run.GetId()
		return m.enterConfirm(confirmState{
			title:    "Cancel run " + runID + "?",
			body:     []string{"It leaves the queue; nothing was delivered."},
			warning:  "The call is not retried.",
			returnTo: screenOrchDetail,
			onConfirm: func() tea.Msg {
				ctx, cancel := ctxTimeout()
				defer cancel()
				if _, err := od.CancelRun(ctx, id, runID); err != nil {
					return orchDoneMsg{err: err}
				}
				return orchDoneMsg{status: "cancelled " + runID, reload: true}
			},
		})
	case "m":
		if run.GetState() != pb.RunState_RUN_STATE_AWAITING_REVIEW {
			d.status = "only a run awaiting review can be marked free"
			return m, nil
		}
		return m.openMarkFreeForm(run.GetSandboxId(), d.host, d.id)
	}
	return m, nil
}

// runPromptMsg carries a run fetched for the prompt overlay.
type runPromptMsg struct{ run *pb.Run }

// --- mark free (FR-156) ---

// openMarkFreeForm opens the outcome + note dialog for a worker's reviewed run.
func (m Model) openMarkFreeForm(sandboxID, host, orchID string) (tea.Model, tea.Cmd) {
	v := &markFreeVals{outcome: "completed", sandboxID: sandboxID, host: host, orchID: orchID}
	d := &m.orchDetail
	d.formVals, d.formKind = v, "mark free"
	d.form = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Key("outcome").Title("Outcome").Options(huh.NewOption("completed", "completed"), huh.NewOption("failed", "failed")).Value(&v.outcome),
		huh.NewInput().Key("note").Title("Note").Description("optional; recorded as the run's summary").Value(&v.note),
	)).WithTheme(huhTheme()).WithShowHelp(false).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(kitFormKeyMap())
	return m, d.form.Init()
}

// markFreeKey is `m` on a sandbox row: while the worker's run awaits review
// the outcome dialog is sent to the controller's daemon; a worker parked
// "needs input" with no run on it is freed outright (its agent set back to
// idle), no dialog.
func (m Model) markFreeKey(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	c := sb.GetController()
	parked := c.GetRun() == nil && sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT
	if !c.GetRun().GetNeedsReview() && !parked {
		m.status = "no run awaiting review on this sandbox"
		return m, nil
	}
	orchHost, ok := m.entryForDaemonHost(c.GetHostId())
	if !ok {
		m.status = c.GetName() + "'s machine is not connected — mark free needs the orchestrator's daemon"
		return m, nil
	}
	if parked {
		return m.freeParkedWorker(sb.GetId(), sb.GetDisplayName(), orchHost, c.GetOrchestratorId())
	}
	// The dialog lives on the detail state but returns to the list.
	m.orchDetail.host = orchHost
	m.orchDetail.id = c.GetOrchestratorId()
	m.markFreeFromList = true
	m.screen = screenOrchDetail
	return m.openMarkFreeForm(sb.GetId(), orchHost, c.GetOrchestratorId())
}

// markFreeFromView is `m` in the Workers view.
func (m Model) markFreeFromView(w *pb.WorkerView) (tea.Model, tea.Cmd) {
	switch {
	case w.GetAvailability() == pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_REVIEW:
		return m.openMarkFreeForm(w.GetSandboxId(), m.orchDetail.host, m.orchDetail.id)
	case w.GetAvailability() == pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_INPUT && w.GetRunId() == "":
		return m.freeParkedWorker(w.GetSandboxId(), w.GetDisplayName(), m.orchDetail.host, m.orchDetail.id)
	}
	m.orchDetail.status = "that worker has no run awaiting review"
	return m, nil
}

// freeParkedWorker asks the orchestrator's daemon to set a worker that is
// parked needs-input with no run back to idle (MarkWorkerFree with nothing to
// close); the next queued run can then go to it.
func (m Model) freeParkedWorker(sandboxID, name, host, orchID string) (tea.Model, tea.Cmd) {
	md := m.daemonForHost(host)
	if name == "" {
		name = sandboxID
	}
	return m, func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := md.MarkWorkerFree(ctx, orchID, sandboxID, false, ""); err != nil {
			return orchDoneMsg{err: err}
		}
		return orchDoneMsg{status: "marked " + name + " free", reload: true}
	}
}

// --- Events view (user story 5) ---

func (m Model) updateEventsViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	events := m.orchRec().GetTimedEvents()
	var cur *pb.TimedEvent
	if len(events) > 0 {
		cur = events[d.cursors[viewEvents]]
	}
	switch msg.String() {
	case "a":
		return m.openEventForm(nil)
	case "e":
		if cur != nil {
			return m.openEventForm(cur)
		}
	case "d":
		if cur == nil {
			return m, nil
		}
		od := m.daemonForHost(d.host)
		id, evID, name := d.id, cur.GetId(), cur.GetName()
		return m.enterConfirm(confirmState{
			title:    "Delete event " + name + "?",
			body:     []string{"It stops firing; runs it already queued are untouched."},
			returnTo: screenOrchDetail,
			onConfirm: func() tea.Msg {
				ctx, cancel := ctxTimeout()
				defer cancel()
				if err := od.DeleteTimedEvent(ctx, id, evID); err != nil {
					return orchDoneMsg{err: err}
				}
				return orchDoneMsg{status: "deleted " + name, reload: true}
			},
		})
	case " ":
		if cur == nil {
			return m, nil
		}
		od := m.daemonForHost(d.host)
		id := d.id
		ev := cloneEvent(cur)
		ev.Enabled = !ev.GetEnabled()
		if ev.GetEnabled() {
			ev.DisabledReason = ""
		}
		verb := "disabled"
		if ev.GetEnabled() {
			verb = "enabled"
		}
		return m, func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			if _, err := od.PutTimedEvent(ctx, id, ev); err != nil {
				return orchDoneMsg{err: err}
			}
			return orchDoneMsg{status: verb + " " + ev.GetName(), reload: true}
		}
	}
	return m, nil
}

func cloneEvent(ev *pb.TimedEvent) *pb.TimedEvent {
	return proto.Clone(ev).(*pb.TimedEvent)
}

// openEventForm opens the timed-event editor (FR-130/FR-157).
func (m Model) openEventForm(ev *pb.TimedEvent) (tea.Model, tea.Cmd) {
	actions := m.orchRec().GetActions()
	if len(actions) == 0 {
		m.orchDetail.status = "add an action first — a timed event calls one"
		return m, nil
	}
	v := &eventFormVals{intervalMinutes: "5", enabled: true, dedupe: true, action: actions[0].GetName()}
	if ev != nil {
		v.id, v.name, v.action = ev.GetId(), ev.GetName(), ev.GetAction()
		v.intervalMinutes = strconv.Itoa(int(ev.GetIntervalSeconds()) / 60)
		v.enabled, v.dedupe = ev.GetEnabled(), !ev.GetDedupeDisabled()
		keys := make([]string, 0, len(ev.GetArguments()))
		for k := range ev.GetArguments() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var lines []string
		for _, k := range keys {
			lines = append(lines, k+" = "+ev.GetArguments()[k])
		}
		v.args = strings.Join(lines, "\n")
	}
	var opts []huh.Option[string]
	for _, a := range actions {
		opts = append(opts, huh.NewOption(a.GetName(), a.GetName()))
	}
	d := &m.orchDetail
	d.formVals, d.formKind = v, "event"
	d.form = huh.NewForm(huh.NewGroup(
		huh.NewInput().Key("name").Title("Name").Value(&v.name),
		huh.NewSelect[string]().Key("action").Title("Action").Options(opts...).Value(&v.action),
		huh.NewInput().Key("interval").Title("Every (minutes)").Description("at least 1").Value(&v.intervalMinutes),
		huh.NewText().Key("args").Title("Fixed arguments").Lines(3).Description("name = value, one per line (the action's declared arguments)").Value(&v.args),
		huh.NewConfirm().Key("enabled").Title("Enabled").Value(&v.enabled),
		huh.NewConfirm().Key("dedupe").Title("Dedupe").Description("skip a firing while a run of this event is queued or in flight").Value(&v.dedupe),
	)).WithTheme(huhTheme()).WithShowHelp(false).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(kitFormKeyMap())
	return m, d.form.Init()
}

// parseKV turns "name = value" lines into a map.
func parseKV(text string) (map[string]string, error) {
	out := map[string]string{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		k, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("argument line %q must be name = value", raw)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(val)
	}
	return out, nil
}

// --- form submission ---

// applyOrchDetailForm validates and sends whichever form a view opened; a
// daemon refusal keeps the form open with the text verbatim.
func (m Model) applyOrchDetailForm() (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	if d.form == nil {
		return m, nil
	}
	od := m.daemonForHost(d.host)
	id := d.id
	switch v := d.formVals.(type) {
	case *actionFormVals:
		args, err := parseArgLines(v.args)
		if err != nil {
			d.status = err.Error()
			return m, nil
		}
		kind := pb.ActionType_ACTION_TYPE_PROMPT
		if v.kind == "reason" {
			kind = pb.ActionType_ACTION_TYPE_REASON
		}
		a := &pb.Action{Name: strings.TrimSpace(v.name), Description: strings.TrimSpace(v.description), Type: kind, Body: v.body, WorkerTag: strings.TrimSpace(v.workerTag), Arguments: args}
		return m, func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			if _, err := od.PutAction(ctx, id, a); err != nil {
				return orchFormErrMsg{err: err}
			}
			return orchDoneMsg{status: "saved action " + a.GetName(), reload: true, closeForm: true}
		}
	case *callFormVals:
		args := map[string]string{}
		for _, arg := range v.action.GetArguments() {
			val := strings.TrimSpace(*v.values[arg.GetName()])
			if arg.GetRequired() && val == "" {
				d.status = "argument " + arg.GetName() + " is required"
				return m, nil
			}
			if val != "" {
				args[arg.GetName()] = val
			}
		}
		action := v.action.GetName()
		return m, func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			run, err := od.CallAction(ctx, id, action, args)
			if err != nil {
				return orchFormErrMsg{err: err}
			}
			return orchDoneMsg{status: fmt.Sprintf("queued %s at position %d", run.GetId(), run.GetPosition()), reload: true, closeForm: true}
		}
	case *markFreeVals:
		md := m.daemonForHost(v.host)
		failed := v.outcome == "failed"
		note, sbID, orchID := strings.TrimSpace(v.note), v.sandboxID, v.orchID
		return m, func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			run, err := md.MarkWorkerFree(ctx, orchID, sbID, failed, note)
			if err != nil {
				return orchFormErrMsg{err: err}
			}
			if run.GetId() == "" {
				// Nothing was awaiting review after all; the daemon freed the
				// parked worker instead.
				return orchDoneMsg{status: "marked the worker free", reload: true, closeForm: true}
			}
			return orchDoneMsg{status: fmt.Sprintf("closed %s as %s", run.GetId(), strings.ToLower(strings.TrimPrefix(run.GetState().String(), "RUN_STATE_"))), reload: true, closeForm: true}
		}
	case *eventFormVals:
		minutes, err := strconv.Atoi(strings.TrimSpace(v.intervalMinutes))
		if err != nil || minutes < 1 {
			d.status = "the interval must be a whole number of minutes, at least 1"
			return m, nil
		}
		args, err := parseKV(v.args)
		if err != nil {
			d.status = err.Error()
			return m, nil
		}
		ev := &pb.TimedEvent{Id: v.id, Name: strings.TrimSpace(v.name), Action: v.action, IntervalSeconds: uint32(minutes * 60), Arguments: args, Enabled: v.enabled, DedupeDisabled: !v.dedupe}
		return m, func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			if _, err := od.PutTimedEvent(ctx, id, ev); err != nil {
				return orchFormErrMsg{err: err}
			}
			return orchDoneMsg{status: "saved event " + ev.GetName(), reload: true, closeForm: true}
		}
	}
	return m.reasoningFormSubmit()
}

// orchFormErrMsg is a refusal of an open form: shown verbatim, form kept.
type orchFormErrMsg struct{ err error }

// --- renderers ---

func (m Model) renderActionsView(cursor func(int) string) []string {
	actions := m.orchRec().GetActions()
	if len(actions) == 0 {
		return orchEmpty("actions")
	}
	var lines []string
	for i, a := range actions {
		kind := "prompt"
		if a.GetType() == pb.ActionType_ACTION_TYPE_REASON {
			kind = "reason"
		}
		line := cursor(i) + selectedStyle.Render(a.GetName()) + dimStyle.Render(" ("+kind+")")
		if a.GetDescription() != "" {
			line += "  " + a.GetDescription()
		}
		if a.GetWorkerTag() != "" {
			line += "  " + tagBadgeStyle.Render("#"+a.GetWorkerTag())
		}
		var args []string
		for _, arg := range a.GetArguments() {
			n := arg.GetName()
			if arg.GetRequired() {
				n += "*"
			}
			args = append(args, n)
		}
		if len(args) > 0 {
			line += dimStyle.Render("  {" + strings.Join(args, ", ") + "}")
		}
		lines = append(lines, line)
	}
	return lines
}

// runLine renders one Queue row per group.
func runLine(r *pb.Run) string {
	kind := "user"
	switch r.GetEventKind() {
	case pb.EventKind_EVENT_KIND_TIMED:
		kind = "timed " + r.GetOriginLabel()
	case pb.EventKind_EVENT_KIND_TRIGGERED:
		kind = "triggered by " + r.GetOriginLabel()
	}
	state := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(r.GetState().String(), "RUN_STATE_"), "_", " "))
	switch r.GetState() {
	case pb.RunState_RUN_STATE_QUEUED:
		return fmt.Sprintf("%s  %s  %s  %s", dimStyle.Render(fmt.Sprintf("#%d", r.GetPosition())), selectedStyle.Render(r.GetAction()), dimStyle.Render(kind), dimStyle.Render("received "+relTime(r.GetReceivedAt())))
	case pb.RunState_RUN_STATE_RUNNING, pb.RunState_RUN_STATE_AWAITING_INPUT, pb.RunState_RUN_STATE_AWAITING_REVIEW:
		style := agentWorkStyle
		if r.GetState() != pb.RunState_RUN_STATE_RUNNING {
			style = agentWaitStyle
		}
		line := fmt.Sprintf("%s  %s  %s  %s", style.Render(state), selectedStyle.Render(r.GetAction()), dimStyle.Render("on "+short(r.GetSandboxId())), dimStyle.Render("started "+relTime(r.GetStartedAt())))
		if r.GetEventKind() != pb.EventKind_EVENT_KIND_USER {
			line += "  " + dimStyle.Render(kind)
		}
		return line
	default:
		style := agentIdleStyle
		if r.GetState() != pb.RunState_RUN_STATE_COMPLETED {
			style = statusErrStyle
		}
		detail := r.GetSummary()
		if r.GetClosedByDeveloper() {
			detail = "closed by developer: " + r.GetSummary()
		}
		if r.GetReason() != "" {
			if detail != "" {
				detail += " · "
			}
			detail += r.GetReason()
		}
		return fmt.Sprintf("%s  %s  %s", style.Render(state), selectedStyle.Render(r.GetAction()), dimStyle.Render(truncate(detail, 70)))
	}
}

// relTime renders a timestamp relative to now ("3m ago", "just now").
func relTime(ts interface{ AsTime() time.Time }) string {
	if ts == nil {
		return "—"
	}
	d := time.Since(ts.AsTime())
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return ts.AsTime().Format("Jan 2 15:04")
}

func (m Model) renderQueueView(cursor func(int) string) []string {
	r := m.orchDetail.runs
	if len(r.GetQueued())+len(r.GetInFlight())+len(r.GetHistory()) == 0 {
		return orchEmpty("runs")
	}
	var lines []string
	i := 0
	group := func(title string, runs []*pb.Run) {
		if len(runs) == 0 {
			return
		}
		lines = append(lines, sectionStyle.Render(title))
		for _, run := range runs {
			lines = append(lines, cursor(i)+runLine(run))
			i++
		}
	}
	group("Queued", r.GetQueued())
	group("In progress", r.GetInFlight())
	group("History", r.GetHistory())
	return lines
}

func (m Model) renderEventsView(cursor func(int) string) []string {
	events := m.orchRec().GetTimedEvents()
	if len(events) == 0 {
		return orchEmpty("events")
	}
	waiting := map[string]int{}
	for _, run := range m.orchDetail.runs.GetQueued() {
		if run.GetEventKind() == pb.EventKind_EVENT_KIND_TIMED {
			waiting[run.GetOrigin()]++
		}
	}
	var lines []string
	for i, ev := range events {
		onOff := statusOKStyle.Render("[on]")
		if !ev.GetEnabled() {
			onOff = dimStyle.Render("[off]")
		}
		line := fmt.Sprintf("%s%s  %s  %s  %s  %s  %s", cursor(i), onOff, selectedStyle.Render(ev.GetName()), dimStyle.Render("→ "+ev.GetAction()),
			dimStyle.Render(fmt.Sprintf("every %dm", ev.GetIntervalSeconds()/60)), dimStyle.Render("next "+nextFire(ev)),
			dimStyle.Render(fmt.Sprintf("waiting %d · skipped %d", waiting[ev.GetId()], ev.GetSkippedCount())))
		if !ev.GetDedupeDisabled() {
			line += "  " + tagBadgeStyle.Render("dedupe")
		}
		if ev.GetDisabledReason() != "" {
			line += "  " + statusErrStyle.Render("disabled: "+ev.GetDisabledReason())
		}
		lines = append(lines, line)
	}
	return lines
}

// nextFire renders when an event fires next, relative to now.
func nextFire(ev *pb.TimedEvent) string {
	if !ev.GetEnabled() || ev.GetNextFireAt() == nil {
		return "—"
	}
	d := time.Until(ev.GetNextFireAt().AsTime())
	switch {
	case d <= 0:
		return "now"
	case d < time.Minute:
		return "in <1m"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	}
	return "in " + d.Round(time.Minute).String()
}

var _ = lipgloss.Left
