package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/libs/switchboard-proto/mcprules"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// saveTimeout bounds a save's forward steps (one `mcp rm` + N stores + one `mcp
// add`); cleanup calls run on their own contexts (research R2 Contexts).
const saveTimeout = 10 * time.Minute

// Persist retry timing (research R2 Failed persist); variables so tests shrink them.
var (
	persistAttempts   = 3
	persistRetryDelay = time.Second
	persistBackoff    = 30 * time.Second
)

// Test hooks (nil in production): testHookPersist runs first in the persist
// mutate func, before anything takes stateMu; testHookStep runs between the
// named forward steps.
var (
	testHookPersist func()
	testHookStep    func(step string)
)

// Errors specific to the save.
var (
	// ErrConfirmRequired: a replace needs a confirmation the request lacks
	// (FR-402 dropped settings, FR-404 discarded authorization) → FAILED_PRECONDITION.
	ErrConfirmRequired = errors.New("confirmation required")
	// ErrAlreadyRegistered: an add of a name the host already lists → ALREADY_EXISTS.
	ErrAlreadyRegistered = errors.New("already registered on this host")
	// ErrSecretsUnavailable: the request carries secret values but the host's
	// secret store cannot be used → FAILED_PRECONDITION.
	ErrSecretsUnavailable = errors.New("secret storage is not available")
)

// saveJob is one save's state across its steps.
type saveJob struct {
	m    *Manager
	l    *nameLock
	name string
	req  *pb.SaveMcpServerRequest
	def  *pb.McpServerDefinition

	settings *pb.McpGatewaySettings
	rec      *pb.McpServerDefinition
	pending  *pb.McpPendingReplace
	view     *pb.McpDefinitionView
	prev     *pb.McpServerDefinition // live (replace), the pending's previous (Retry), nil (new)

	retry, isNew, regChange, replace bool
	clientChanged, storesClient      bool
	snapshot                         map[string]bool
	order                            []string // secret names to store, in order

	out        *pb.McpSaveOutcome
	storedAny  bool
	removedAny bool
}

// Save runs one MCP form save (SWI-21 FR-404, research R2) on a context
// detached from the caller and bounded by saveTimeout, so it finishes and
// persists its outcome even when the caller goes away. A live caller gets the
// outcome (or the refusal); a caller that left gets ctx.Err().
func (m *Manager) Save(ctx context.Context, req *pb.SaveMcpServerRequest) (*pb.McpSaveOutcome, error) {
	name := strings.TrimSpace(req.GetName())
	def, err := checkSaveRequest(name, req)
	if err != nil {
		return nil, err
	}
	type result struct {
		out *pb.McpSaveOutcome
		err error
	}
	done := make(chan result, 1)
	replied := false
	reply := func(out *pb.McpSaveOutcome, err error) {
		if !replied {
			replied = true
			done <- result{out, err}
		}
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	go func() {
		defer cancel()
		l, release, err := m.lockName(sctx, name, lockOpts{authSensitive: req.GetSecretValues()[mcprules.ClientSecretName(name)] != ""})
		if err != nil {
			reply(nil, err)
			return
		}
		defer release()
		j := &saveJob{m: m, l: l, name: name, req: req, def: def}
		j.run(sctx, reply)
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// checkSaveRequest applies every FR-420 rule that needs no host read and
// returns the cleaned definition.
func checkSaveRequest(name string, req *pb.SaveMcpServerRequest) (*pb.McpServerDefinition, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	if req.GetDefinition() == nil {
		return nil, fmt.Errorf("%w: a definition is required", ErrInvalidArgs)
	}
	if !req.GetIsNew() && req.GetRetryOfRevision() == 0 && req.GetFingerprint() == "" {
		return nil, fmt.Errorf("%w: an edit needs the fingerprint of the definition it was opened from", ErrInvalidArgs)
	}
	def := proto.Clone(req.GetDefinition()).(*pb.McpServerDefinition)
	def.RebindPending, def.LiteralOnHost = false, nil
	switch def.Kind {
	case pb.McpServerKind_MCP_SERVER_KIND_REMOTE:
		def.Url = strings.TrimSpace(def.Url)
		if def.Url == "" {
			return nil, fmt.Errorf("%w: a remote server needs a URL", ErrInvalidArgs)
		}
		if def.Command != nil {
			return nil, fmt.Errorf("%w: a remote server has no command", ErrInvalidArgs)
		}
		headers, err := mcprules.NormalizeHeaders(def.Headers)
		if err != nil {
			return nil, ruleErr(err)
		}
		def.Headers = headers
		if err := mcprules.CheckPlaceholders(headers); err != nil {
			return nil, ruleErr(err)
		}
		def.ClientId, def.OauthAuthorizationServer = strings.TrimSpace(def.ClientId), strings.TrimSpace(def.OauthAuthorizationServer)
		if err := mcprules.ValidClientID(def.ClientId); err != nil {
			return nil, ruleErr(err)
		}
		if err := mcprules.CheckAuthServer(def.OauthAuthorizationServer); err != nil {
			return nil, ruleErr(err)
		}
		if err := mcprules.ValidScopes(def.Scopes); err != nil {
			return nil, ruleErr(err)
		}
	case pb.McpServerKind_MCP_SERVER_KIND_LOCAL:
		if len(def.Headers) > 0 || len(def.Scopes) > 0 || def.ClientId != "" || def.OauthAuthorizationServer != "" || def.Url != "" {
			return nil, fmt.Errorf("%w: headers, scopes and OAuth client settings apply to remote endpoints only", ErrInvalidArgs)
		}
		if def.Command == nil {
			return nil, fmt.Errorf("%w: a host-run server needs a command", ErrInvalidArgs)
		}
		if err := checkCommand(def.Command); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: the definition needs a kind (remote or host command)", ErrInvalidArgs)
	}
	// Secret values (FR-410, research R2 Secret values): only the server's own
	// names, never empty, single-line, never the redaction marker. Messages name
	// the secret, never the value.
	allowed := map[string]bool{}
	for _, p := range mcprules.Placeholders(def.Headers) {
		allowed[mcprules.SecretName(name, p)] = true
	}
	if def.ClientId != "" {
		allowed[mcprules.ClientSecretName(name)] = true
	}
	for n, v := range req.GetSecretValues() {
		switch {
		case !allowed[n]:
			return nil, fmt.Errorf("%w: secret %s is not a placeholder of this definition (or the client secret of its client ID)", ErrInvalidArgs, n)
		case v == "" || strings.TrimSpace(v) == "":
			return nil, fmt.Errorf("%w: secret %s is empty; leave it out to keep the stored value", ErrInvalidArgs, n)
		case strings.ContainsAny(v, "\r\n"):
			return nil, fmt.Errorf("%w: secret %s must be a single line", ErrInvalidArgs, n)
		case strings.Contains(v, mcprules.RedactMarker):
			return nil, fmt.Errorf("%w: secret %s holds the redaction marker %s; re-enter the credential", ErrInvalidArgs, n, mcprules.RedactMarker)
		}
	}
	if req.GetIsNew() && req.GetRetryOfRevision() == 0 {
		if err := mcprules.CheckCredentialHeaders(def.Headers, nil); err != nil {
			return nil, ruleErr(err)
		}
	}
	return def, nil
}

// run executes the steps under the lock. reply delivers the caller's result
// once; after an UNAVAILABLE reply the job keeps retrying its persist.
func (j *saveJob) run(ctx context.Context, reply func(*pb.McpSaveOutcome, error)) {
	if err := j.check(ctx); err != nil {
		reply(nil, err)
		return
	}
	if err := j.retryPreStep(ctx); err != nil {
		reply(nil, err)
		return
	}
	if err := j.snapshotStep(ctx); err != nil {
		reply(nil, err)
		return
	}
	if err := j.classify(); err != nil {
		reply(nil, err)
		return
	}
	if err := j.markInProgress(); err != nil {
		reply(nil, err)
		return
	}
	j.hook("in-progress")
	j.forward(ctx)
	j.persist(reply)
}

func (j *saveJob) hook(step string) {
	if testHookStep != nil {
		testHookStep(step)
	}
}

// check is steps 1 and the redacted-header rule: the record re-read under the
// lock, the fingerprint against fresh host reads (research R5), and the view
// the form was built from.
func (j *saveJob) check(ctx context.Context) error {
	m := j.m
	settings, err := m.reg.GetMcpSettings()
	if err != nil {
		return err
	}
	j.settings = settings
	if settings.GetSavesInProgress()[j.name] != nil {
		return ErrRecovering
	}
	j.rec = settings.GetDefinitions()[j.name]
	j.pending = settings.GetPendingReplaces()[j.name]
	j.retry = j.req.GetRetryOfRevision() != 0
	j.isNew = j.req.GetIsNew() && !j.retry
	if j.retry && (j.pending == nil || j.pending.GetRevision() != j.req.GetRetryOfRevision()) {
		return refuse(pb.McpSaveRefusal_PENDING_ENDED, "the pending replace of %s has ended or changed", j.name)
	}
	if j.isNew && j.pending != nil {
		return refuse(pb.McpSaveRefusal_PENDING_EXISTS, "%s has a pending replace; retry or cancel it", j.name)
	}
	hr := m.readHost(ctx, j.name)
	switch {
	case j.isNew:
		if hr.lsErr != nil {
			return refuse(pb.McpSaveRefusal_UNVERIFIED, "couldn't read the server from the host; try again")
		}
		if hr.row != nil {
			return fmt.Errorf("%w: %s", ErrAlreadyRegistered, j.name)
		}
		j.view = &pb.McpDefinitionView{Name: j.name}
		return nil
	case hr.lsErr == nil && hr.row == nil && !j.retry:
		return ErrGone
	case j.retry && hr.row != nil:
		return refuse(pb.McpSaveRefusal_PENDING_ENDED, "%s was registered by another path; the pending replace has ended", j.name)
	}
	if fp := m.fingerprint(j.rec, hr, j.pending.GetRevision()); fp != j.req.GetFingerprint() && !j.retry {
		switch {
		case !strings.HasPrefix(j.req.GetFingerprint(), m.fpKeyID):
			return refuse(pb.McpSaveRefusal_STALE_RESTARTED, "the daemon restarted since the form opened; it was reloaded")
		case hr.failed():
			return refuse(pb.McpSaveRefusal_UNVERIFIED, "couldn't read the server from the host; try again")
		default:
			return refuse(pb.McpSaveRefusal_STALE_CHANGED, "the server changed since the form opened; it was reloaded")
		}
	} else if j.retry && hr.failed() {
		return refuse(pb.McpSaveRefusal_UNVERIFIED, "couldn't read the server from the host; try again")
	}
	j.view = m.buildView(j.name, settings, hr)
	// A LITERAL_ON_HOST header that still holds the view's redacted value is
	// unchanged and exempt from the credential rule (research R2).
	viewVals := map[string]string{}
	for _, h := range j.view.GetDefinition().GetHeaders() {
		n, v := mcprules.SplitHeader(h)
		viewVals[strings.ToLower(n)] = v
	}
	exempt := map[string]bool{}
	for _, n := range j.view.GetDefinition().GetLiteralOnHost() {
		key := strings.ToLower(n)
		for _, h := range j.def.Headers {
			hn, hv := mcprules.SplitHeader(h)
			if strings.ToLower(hn) == key && hv == viewVals[key] {
				exempt[key] = true
				j.def.LiteralOnHost = append(j.def.LiteralOnHost, hn)
			}
		}
	}
	if err := mcprules.CheckCredentialHeaders(j.def.Headers, exempt); err != nil {
		return ruleErr(err)
	}
	return nil
}

// retryPreStep (R8): a Retry first removes each carried removal-failed name it
// leaves blank; one still listed (or unreadable) refuses with no change.
func (j *saveJob) retryPreStep(ctx context.Context) error {
	if !j.retry {
		return nil
	}
	for _, n := range j.pending.GetRemovalFailedSecrets() {
		if _, entered := j.req.GetSecretValues()[n]; entered {
			continue
		}
		if j.m.secrets == nil {
			return refuse(pb.McpSaveRefusal_PENDING_REMOVAL_FAILED, "secret %s could not be removed: secret storage is not available", n)
		}
		if !j.m.removeChecked(n) {
			return refuse(pb.McpSaveRefusal_PENDING_REMOVAL_FAILED, "secret %s is still stored on the host and could not be removed; re-enter it or try again", n)
		}
		j.removedAny = true
	}
	return nil
}

// removeChecked removes one secret on its own context; a failure is re-checked
// against a fresh global list. It reports whether the secret is confirmed gone.
func (m *Manager) removeChecked(name string) bool {
	ok, _ := m.removeCheckedOut(name)
	return ok
}

// removeCheckedOut is removeChecked plus the removal's error text (empty when
// the secret is confirmed gone).
func (m *Manager) removeCheckedOut(name string) (bool, string) {
	rctx, cancel := context.WithTimeout(context.Background(), mutateTimeout)
	_, err := m.secrets.RemoveQuiet(rctx, name)
	cancel()
	if err == nil {
		return true, ""
	}
	lctx, cancel := context.WithTimeout(context.Background(), time.Duration(listTimeout))
	defer cancel()
	global, lerr := m.secrets.ListGlobal(lctx)
	if lerr != nil {
		return false, err.Error() + "; " + lerr.Error()
	}
	if !global[name] {
		return true, ""
	}
	return false, err.Error()
}

// snapshotStep reads the global secret list once, under the lock (FR-410).
func (j *saveJob) snapshotStep(ctx context.Context) error {
	values := j.req.GetSecretValues()
	if j.m.secrets == nil {
		if len(values) > 0 {
			return fmt.Errorf("%w on this daemon", ErrSecretsUnavailable)
		}
		j.snapshot = map[string]bool{}
		return nil
	}
	global, err := j.m.secrets.ListGlobal(ctx)
	if err != nil {
		if len(values) > 0 {
			return fmt.Errorf("%w: couldn't read the host's secrets: %v", ErrSecretsUnavailable, err)
		}
		global = map[string]bool{}
	}
	j.snapshot = global
	for _, p := range mcprules.Placeholders(j.def.Headers) {
		if n := mcprules.SecretName(j.name, p); values[n] != "" {
			j.order = append(j.order, n)
		}
	}
	if n := mcprules.ClientSecretName(j.name); values[n] != "" {
		j.order = append(j.order, n)
		j.storesClient = true
	}
	return nil
}

// classify decides switchboard-only vs registration change (research R6) and
// re-checks the authorization exclusion now that it is known (research R3).
func (j *saveJob) classify() error {
	switch {
	case j.isNew:
		j.regChange = true
	case j.retry:
		j.regChange = true
		j.prev = j.pending.GetPrevious()
	default:
		j.prev = j.view.GetDefinition()
		j.regChange = mcprules.RegistrationChanged(j.prev, j.def)
		j.replace = j.regChange
	}
	j.clientChanged = mcprules.ClientChanged(j.prev, j.def)
	if j.regChange && len(j.def.LiteralOnHost) > 0 {
		return fmt.Errorf("%w: header %q holds a credential that is only on the host; convert it to a ${placeholder} and re-enter the secret before re-registering", ErrInvalidArgs, j.def.LiteralOnHost[0])
	}
	if j.regChange && j.def.Kind == pb.McpServerKind_MCP_SERVER_KIND_LOCAL && !j.req.GetAcknowledgeLocalExecution() {
		return ErrLocalNotAcknowledged
	}
	if j.replace {
		if st := j.view.GetAuthState(); st != pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED && st != pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE && !j.req.GetConfirmDiscardAuthorization() {
			return fmt.Errorf("%w: replacing the registration discards its authorization; confirm to continue", ErrConfirmRequired)
		}
		if len(j.view.GetUnsupportedSettings()) > 0 && !j.req.GetConfirmDropUnsupported() {
			return fmt.Errorf("%w: replacing the registration drops settings switchboard cannot carry (%s); confirm to continue", ErrConfirmRequired, strings.Join(j.view.GetUnsupportedSettings(), ", "))
		}
	}
	if j.replace || j.storesClient {
		if err := j.m.recheckInflight(j.name); err != nil {
			return err
		}
	}
	if j.regChange {
		j.m.setReplacing(j.name, j.l)
	}
	return nil
}

// markInProgress writes the crash-safety record before any host mutation of a
// registration change, then marks the name saving (research R2 step 4a). A
// failed write stops the save with no host change.
func (j *saveJob) markInProgress() error {
	if !j.regChange {
		return nil
	}
	rec := &pb.McpSaveInProgress{
		Name: j.name, AttachByDefault: j.req.GetAttachByDefault(), Definition: proto.Clone(j.def).(*pb.McpServerDefinition),
		Retry: j.retry,
	}
	if j.prev != nil {
		rec.Previous = proto.Clone(j.prev).(*pb.McpServerDefinition)
	}
	for _, n := range j.order {
		if j.snapshot[n] {
			rec.PlannedOverwrittenSecrets = append(rec.PlannedOverwrittenSecrets, n)
		} else {
			rec.PlannedCreatedSecrets = append(rec.PlannedCreatedSecrets, n)
		}
	}
	if _, err := j.m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		if s.SavesInProgress == nil {
			s.SavesInProgress = map[string]*pb.McpSaveInProgress{}
		}
		s.SavesInProgress[j.name] = rec
		return nil
	}); err != nil {
		return err
	}
	j.m.markRefusing(j.name, j.l, true)
	return nil
}

// forward runs remove → stores → add → identity (research R2 steps 5–8) and
// fills the outcome; failures roll back (step 9).
func (j *saveJob) forward(ctx context.Context) {
	j.out = &pb.McpSaveOutcome{Name: j.name, Identity: pb.McpSaveOutcome_NOT_TOUCHED, State: pb.McpSaveOutcome_UNCHANGED_REGISTRATION}
	if j.replace && !j.removeStep(ctx) {
		return
	}
	j.hook("removed")
	if !j.storeStep(ctx) {
		j.failed()
		return
	}
	if j.regChange {
		if !j.addStep(ctx) {
			j.failed()
			return
		}
		j.out.State, j.out.Replaced = pb.McpSaveOutcome_REGISTERED, j.replace
	}
	j.identityStep()
	j.unusedSecrets()
	if j.replace {
		j.out.AttachedSandboxes = j.m.attachedSandboxes(j.name)
	}
}

// removeStep runs `mcp rm --force` (reaped before any re-check, R7); on failure
// the name is re-listed: absent ⇒ continue, present ⇒ STILL_REGISTERED.
func (j *saveJob) removeStep(ctx context.Context) bool {
	rctx, cancel := context.WithTimeout(ctx, time.Duration(mutateTimeout))
	_, err := j.m.cli.runBounded(rctx, argvRemove(j.name)...)
	cancel()
	if err == nil {
		return true
	}
	lctx, cancel := context.WithTimeout(context.Background(), time.Duration(listTimeout))
	defer cancel()
	rows, lerr := j.m.listRows(lctx)
	if lerr == nil {
		listed := false
		for _, r := range rows {
			listed = listed || r.name == j.name
		}
		if !listed {
			return true
		}
	}
	j.out.State = pb.McpSaveOutcome_STILL_REGISTERED
	j.out.HostErrors = append(j.out.HostErrors, err.Error())
	if lerr != nil {
		j.out.HostErrors = append(j.out.HostErrors, lerr.Error())
	}
	return false
}

// storeStep stores the entered secrets in placeholder order, then the client
// secret, each classified created or overwritten from the snapshot (FR-412).
func (j *saveJob) storeStep(ctx context.Context) bool {
	for _, n := range j.order {
		if err := j.m.secrets.SetQuiet(ctx, n, j.req.GetSecretValues()[n]); err != nil {
			j.out.HostErrors = append(j.out.HostErrors, err.Error())
			return false
		}
		j.storedAny = true
		if j.snapshot[n] {
			j.out.Overwritten = append(j.out.Overwritten, n)
		} else {
			j.out.Created = append(j.out.Created, n)
		}
		j.hook("stored")
	}
	return true
}

// addStep registers the new definition; a failure or timeout is never
// re-checked (the add may still complete on the host, R7).
func (j *saveJob) addStep(ctx context.Context) bool {
	actx, cancel := context.WithTimeout(ctx, time.Duration(mutateTimeout))
	defer cancel()
	j.out.AddStarted = true
	if _, err := j.m.cli.runBounded(actx, addArgv(j.name, j.def)...); err != nil {
		j.out.HostErrors = append(j.out.HostErrors, err.Error())
		return false
	}
	j.out.AddStarted = false
	return true
}

// addArgv is the add argv for a definition.
func addArgv(name string, def *pb.McpServerDefinition) []string {
	if def.Kind == pb.McpServerKind_MCP_SERVER_KIND_LOCAL {
		c := def.GetCommand()
		return argvAddLocal(name, c.GetCommand(), c.GetArgs(), c.GetDir())
	}
	return argvAddRemote(name, def.Url, def.Headers, def.ClientId, def.OauthAuthorizationServer, def.Scopes)
}

// failed rolls back after a failed store or add (FR-406, FR-412): created
// secrets are removed, and overwritten ones too for a registration change; a
// switchboard-only save names its overwritten secrets as changed.
func (j *saveJob) failed() {
	remove := append([]string(nil), j.out.Created...)
	if j.regChange {
		remove = append(remove, j.out.Overwritten...)
	}
	for _, n := range remove {
		j.removedAny = true
		if ok, _ := j.m.removeCheckedOut(n); ok {
			j.out.RolledBack = append(j.out.RolledBack, n)
		} else {
			j.out.RollbackFailed = append(j.out.RollbackFailed, n)
		}
	}
	if j.regChange {
		j.out.State = pb.McpSaveOutcome_NOT_REGISTERED
	}
}

// identityStep clears the client's identity binding or marks the server
// *client re-bind pending* (FR-415, research R9).
func (j *saveJob) identityStep() {
	j.out.RebindPending = j.rec.GetRebindPending()
	clear := j.storesClient && (j.clientChanged || j.rec.GetRebindPending() || j.rec == nil)
	switch {
	case clear:
		id := mcprules.IdentityName(j.name)
		j.removedAny = true
		rctx, cancel := context.WithTimeout(context.Background(), mutateTimeout)
		_, err := j.m.secrets.RemoveQuiet(rctx, id)
		cancel()
		if err == nil {
			j.out.Identity, j.out.RebindPending = pb.McpSaveOutcome_CLEARED, false
			return
		}
		lctx, cancel := context.WithTimeout(context.Background(), time.Duration(listTimeout))
		global, lerr := j.m.secrets.ListGlobal(lctx)
		cancel()
		switch {
		case lerr != nil:
			j.out.Identity, j.out.RebindPending = pb.McpSaveOutcome_UNCONFIRMED, true
			j.out.HostErrors = append(j.out.HostErrors, err.Error(), lerr.Error())
		case global[id]:
			j.out.Identity, j.out.RebindPending = pb.McpSaveOutcome_NOT_CLEARED, true
			j.out.HostErrors = append(j.out.HostErrors, err.Error())
		default:
			// Confirmed absent: a first add has no :identity record yet, so the
			// expected failure is left out of host_errors.
			j.out.Identity, j.out.RebindPending = pb.McpSaveOutcome_CLEARED, false
		}
	case j.clientChanged:
		j.out.Identity, j.out.RebindPending = pb.McpSaveOutcome_LEFT_IN_PLACE, true
	}
}

// unusedSecrets lists FR-413 / R9 removal candidates: stored secrets of
// placeholders the edit removed, and the client secret plus its binding record
// for a public client (LEFT_IN_PLACE) or a cleared client ID.
func (j *saveJob) unusedSecrets() {
	used := map[string]bool{}
	for _, p := range mcprules.Placeholders(j.def.Headers) {
		used[mcprules.SecretName(j.name, p)] = true
	}
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			j.out.UnusedSecrets = append(j.out.UnusedSecrets, n)
		}
	}
	for _, p := range mcprules.Placeholders(j.prev.GetHeaders()) {
		if n := mcprules.SecretName(j.name, p); !used[n] && j.snapshot[n] {
			add(n)
		}
	}
	cs := mcprules.ClientSecretName(j.name)
	clientCleared := j.prev.GetClientId() != "" && j.def.ClientId == ""
	if (j.out.Identity == pb.McpSaveOutcome_LEFT_IN_PLACE || clientCleared) && (j.snapshot[cs] || j.storesClient) {
		add(cs)
		add(mcprules.IdentityName(j.name))
	}
}

// attachedSandboxes is FR-419's list: this daemon's sandboxes that record the
// server, plus FR-425 skipped ones.
func (m *Manager) attachedSandboxes(name string) []*pb.McpAttachedSandbox {
	if m.sandboxes == nil {
		return nil
	}
	all, err := m.sandboxes.List()
	if err != nil {
		log.Printf("mcp: %s: list sandboxes for the save outcome: %v", name, err)
		return nil
	}
	var out []*pb.McpAttachedSandbox
	for _, sb := range all {
		attached := false
		for _, n := range sb.GetMcpServers() {
			attached = attached || n == name
		}
		skipped := false
		for _, e := range sb.GetMcpSkipped() {
			skipped = skipped || e.GetName() == name
		}
		if attached || skipped {
			out = append(out, &pb.McpAttachedSandbox{
				SandboxId: sb.GetId(), DisplayName: sb.GetDisplayName(), SkippedAtLaunch: skipped && !attached,
				Exclusive: sb.GetMcpAttachMode() == pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE,
			})
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].GetDisplayName() < out[k].GetDisplayName() })
	return out
}

// mutateFor is the save's per-name persist write (research R2 Failed persist):
// built once from the outcome, touching only this name's definition, mark,
// pending replace and in-progress record, re-applied to each attempt's record.
func (j *saveJob) mutateFor() (func(*pb.McpGatewaySettings) error, bool) {
	name, out := j.name, j.out
	now := timestamppb.Now()
	var pending *pb.McpPendingReplace
	if out.State == pb.McpSaveOutcome_NOT_REGISTERED && (j.replace || j.retry) {
		pending = &pb.McpPendingReplace{
			Name: name, AttachByDefault: j.req.GetAttachByDefault(), Definition: proto.Clone(j.def).(*pb.McpServerDefinition),
			RemovedSecrets: out.RolledBack, RemovalFailedSecrets: out.RollbackFailed, AddStarted: out.AddStarted,
			Error: strings.Join(out.HostErrors, "; "), FailedAt: now, Revision: j.pending.GetRevision() + 1,
		}
		if j.prev != nil {
			pending.Previous = proto.Clone(j.prev).(*pb.McpServerDefinition)
		}
		out.PendingReplace = pending
	}
	def := proto.Clone(j.def).(*pb.McpServerDefinition)
	def.RebindPending = out.RebindPending
	return func(s *pb.McpGatewaySettings) error {
		if testHookPersist != nil {
			testHookPersist()
		}
		delete(s.SavesInProgress, name)
		if s.Definitions == nil {
			s.Definitions = map[string]*pb.McpServerDefinition{}
		}
		if s.PendingReplaces == nil {
			s.PendingReplaces = map[string]*pb.McpPendingReplace{}
		}
		switch {
		case pending != nil:
			s.PendingReplaces[name] = proto.Clone(pending).(*pb.McpPendingReplace)
			delete(s.Marks, name)
			if j.prev != nil && !j.retry {
				s.Definitions[name] = proto.Clone(j.prev).(*pb.McpServerDefinition)
			}
		case out.State == pb.McpSaveOutcome_REGISTERED:
			s.Definitions[name] = proto.Clone(def).(*pb.McpServerDefinition)
			setMarkIn(s, name, j.req.GetAttachByDefault())
			delete(s.PendingReplaces, name)
		case out.State == pb.McpSaveOutcome_UNCHANGED_REGISTRATION:
			setMarkIn(s, name, j.req.GetAttachByDefault())
			if cur := s.Definitions[name]; cur != nil {
				cur.RebindPending = out.RebindPending
				cur.LiteralOnHost = def.LiteralOnHost
				cur.Headers = def.Headers
			}
		}
		return nil
	}, pending != nil
}

// persist writes the outcome (step 10) with up to persistAttempts tries; if all
// fail it replies UNAVAILABLE, keeps the lock and retries the same write every
// persistBackoff until it lands or the daemon stops. The post-persist tail runs
// only after the write lands.
func (j *saveJob) persist(reply func(*pb.McpSaveOutcome, error)) {
	if j.out == nil { // refused before forward ran
		return
	}
	mutate, pendingNow := j.mutateFor()
	write := func() (*pb.McpGatewaySettings, error) { return j.m.reg.UpdateMcpSettings(mutate) }
	settings, err := write()
	for i := 1; err != nil && i < persistAttempts; i++ {
		time.Sleep(persistRetryDelay)
		settings, err = write()
	}
	if err != nil {
		log.Printf("mcp: %s: persist the save outcome: %v (retrying)", j.name, err)
		var msg string
		if j.regChange {
			msg = "the host was changed but the result could not be recorded yet; retrying"
		} else {
			j.m.markRefusing(j.name, j.l, false)
			msg = "the save ran but its result (mark, re-bind state) could not be recorded yet; retrying"
			if stored := append(append([]string(nil), j.out.Created...), j.out.Overwritten...); len(stored) > 0 {
				msg += "; stored secrets: " + strings.Join(stored, ", ")
			}
		}
		if j.storedAny || j.removedAny {
			j.m.secrets.EmitChanged()
		}
		j.m.emitOutcome(nil, j.settings)
		reply(nil, fmt.Errorf("%w: %s", ErrUnavailable, msg))
		for err != nil {
			select {
			case <-j.m.daemonCtx.Done():
				return // the record (if any) is a start-up leftover now
			case <-time.After(persistBackoff):
			}
			settings, err = write()
		}
		j.tail(settings, pendingNow, nil)
		return
	}
	j.tail(settings, pendingNow, reply)
}

// tail is everything after a landed persist: the state map (with the refuse
// channel swap), the replace hook, the events, then the reply. Unlock and the
// generation bump follow in the deferred release.
func (j *saveJob) tail(settings *pb.McpGatewaySettings, pendingNow bool, reply func(*pb.McpSaveOutcome, error)) {
	m := j.m
	m.clearRefusing(j.name, j.l, &pendingNow)
	if (j.replace || j.retry) && (j.out.State == pb.McpSaveOutcome_REGISTERED || j.out.State == pb.McpSaveOutcome_STILL_REGISTERED) {
		m.replaceEnded(j.name, true)
	}
	if j.out.State == pb.McpSaveOutcome_REGISTERED {
		j.out.Server = m.serverRow(j.name, settings)
	}
	if j.storedAny || j.removedAny {
		m.secrets.EmitChanged()
	}
	m.emitOutcome(j.out, settings)
	if reply != nil {
		reply(j.out, nil)
	}
}

// serverRow decorates the saved server's row for the outcome (best effort).
func (m *Manager) serverRow(name string, settings *pb.McpGatewaySettings) *pb.McpServer {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(listTimeout))
	defer cancel()
	rows, err := m.listRows(ctx)
	if err != nil {
		return nil
	}
	for _, r := range rows {
		if r.name == name {
			return m.decorate(ctx, []listRow{r}, normalize(settings))[0]
		}
	}
	return nil
}

// --- FR-413 removal (research R13) ---

// RemoveSecrets removes secrets a save stopped using, under the per-name lock.
// Only the server's own placeholder, client-secret and identity names are
// allowed. Failed removals are re-checked; rebind_pending clears when the
// client secret and its identity record are both confirmed gone (R9).
func (m *Manager) RemoveSecrets(ctx context.Context, name string, names []string) (*pb.RemoveMcpServerSecretsResponse, error) {
	name = strings.TrimSpace(name)
	if !SafeArg(name) {
		return nil, fmt.Errorf("%w: server name %q", ErrInvalidArgs, name)
	}
	if m.secrets == nil {
		return nil, fmt.Errorf("%w on this daemon", ErrSecretsUnavailable)
	}
	cs, id := mcprules.ClientSecretName(name), mcprules.IdentityName(name)
	sensitive := false
	for _, n := range names {
		p, ok := strings.CutPrefix(n, mcprules.SecretPrefix(name))
		switch {
		case n == cs || n == id:
			sensitive = true
		case !ok || mcprules.ValidPlaceholder(p) != nil:
			return nil, fmt.Errorf("%w: %s is not a secret of server %s", ErrInvalidArgs, n, name)
		}
	}
	_, release, err := m.lockName(ctx, name, lockOpts{authSensitive: sensitive})
	if err != nil {
		return nil, err
	}
	defer release()
	if err := m.refuseRecord(name, false); err != nil {
		return nil, err
	}
	resp := &pb.RemoveMcpServerSecretsResponse{}
	gone := map[string]bool{}
	for _, n := range names {
		ok, msg := m.removeCheckedOut(n)
		if ok {
			gone[n] = true
			resp.Removed = append(resp.Removed, n)
		} else {
			resp.Failed = append(resp.Failed, n)
			resp.HostErrors = append(resp.HostErrors, msg)
		}
	}
	settings, err := m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		if d := s.GetDefinitions()[name]; d != nil && gone[cs] && gone[id] {
			d.RebindPending = false
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	resp.RebindPending = settings.GetDefinitions()[name].GetRebindPending()
	m.secrets.EmitChanged()
	m.emitChanged()
	return resp, nil
}
