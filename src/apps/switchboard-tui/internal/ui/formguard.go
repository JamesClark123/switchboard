package ui

import (
	"maps"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	"gopkg.in/yaml.v3"
)

// formKeyMap is the one keymap every huh form in the app uses (SWI-9 R1).
//
// Enter inserts a newline in multi-line Text fields (subcommands, workspaces,
// allowed/denied domains, env vars, … — one list entry per line) instead of
// advancing to the next field. huh's default binds Enter to "next" and puts
// newline on alt+enter/ctrl+j, which is unintuitive for these list fields. Tab
// still moves between fields and ctrl+s applies the form, so Enter is freed up
// for newlines. Enter is removed from Text.Next/Submit because huh dispatches
// those before the textarea's newline, and the textarea's newline key is driven
// from Text.NewLine. Single-line Input/Confirm fields keep huh's default (Enter
// advances).
//
// Text.Editor is keyless: huh's Text.Update matches keymap.Editor whatever
// ExternalEditor is set to (ExternalEditor(false) only hides the binding when
// help renders), so any key left on it would still launch $EDITOR on a form that
// hides help. ctrl+e opens the in-app enlarged view instead.
func formKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Text.NewLine = key.NewBinding(key.WithKeys("enter", "alt+enter", "ctrl+j"), key.WithHelp("enter", "new line"))
	km.Text.Next = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next"))
	km.Text.Prev = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back"))
	km.Text.Submit = key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "apply"))
	km.Text.Editor = key.NewBinding()
	return km
}

// textEntry is what a formSpec remembers about one multi-line Text field: the
// pointer it is bound to, its title (for the enlarged view's border), and the
// value it was built with (for the normalisation notice).
type textEntry struct {
	ptr      *string
	title    string
	original string
}

// formSpec is the registry of a form's multi-line Text fields. Every Text in the
// app is built through spec.text so the enlarged view can find the focused
// field's pointer, and no Text can bring the external editor back.
type formSpec struct {
	texts map[*huh.Text]textEntry
}

func newFormSpec() *formSpec {
	return &formSpec{texts: map[*huh.Text]textEntry{}}
}

// text builds a multi-line field bound to ptr and records it. CRLF line endings
// are folded to LF first: the textarea maps each \r and \n to a newline, so CRLF
// content would otherwise come back with every line doubled.
func (s *formSpec) text(ptr *string, title string) *huh.Text {
	original := *ptr
	*ptr = strings.ReplaceAll(*ptr, "\r\n", "\n")
	t := huh.NewText().ExternalEditor(false).Title(title).Value(ptr)
	s.texts[t] = textEntry{ptr: ptr, title: title, original: original}
	return t
}

// lookup reports whether field is one of this spec's Text fields.
func (s *formSpec) lookup(field huh.Field) (*huh.Text, textEntry, bool) {
	if s == nil {
		return nil, textEntry{}, false
	}
	t, ok := field.(*huh.Text)
	if !ok {
		return nil, textEntry{}, false
	}
	e, ok := s.texts[t]
	return t, e, ok
}

// normalize pushes each Text's cleaned textarea value back into its pointer
// (huh only writes the pointer on Update), so a baseline taken afterwards
// matches what a later tab-past-without-editing writes. It returns the titles
// of fields whose value differs from what they were built with, so the surface
// can tell the user the text will be rewritten when saved.
func (s *formSpec) normalize() []string {
	var changed []string
	for t, e := range s.texts {
		t.Update(nil)
		if *e.ptr != e.original {
			changed = append(changed, e.title)
		}
	}
	return changed
}

// normalizeNotice is the status shown when normalize reports rewritten fields.
func normalizeNotice(titles []string) string {
	if len(titles) == 0 {
		return ""
	}
	return strings.Join(titles, ", ") + ": line endings/tabs/control characters will be rewritten when saved"
}

// snapshotOpVals copies an op form's bound values so later edits can be compared
// against it. Values are copied, never aliased: the vals are live pointers huh
// writes to.
func snapshotOpVals(vals any) any {
	switch v := vals.(type) {
	case *actionFormVals:
		return *v
	case *eventFormVals:
		return *v
	case *markFreeVals:
		return *v
	case *callFormVals:
		return flattenCallVals(v)
	}
	return nil
}

// flattenCallVals turns a call form's per-argument pointers into plain values.
func flattenCallVals(v *callFormVals) map[string]string {
	out := make(map[string]string, len(v.values))
	for k, p := range v.values {
		out[k] = *p
	}
	return out
}

// opValsEqual compares two snapshotOpVals results: the call form's flattened map
// with maps.Equal, every other (scalar-only) vals struct with ==.
func opValsEqual(a, b any) bool {
	am, aok := a.(map[string]string)
	bm, bok := b.(map[string]string)
	if aok || bok {
		return aok && bok && maps.Equal(am, bm)
	}
	return a == b
}

// launchSnapshot is what a launch or reasoning-setup rebuild would send (SWI-9
// R3). Sources and kits keep selection order: a different order is a different
// launch.
type launchSnapshot struct {
	sources   []string
	cloneMode bool
	name      string
	kits      []string
	host      string
	orch      string
}

// snapshotLaunch copies the launch state's choices. The slices are cloned:
// toggle removes entries in place, which would rewrite an aliased baseline.
func snapshotLaunch(l launchState) launchSnapshot {
	s := launchSnapshot{
		sources:   slices.Clone(l.order),
		cloneMode: l.cloneMode,
		name:      strings.TrimSpace(l.name.Value()),
		kits:      slices.Clone(l.kitOrder),
		host:      l.targetHost,
	}
	if l.orchestrator != nil {
		s.orch = l.orchestrator.summary.GetOrchestrator().GetId()
	}
	return s
}

func (s launchSnapshot) equal(o launchSnapshot) bool {
	return slices.Equal(s.sources, o.sources) && s.cloneMode == o.cloneMode && s.name == o.name &&
		slices.Equal(s.kits, o.kits) && s.host == o.host && s.orch == o.orch
}

// kitFingerprint is the kit as it would be saved: spec.yaml of a normalised
// deep clone plus the two sidecar sections. It deliberately avoids SpecYAML
// (which refuses an unnamed kit) and never normalises the live kit (Normalize
// writes through the shared Permissions pointer). Marshal errors cannot happen
// for these plain structs; one would only make the fingerprint coarser.
func kitFingerprint(kit store.Kit) string {
	b, _ := yaml.Marshal(&kit)
	var clone store.Kit
	_ = yaml.Unmarshal(b, &clone)
	clone.Normalize()
	spec, _ := yaml.Marshal(&clone)
	out := string(spec)
	if len(kit.EscapeHatch) > 0 {
		eh, _ := yaml.Marshal(kit.EscapeHatch)
		out += "\n--- escape hatch\n" + string(eh)
	}
	if len(kit.Services) > 0 {
		svc, _ := yaml.Marshal(kit.Services)
		out += "\n--- services\n" + string(svc)
	}
	return out
}

// guardSurface names one editing surface the unsaved-changes guard protects
// (SWI-9 data-model.md). At most one is active at a time; activeGuard derives it
// from the screen and that screen's sub-state. The op forms get one value per
// orchSurface so a dialog on the Jobs form never outlives it onto the detail.
type guardSurface int

const (
	gNone guardSurface = iota
	gConfig
	gMcp
	gSecrets
	gOrchAdmin
	gOpJobs
	gOpDetail
	gKitItem
	gKitOverview
	gLaunch
	gReasoning
	gSources
	gSourcesBrowse
	gRename
	gTag
	gHostAdd
	gGroupAdd
)

// activeGuard is the surface open now. Every case checks m.screen first, so a
// form left behind on a screen the user has moved off is never active, and a
// nested form (gKitItem) wins over its parent (gKitOverview). It is O(1): it
// runs after every message.
func (m Model) activeGuard() (guardSurface, bool) {
	switch m.screen {
	case screenConfigEditor:
		if m.editor.form != nil {
			return gConfig, true
		}
	case screenMcp:
		if m.mcpView.form != nil {
			return gMcp, true
		}
	case screenSecrets:
		if m.secretsView.form != nil {
			return gSecrets, true
		}
	case screenDaemonOrchs:
		if m.orch.form != nil {
			return gOrchAdmin, true
		}
	case screenJobs:
		if m.jobs.form != nil {
			return gOpJobs, true
		}
	case screenOrchDetail:
		if m.orchDetail.form != nil {
			return gOpDetail, true
		}
	case screenKitEditor:
		if m.kitEditor.form != nil {
			return gKitItem, true
		}
		return gKitOverview, true
	case screenLaunch:
		if m.launch.reasoning != nil {
			return gReasoning, true
		}
		return gLaunch, true
	case screenSources:
		if m.sourcesView.browsing {
			return gSourcesBrowse, true
		}
		return gSources, true
	case screenRename:
		return gRename, true
	case screenTag:
		return gTag, true
	case screenHosts:
		if m.hosts.adding {
			return gHostAdd, true
		}
	case screenGroups:
		if m.groups.adding {
			return gGroupAdd, true
		}
	}
	return gNone, false
}

// opSurfaceOf maps an op-form guard to the orchSurface hosting it.
func opSurfaceOf(g guardSurface) orchSurface {
	if g == gOpJobs {
		return surfaceJobs
	}
	return surfaceDetail
}

// opGuard is the inverse of opSurfaceOf.
func opGuard(s orchSurface) guardSurface {
	if s == surfaceJobs {
		return gOpJobs
	}
	return gOpDetail
}

// guardForm is g's open huh form, or nil (inline and browser surfaces have none).
func (m Model) guardForm(g guardSurface) *huh.Form {
	switch g {
	case gConfig:
		return m.editor.form
	case gMcp:
		return m.mcpView.form
	case gSecrets:
		return m.secretsView.form
	case gOrchAdmin:
		return m.orch.form
	case gOpJobs, gOpDetail:
		return m.opState(opSurfaceOf(g)).form
	case gKitItem:
		return m.kitEditor.form
	}
	return nil
}

// reviveCompleted wraps a save run after huh completed g's form (enter on the
// last field): huh then ignores input and renders nothing, so if the save left
// the form open — a validation error — it is rebuilt from the same vals with
// the error still showing (research R4). Use as reviveCompleted(g)(m.save()).
func reviveCompleted(g guardSurface) func(tea.Model, tea.Cmd) (tea.Model, tea.Cmd) {
	return func(tm tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
		m := tm.(Model)
		if f := m.guardForm(g); f == nil || f.State != huh.StateCompleted {
			return m, cmd
		}
		reopen := m.reopenForm(g)
		return m, tea.Batch(cmd, reopen)
	}
}

// guardDirty reports whether g's current values differ from its baseline. It is
// a pure comparison: an edit undone back to the original value is clean.
// Surfaces without a baseline yet are never dirty.
func (m Model) guardDirty(g guardSurface) bool {
	switch g {
	case gConfig:
		return m.editor.vals != nil && !m.editor.vals.snapshot().equal(m.editor.baseline)
	case gMcp:
		return m.mcpView.vals != nil && *m.mcpView.vals != m.mcpView.baseline
	case gSecrets:
		return m.secretsView.vals != nil && *m.secretsView.vals != m.secretsView.baseline
	case gOrchAdmin:
		return m.orch.vals != nil && *m.orch.vals != m.orch.baseline
	case gOpJobs, gOpDetail:
		op := m.opState(opSurfaceOf(g))
		return op.form != nil && !opValsEqual(snapshotOpVals(op.formVals), op.formBaseline)
	case gKitItem:
		return m.kitEditor.vals != nil && *m.kitEditor.vals != m.kitEditor.formBaseline
	case gLaunch, gReasoning:
		return !snapshotLaunch(m.launch).equal(m.launch.baseline)
	}
	return false
}

// guardSave is the dialog's Save: the surface's own save or confirm, unchanged.
// It re-checks dirty first: a background update can leave the surface clean
// while the dialog is open, and then Save just leaves (research R4). The outcome
// (closed, open with an error, waiting on a confirm) is read from the state.
func (m Model) guardSave(g guardSurface) (Model, tea.Cmd) {
	if !m.guardDirty(g) {
		return m.guardLeave(g)
	}
	switch g {
	case gConfig:
		return guardResult(m.saveConfig())
	case gMcp:
		return guardResult(m.applyMcpForm())
	case gSecrets:
		return guardResult(m.applySecretsForm())
	case gOrchAdmin:
		return guardResult(m.applyOrchForm())
	case gOpJobs:
		return guardResult(m.submitOrchOpForm(surfaceJobs))
	case gOpDetail:
		return guardResult(m.submitOrchDetailForm())
	case gKitItem:
		return guardResult(m.applyKitForm())
	case gLaunch:
		return guardResult(m.startLaunch())
	case gReasoning:
		return m.confirmRebuildFromSetup()
	}
	return m, nil
}

// guardResult narrows a surface handler's result for the guard's switches, which
// keep working on the Model.
func guardResult(tm tea.Model, cmd tea.Cmd) (Model, tea.Cmd) {
	return tm.(Model), cmd
}

// guardLeave is the surface's esc path: close without applying.
func (m Model) guardLeave(g guardSurface) (Model, tea.Cmd) {
	switch g {
	case gConfig:
		m.screen = screenList
	case gMcp:
		m.mcpView.form, m.mcpView.vals, m.mcpView.spec = nil, nil, nil
	case gSecrets:
		m.secretsView.form, m.secretsView.vals = nil, nil
	case gOrchAdmin:
		m.closeOrchForm()
	case gOpJobs:
		m.closeOpForm(surfaceJobs)
		m.jobs.status = ""
	case gOpDetail:
		m.closeOpForm(surfaceDetail)
		m.orchDetail.status = ""
		if m.markFreeFromList {
			m.markFreeFromList = false
			m.screen = screenList
		}
	case gKitItem:
		m.closeKitForm()
	case gLaunch:
		m.screen = screenList
	case gReasoning:
		m.screen = screenOrchDetail
	}
	return m, nil
}

// requestExit is every guarded exit key: a clean surface leaves at once, a
// changed one opens the unsaved-changes dialog.
func (m Model) requestExit(g guardSurface) (tea.Model, tea.Cmd) {
	if !m.guardDirty(g) {
		return m.guardLeave(g)
	}
	m.openUnsaved(g, false)
	return m, nil
}

// reopenForm rebuilds a form huh has completed (enter on its last field) from
// the same vals, so a failed save never leaves a blank, dead form. The spec is
// fresh (the rebuilt form has new *huh.Text pointers), normalize is skipped (the
// vals are already normalised) and the surface baseline is kept (research R4).
func (m *Model) reopenForm(g guardSurface) tea.Cmd {
	switch g {
	case gOpJobs, gOpDetail:
		op := m.opState(opSurfaceOf(g))
		if op.formBuild == nil {
			return nil
		}
		op.formSpec = newFormSpec()
		op.form = op.formBuild(*m, op.formSpec)
		return op.form.Init()
	case gKitItem:
		ke := &m.kitEditor
		if ke.vals == nil {
			return nil
		}
		ke.spec = newFormSpec()
		ke.form = m.buildKitForm(ke.formKind, ke.spec, ke.vals)
		return ke.form.Init()
	case gMcp:
		if m.mcpView.vals == nil {
			return nil
		}
		return m.buildMcpForm()
	case gConfig:
		if m.editor.vals == nil {
			return nil
		}
		m.editor.form = m.buildEditorForm(m.editor.vals)
		return m.editor.form.Init()
	case gSecrets:
		if m.secretsView.vals == nil {
			return nil
		}
		m.secretsView.form = m.secretsForm(m.secretsView.vals)
		return m.secretsView.form.Init()
	case gOrchAdmin:
		if m.orch.formBuild == nil {
			return nil
		}
		m.orch.form = m.orch.formBuild(*m)
		return m.orch.form.Init()
	}
	return nil
}

// reconcileGuardLayers keeps the guard's layers bound to a live surface: the
// dialog closes when its surface is no longer the active one (an in-flight save
// closed it, a screen change, a forced close). It runs after every message and
// is O(1) — no sandbox-list walks.
func (m *Model) reconcileGuardLayers() {
	if !m.unsaved.open {
		return
	}
	if g, ok := m.activeGuard(); !ok || g != m.unsaved.surface {
		m.unsaved = unsavedState{}
	}
}
