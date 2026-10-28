package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// Forms and operations on an orchestrator's actions and timed events (feature
// 009: FR-129/FR-130/FR-135–FR-140/FR-157), the mark-free dialog (FR-156) and
// Enqueue now (SWI-4). Since feature 010 (research R1) each acts on an explicit
// orchTarget and reports on the surface it was started from: the Jobs Actions and
// Events tabs and the Overview's Upcoming rows use them for any orchestrator.
// Forms follow the 008 huh pattern: heap-allocated values, ctrl+s saves, esc
// cancels; a daemon refusal is shown verbatim and keeps the form open.

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
	runID     string // the run awaiting review the dialog was opened for
	host      string // host entry id of the orchestrator's daemon
	orchID    string
}

// eventFormVals binds the timed-event editor.
type eventFormVals struct {
	id, name, action string
	intervalMinutes  string
	args             string // name = value, one per line
	enabled, dedupe  bool
	// The active window (SWI-10, event_window.go): both times blank = around
	// the clock; days empty = every day.
	activeFrom, activeUntil string
	days                    []int // 0=Sunday … 6=Saturday
	timezone                string
}

// --- actions ---

// actionKindOptions are the action types.
func actionKindOptions() []huh.Option[string] {
	return []huh.Option[string]{huh.NewOption("prompt — runs on a free worker", "prompt"), huh.NewOption("reason — runs on the reasoning sandbox", "reason")}
}

// openActionForm opens the editor for a new action (a == nil) or an existing one
// on the target orchestrator.
func (m Model) openActionForm(s orchSurface, t orchTarget, a *pb.Action) (tea.Model, tea.Cmd) {
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
	return m, m.setOpForm(s, t, "action", v, func(m Model, spec *formSpec) *huh.Form {
		return m.newOrchForm(spec,
			spec.input("name", "Name").Description("lowercase letters, digits and hyphens; saving an existing name replaces it").Value(&v.name),
			spec.input("description", "Description").Value(&v.description),
			huh.NewSelect[string]().Key("kind").Title("Type").Options(actionKindOptions()...).Value(&v.kind),
			spec.text(&v.body, "Body").Key("body").Lines(5).Description("{name} placeholders name declared arguments; {{ is a literal {"),
			spec.input("tag", "Worker tag (prompt only)").Description("only workers tagged #<tag> are eligible; blank = any worker").Value(&v.workerTag),
			spec.text(&v.args, "Arguments").Key("args").Lines(4).Description("one per line: name [*] [description] [= default]  — * marks required"),
		)
	})
}

// confirmDeleteAction gates deleting an action of the target orchestrator.
func (m Model) confirmDeleteAction(s orchSurface, t orchTarget, a *pb.Action) (tea.Model, tea.Cmd) {
	od, ok := m.requireDaemon(s, t.host)
	if !ok {
		return m, nil
	}
	id, name := t.id, a.GetName()
	return m.enterConfirm(confirmState{
		title:    "Delete action " + name + "?",
		body:     []string{"On " + sanitizeLine(t.label(), 0) + ".", "Timed events calling it are disabled; queued runs keep their composed prompt."},
		returnTo: surfaceScreen(s),
		onConfirm: func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			disabled, err := od.DeleteAction(ctx, id, name)
			if err != nil {
				return orchDoneMsg{err: err, sink: s}
			}
			st := "deleted " + name
			if len(disabled) > 0 {
				st += " · disabled events: " + strings.Join(disabled, ", ")
			}
			return orchDoneMsg{status: st, reload: true, sink: s, target: t}
		},
	})
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
func (m Model) openCallForm(s orchSurface, t orchTarget, a *pb.Action) (tea.Model, tea.Cmd) {
	v := &callFormVals{action: a, values: map[string]*string{}}
	for _, arg := range a.GetArguments() {
		val := arg.GetDefaultValue()
		v.values[arg.GetName()] = &val
	}
	return m, m.setOpForm(s, t, "call "+a.GetName(), v, func(m Model, spec *formSpec) *huh.Form {
		var fields []huh.Field
		for _, arg := range a.GetArguments() {
			name := sanitizeLine(arg.GetName(), 0)
			title := name
			if arg.GetRequired() {
				title += " *"
			}
			fields = append(fields, spec.input(arg.GetName(), name).Title(title).Description(sanitizeTerminalText(arg.GetDescription())).Value(v.values[arg.GetName()]))
		}
		if len(fields) == 0 {
			fields = append(fields, huh.NewNote().Title("No arguments").Description("ctrl+s calls "+sanitizeLine(a.GetName(), 0)))
		}
		return m.newOrchForm(spec, fields...)
	})
}

// --- timed events (user story 5 of feature 009) ---

// confirmDeleteEvent gates deleting a timed event of the target orchestrator.
func (m Model) confirmDeleteEvent(s orchSurface, t orchTarget, ev *pb.TimedEvent) (tea.Model, tea.Cmd) {
	od, ok := m.requireDaemon(s, t.host)
	if !ok {
		return m, nil
	}
	id, evID, name := t.id, ev.GetId(), ev.GetName()
	return m.enterConfirm(confirmState{
		title:    "Delete event " + name + "?",
		body:     []string{"On " + sanitizeLine(t.label(), 0) + ".", "It stops firing; runs it already queued are untouched."},
		returnTo: surfaceScreen(s),
		onConfirm: func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			if err := od.DeleteTimedEvent(ctx, id, evID); err != nil {
				return orchDoneMsg{err: err, sink: s}
			}
			return orchDoneMsg{status: "deleted " + name, reload: true, sink: s, target: t}
		},
	})
}

// toggleEventCmd flips a timed event's enabled flag on the target orchestrator.
func (m Model) toggleEventCmd(s orchSurface, t orchTarget, cur *pb.TimedEvent) tea.Cmd {
	od, err := m.connectedDaemon(t.host)
	if err != nil {
		return errCmd(s, err)
	}
	id := t.id
	ev := cloneEvent(cur)
	ev.Enabled = !ev.GetEnabled()
	if ev.GetEnabled() {
		ev.DisabledReason = ""
	}
	verb := "disabled"
	if ev.GetEnabled() {
		verb = "enabled"
	}
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := od.PutTimedEvent(ctx, id, ev); err != nil {
			return orchDoneMsg{err: err, sink: s}
		}
		return orchDoneMsg{status: verb + " " + ev.GetName(), reload: true, sink: s, target: t}
	}
}

// enqueueNowKey is Enqueue now (SWI-4) on a timed event of the target
// orchestrator: one run as this event, schedule untouched. No confirm: it only
// queues work, and dedupe refuses a duplicate — except with dedupe off and a run
// of the event already open, which really would double up (research R9). runs
// are the orchestrator's runs as last loaded (they only decide whether to ask).
func (m Model) enqueueNowKey(s orchSurface, t orchTarget, ev *pb.TimedEvent, runs *pb.ListRunsResponse) (tea.Model, tea.Cmd) {
	op := m.opState(s)
	key := t.key()
	if m.orchFires[key] != 0 {
		op.status = enqueueInProgressText
		return m, nil
	}
	// Terminal auto-repeat (or a double tap) right after a SUCCESSFUL fire is
	// dropped: the success message in the footer stays instead of being replaced
	// by a dedupe refusal, and a dedupe-off event cannot be flooded by a held key.
	// The hint goes on the surface's own status line, which does not overwrite it.
	if left := enqueueRepeatWindow - time.Since(op.lastFireAt); op.lastFireKey == key+"/"+ev.GetId() && left > 0 {
		// The hint lasts only as long as the window it describes; the first
		// dropped press owns the expiry timer (a held key schedules one, not many).
		if op.status == enqueueRepeatText {
			return m, nil
		}
		op.status, op.hintKey = enqueueRepeatText, key
		return m, tea.Tick(left, func(time.Time) tea.Msg { return orchHintExpiredMsg{sink: s, orch: key, text: enqueueRepeatText} })
	}
	fire := orchFireStartMsg{sink: s, target: t, evID: ev.GetId(), name: ev.GetName(), disabled: !ev.GetEnabled()}
	if open := openRunOfEvent(runs, ev.GetId()); ev.GetDedupeDisabled() && open != nil {
		return m.enterConfirm(confirmState{
			title:     "Enqueue " + fire.name + " now?",
			body:      []string{"On " + sanitizeLine(t.label(), 0) + ".", "Dedupe is off and run " + open.GetId() + " of this event is still open,", "so this queues another one alongside it."},
			warning:   "Another run will be queued; the schedule is not changed.",
			returnTo:  surfaceScreen(s),
			onConfirm: func() tea.Msg { return fire },
		})
	}
	return m.startOrchFire(fire)
}

// orchFireStartMsg is an accepted Enqueue now (directly, or after the dedupe-off
// confirmation): record it in flight, then call the daemon.
type orchFireStartMsg struct {
	sink       orchSurface
	target     orchTarget
	evID, name string
	disabled   bool
}

// enqueueRepeatWindow drops a repeat Enqueue now for the same event this soon
// after one started (terminal auto-repeat is ~30 ms; a deliberate second press is
// slower). A package constant, never an env var.
const enqueueRepeatWindow = 500 * time.Millisecond

// The Enqueue now hints on a surface's status line; both are transient.
const (
	enqueueInProgressText = "an enqueue is already in progress"
	enqueueRepeatText     = "repeat ignored — press n again in a moment to queue another"
)

// orchHintExpiredMsg clears a transient surface-line hint once it no longer
// applies, unless something else has replaced it meanwhile. It names the
// orchestrator it was scheduled for, so a timer for one orchestrator cannot clear
// the identical hint since shown for another.
type orchHintExpiredMsg struct {
	sink       orchSurface
	orch, text string // orch is the target key
}

// fireTokens hands out the per-fire tokens. Process-wide so a token is never
// reused. Only uniqueness matters: nothing may depend on a token's value or order.
var fireTokens atomic.Uint64

// openRunOfEvent is a queued or in-flight run created by this timed event, as last
// loaded (nil when none is known). The daemon stays the authority — this only
// decides whether to ask first. The list can lag a moment behind other clients,
// and the run it names may have just finished; both only affect the prompt.
func openRunOfEvent(r *pb.ListRunsResponse, eventID string) *pb.Run {
	for _, group := range [][]*pb.Run{r.GetQueued(), r.GetInFlight()} {
		for _, run := range group {
			if run.GetEventKind() == pb.EventKind_EVENT_KIND_TIMED && run.GetOrigin() == eventID {
				return run
			}
		}
	}
	return nil
}

// startOrchFire calls FireTimedEvent unless a fire for the same orchestrator is
// already in flight. The in-flight record lives on the model (orchFires, keyed by
// the target), so it holds across every surface and tab (research R9).
func (m Model) startOrchFire(f orchFireStartMsg) (tea.Model, tea.Cmd) {
	op := m.opState(f.sink)
	key := f.target.key()
	if m.orchFires[key] != 0 {
		op.status = enqueueInProgressText
		return m, nil
	}
	od, ok := m.requireDaemon(f.sink, f.target.host)
	if !ok {
		return m, nil
	}
	tok := fireTokens.Add(1)
	m.orchFires[key] = tok
	op.status = ""
	t := f.target
	return m, func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		run, err := od.FireTimedEvent(ctx, t.id, f.evID)
		if err != nil {
			return orchDoneMsg{err: err, sink: f.sink, fire: true, fireToken: tok, fireKey: key, fireLabel: t.label()}
		}
		// The position is where the run queued (the daemon reads it before dispatch),
		// not a live one: a free worker may already be starting it.
		detail := run.GetId()
		if run.GetPosition() > 0 {
			detail = fmt.Sprintf("%s, queued at position %d", run.GetId(), run.GetPosition())
		}
		status := fmt.Sprintf("enqueued %s on %s (%s)", f.name, t.label(), detail)
		if f.disabled {
			status += " — the event is disabled; its schedule is unchanged"
		}
		return orchDoneMsg{status: status, reload: true, sink: f.sink, target: t, fire: true, fireToken: tok, fireKey: key, fireLabel: t.label(), fireEvent: f.evID}
	}
}

func cloneEvent(ev *pb.TimedEvent) *pb.TimedEvent {
	return proto.Clone(ev).(*pb.TimedEvent)
}

// openEventForm opens the timed-event editor (FR-130/FR-157) for a new event (ev
// == nil) or an existing one on the target orchestrator.
func (m Model) openEventForm(s orchSurface, t orchTarget, ev *pb.TimedEvent) (tea.Model, tea.Cmd) {
	actions := t.rec.GetActions()
	if len(actions) == 0 {
		m.opState(s).status = "add an action to " + t.label() + " first — a timed event calls one"
		return m, nil
	}
	v := &eventFormVals{intervalMinutes: "5", enabled: true, dedupe: true, action: actions[0].GetName(), timezone: localZoneName()}
	if ev != nil {
		windowToForm(v, ev.GetWindow())
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
	return m, m.setOpForm(s, t, "event", v, func(m Model, spec *formSpec) *huh.Form {
		return m.newOrchForm(spec,
			spec.input("name", "Name").Value(&v.name),
			huh.NewSelect[string]().Key("action").Title("Action").Options(opts...).Value(&v.action),
			spec.input("interval", "Every (minutes)").Description("at least 1").Value(&v.intervalMinutes),
			spec.input("activeFrom", "Active from").Description("HH:MM, 24-hour — blank: around the clock").Value(&v.activeFrom),
			spec.input("activeUntil", "Active until").Description("HH:MM, exclusive; 24:00 = midnight; earlier than from = overnight").Value(&v.activeUntil),
			huh.NewMultiSelect[int]().Key("days").Title("Days").Description("none: every day (an overnight window's day is the day it opens)").Options(dayOptions()...).Value(&v.days),
			spec.input("timezone", "Timezone").Description("IANA name, e.g. America/Los_Angeles (not PST/EST)").Value(&v.timezone),
			spec.text(&v.args, "Fixed arguments").Key("args").Lines(3).Description("name = value, one per line (the action's declared arguments)"),
			huh.NewConfirm().Key("enabled").Title("Enabled").Value(&v.enabled),
			huh.NewConfirm().Key("dedupe").Title("Dedupe").Description("skip a firing while a run of this event is queued or in flight").Value(&v.dedupe),
		)
	})
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

// advanceOrchOpForm feeds a message to a surface's open form, submitting or
// dropping it when the form completes or aborts.
func (m Model) advanceOrchOpForm(s orchSurface, msg tea.Msg) (tea.Model, tea.Cmd) {
	op := m.opState(s)
	if op.form == nil || op.formSaving {
		return m, nil // in flight: a completed form must not submit again per message
	}
	f, cmd := op.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		op.form = ff
	}
	switch op.form.State {
	case huh.StateCompleted:
		// A submit that sent its call (cmd) waits for the reply with the form
		// dead; only a refusal before sending revives it here (the reply's
		// refusal does in applyOrchFormErr).
		mm, cmd := m.submitOrchOpForm(s)
		if cmd != nil {
			return mm, cmd
		}
		return reviveCompleted(opGuard(s))(mm, nil)
	}
	return m, cmd
}

// submitOrchOpForm validates and sends whichever form a surface opened, to the
// orchestrator the form was opened for; a daemon refusal keeps the form open with
// the text verbatim.
// savingText is an op form's status while its call is in flight.
const savingText = "saving…"

// formCallSeq numbers op-form calls, so each reply names the call it answers.
var formCallSeq atomic.Uint64

// submitOrchOpForm sends the open form's call once: while it is in flight the
// form is marked saving and nothing sends it again. The reply is stamped with
// the call's token, the only reply that may clear the mark or close the form.
func (m Model) submitOrchOpForm(s orchSurface) (tea.Model, tea.Cmd) {
	op := m.opState(s)
	if op.form == nil || op.formSaving {
		return m, nil
	}
	// The same call from a form left mid-save is still in flight.
	key := opCallKey(op)
	if _, busy := m.opCalls[key]; busy {
		op.status = "the same " + opCallLabel(op) + " is still saving; its result will show on the footer"
		return m, nil
	}
	label := opCallLabel(op)
	// A mark-free opened from the sandbox list reloads the list when it lands,
	// even if the dialog was left meanwhile.
	reloadList := s == surfaceDetail && m.markFreeFromList
	next, cmd := m.sendOrchOpForm(s)
	if cmd == nil {
		return next, nil
	}
	nm := next.(Model)
	op = nm.opState(s)
	tok := formCallSeq.Add(1)
	op.formSaving, op.formToken, op.status = true, tok, savingText
	if nm.opCalls == nil {
		nm.opCalls = map[string]uint64{}
	}
	nm.opCalls[key] = tok
	call := formCall{token: tok, key: key, label: label, reloadList: reloadList}
	return nm, func() tea.Msg {
		switch msg := cmd().(type) {
		case orchDoneMsg:
			msg.formCall = call
			return msg
		case orchFormErrMsg:
			msg.formCall = call
			return msg
		default:
			return msg
		}
	}
}

// formCall identifies the op-form call a reply answers.
type formCall struct {
	token uint64 // 0: not a form's call
	key   string // its opCalls entry
	label string // names it in a refusal that lands after its form was left
	// reloadList: a mark-free opened from the sandbox list, which reloads the
	// list on success whether or not its dialog is still open.
	reloadList bool
}

// opCallKey names the call the open form would send, so the same call is not
// sent again while it is in flight. A mark-free is keyed by its worker and run (a
// run is closed once, whatever the outcome or note); any other form by its kind,
// orchestrator and the values it sends (trimmed as sendOrchOpForm trims them).
func opCallKey(op *orchOpState) string {
	t := op.formTarget
	if v, ok := op.formVals.(*markFreeVals); ok {
		return markFreeCallKey(v)
	}
	return fmt.Sprintf("%s|%s|%s|%v", op.formKind, t.host, t.id, sentOpVals(op.formVals))
}

// markFreeMismatch reports a mark-free that closed another run (or none) than
// runID, the run its dialog was opened for.
func markFreeMismatch(runID string, closed *pb.Run) string {
	if closed.GetId() == "" {
		return "error: " + runID + " was no longer awaiting review; the worker was marked free instead"
	}
	return fmt.Sprintf("error: mark free closed %s as %s, not %s (%s was no longer awaiting review) — check %s's outcome", closed.GetId(), runState(closed), runID, runID, closed.GetId())
}

// markFreeCallKey names a mark-free of one run on one worker.
func markFreeCallKey(v *markFreeVals) string {
	return "mark free|" + v.host + "|" + v.orchID + "|" + v.sandboxID + "|" + v.runID
}

// sentOpVals is a form's values as its call sends them: surrounding whitespace
// dropped, and a call form's empty arguments left out. The dirty guard keeps
// comparing the raw values (snapshotOpVals).
func sentOpVals(vals any) any {
	switch v := vals.(type) {
	case *actionFormVals:
		c := *v
		c.name, c.description, c.workerTag, c.args = strings.TrimSpace(c.name), strings.TrimSpace(c.description), strings.TrimSpace(c.workerTag), trimLines(c.args)
		return c
	case *eventFormVals:
		c := *v
		c.name, c.intervalMinutes, c.args = strings.TrimSpace(c.name), strings.TrimSpace(c.intervalMinutes), trimLines(c.args)
		c.activeFrom, c.activeUntil, c.timezone = strings.TrimSpace(c.activeFrom), strings.TrimSpace(c.activeUntil), strings.TrimSpace(c.timezone)
		c.days = snapshotOpVals(v).(eventFormVals).days // a set: copied and sorted
		return c
	case *callFormVals:
		out := map[string]string{}
		for k, p := range v.values {
			if val := strings.TrimSpace(*p); val != "" {
				out[k] = val
			}
		}
		return out
	}
	return snapshotOpVals(vals)
}

// trimLines trims each line of s and drops blank ones.
func trimLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// opCallLabel names an op form's call ("call review-pr on alpha-bot", "save
// action triage on alpha-bot", "save event poll (tev-1) on alpha-bot"). A save
// names what it saves, so a notice settled by label never takes another save's
// outcome; an edited event adds its id, since event names need not be unique.
func opCallLabel(op *orchOpState) string {
	verb := op.formKind
	if verb == "action" || verb == "event" {
		verb = "save " + verb
		if name := savedName(op.formVals); name != "" {
			verb += " " + name
		}
	}
	return verb + " on " + op.formTarget.name
}

// savedName is the name an action or event form saves under (trimmed, as sent).
func savedName(vals any) string {
	switch v := vals.(type) {
	case *actionFormVals:
		return strings.TrimSpace(v.name)
	case *eventFormVals:
		name := strings.TrimSpace(v.name)
		if v.id != "" {
			name += " (" + v.id + ")"
		}
		return name
	}
	return ""
}

// releaseOpCall drops c's in-flight record, if it is still c's.
func (m Model) releaseOpCall(c formCall) {
	if c.token != 0 && m.opCalls[c.key] == c.token {
		delete(m.opCalls, c.key)
	}
}

// ownsFormReply reports whether a reply with token answers the call the open
// form on s is waiting for.
func (m Model) ownsFormReply(s orchSurface, token uint64) bool {
	op := m.opState(s)
	return token != 0 && op.formSaving && op.formToken == token
}

func (m Model) sendOrchOpForm(s orchSurface) (tea.Model, tea.Cmd) {
	op := m.opState(s)
	if op.form == nil {
		return m, nil
	}
	t := op.formTarget
	// Re-checked on submit: the daemon can drop while the form is open, and the
	// form stays open with the refusal rather than going elsewhere (FR-030).
	od, err := m.connectedDaemon(t.host)
	if err != nil {
		op.status = err.Error()
		return m, nil
	}
	id := t.id
	switch v := op.formVals.(type) {
	case *actionFormVals:
		args, err := parseArgLines(v.args)
		if err != nil {
			op.status = err.Error()
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
				return orchFormErrMsg{err: err, sink: s}
			}
			return orchDoneMsg{status: "saved action " + a.GetName(), reload: true, closeForm: true, sink: s, target: t}
		}
	case *callFormVals:
		args := map[string]string{}
		for _, arg := range v.action.GetArguments() {
			val := strings.TrimSpace(*v.values[arg.GetName()])
			if arg.GetRequired() && val == "" {
				op.status = "argument " + arg.GetName() + " is required"
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
				return orchFormErrMsg{err: err, sink: s}
			}
			return orchDoneMsg{status: fmt.Sprintf("queued %s at position %d", run.GetId(), run.GetPosition()), reload: true, closeForm: true, sink: s, target: t}
		}
	case *markFreeVals:
		md, err := m.connectedDaemon(v.host)
		if err != nil {
			op.status = err.Error()
			return m, nil
		}
		failed := v.outcome == "failed"
		note, sbID, orchID, runID := strings.TrimSpace(v.note), v.sandboxID, v.orchID, v.runID
		return m, func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			run, err := md.MarkWorkerFree(ctx, orchID, sbID, failed, note)
			if err != nil {
				return orchFormErrMsg{err: err, sink: s}
			}
			if runID != "" && run.GetId() != runID {
				// The daemon closes whatever is awaiting review on the worker (its
				// request names no run): the dialog's run was closed meanwhile, and
				// this call went to another run, or to none (review run-491).
				return orchDoneMsg{status: markFreeMismatch(runID, run), reload: true, closeForm: true, sink: s, target: t}
			}
			if run.GetId() == "" {
				// Nothing was awaiting review after all; the daemon freed the
				// parked worker instead.
				return orchDoneMsg{status: "marked the worker free", reload: true, closeForm: true, sink: s, target: t}
			}
			return orchDoneMsg{status: fmt.Sprintf("closed %s as %s", run.GetId(), runState(run)), reload: true, closeForm: true, sink: s, target: t}
		}
	case *eventFormVals:
		minutes, err := strconv.Atoi(strings.TrimSpace(v.intervalMinutes))
		if err != nil || minutes < 1 {
			op.status = "the interval must be a whole number of minutes, at least 1"
			return m, nil
		}
		args, err := parseKV(v.args)
		if err != nil {
			op.status = err.Error()
			return m, nil
		}
		win, err := windowFromForm(v)
		if err != nil {
			op.status = err.Error()
			return m, nil
		}
		ev := &pb.TimedEvent{Id: v.id, Name: strings.TrimSpace(v.name), Action: v.action, IntervalSeconds: uint32(minutes * 60), Arguments: args, Enabled: v.enabled, DedupeDisabled: !v.dedupe, Window: win}
		return m, func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			if _, err := od.PutTimedEvent(ctx, id, ev); err != nil {
				return orchFormErrMsg{err: err, sink: s}
			}
			return orchDoneMsg{status: "saved event " + ev.GetName(), reload: true, closeForm: true, sink: s, target: t}
		}
	}
	m.closeOpForm(s)
	return m, nil
}

// orchFormErrMsg is a refusal of an open form: shown verbatim, form kept.
type orchFormErrMsg struct {
	err  error
	sink orchSurface
	// formCall is the op-form call refused (see orchOpState.formToken).
	formCall formCall
}

// --- renderers ---

// actionLine renders one action: name (type) description #tag {args}.
func actionLine(a *pb.Action) string {
	kind := "prompt"
	if a.GetType() == pb.ActionType_ACTION_TYPE_REASON {
		kind = "reason"
	}
	line := selectedStyle.Render(sanitizeLine(a.GetName(), 0)) + dimStyle.Render(" ("+kind+")")
	if a.GetDescription() != "" {
		line += "  " + sanitizeLine(a.GetDescription(), 60)
	}
	if a.GetWorkerTag() != "" {
		line += "  " + tagBadgeStyle.Render("#"+sanitizeLine(a.GetWorkerTag(), 0))
	}
	var args []string
	for _, arg := range a.GetArguments() {
		n := sanitizeLine(arg.GetName(), 0)
		if arg.GetRequired() {
			n += "*"
		}
		args = append(args, n)
	}
	if len(args) > 0 {
		line += dimStyle.Render("  {" + strings.Join(args, ", ") + "}")
	}
	return line
}

// eventLine renders one timed event: [on|off] name → action · any disabled
// reason · every · any active window · next · waiting/skipped counts, with the
// dedupe badge. waiting is how many of its runs are queued.
func eventLine(ev *pb.TimedEvent, waiting int) string {
	onOff := statusOKStyle.Render("[on]")
	if !ev.GetEnabled() {
		onOff = dimStyle.Render("[off]")
	}
	line := fmt.Sprintf("%s  %s  %s", onOff, selectedStyle.Render(sanitizeLine(ev.GetName(), 24)), dimStyle.Render("→ "+sanitizeLine(ev.GetAction(), 24)))
	if ev.GetDisabledReason() != "" {
		// Before the counters, so a row cut to the width still says why.
		line += "  " + statusErrStyle.Render("disabled: "+sanitizeLine(ev.GetDisabledReason(), 60))
	}
	every := fmt.Sprintf("every %dm", ev.GetIntervalSeconds()/60)
	if w := windowLabel(ev.GetWindow()); w != "" {
		every += " · " + sanitizeLine(w, 48)
	}
	line += fmt.Sprintf("  %s  %s  %s", dimStyle.Render(every), dimStyle.Render("next "+nextFire(ev)),
		dimStyle.Render(fmt.Sprintf("waiting %d · skipped %d", waiting, ev.GetSkippedCount())))
	if !ev.GetDedupeDisabled() {
		line += "  " + tagBadgeStyle.Render("dedupe")
	}
	return line
}

// runOrigin describes what caused a run: "user", "timed <event>" or
// "triggered by <worker>".
func runOrigin(r *pb.Run) string {
	switch r.GetEventKind() {
	case pb.EventKind_EVENT_KIND_TIMED:
		return "timed " + sanitizeLine(r.GetOriginLabel(), 0)
	case pb.EventKind_EVENT_KIND_TRIGGERED:
		return "triggered by " + sanitizeLine(r.GetOriginLabel(), 0)
	}
	return "user"
}

// runResultLine is a finished run's reported result on one line: the summary (or
// the developer's note) and the reason it ended. Open results shows it in full.
func runResultLine(r *pb.Run) string {
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
	return detail
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
	case d >= 24*time.Hour:
		// A windowed event can sleep for days (a weekend).
		d = d.Round(time.Hour)
		return fmt.Sprintf("in %dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
	return "in " + d.Round(time.Minute).String()
}
