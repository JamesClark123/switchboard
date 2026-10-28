package worker

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"google.golang.org/protobuf/proto"
)

var hexToken = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestAttachRecordsControllerTokenRegistrationAndBriefing(t *testing.T) {
	e := newEnv(t, `
  "mcp add "*) echo "Warning: registered a private/loopback URL"; echo "MCP server registered" ;;
  "mcp load "*) echo "loaded (live)" ;;`)
	sb := e.sandbox(t, "11111111-aaaa", "worker-one", pb.SandboxState_SANDBOX_STATE_RUNNING)
	// Pre-existing user content around the block survives.
	claude := sb.GetWorkspacePath() + "/CLAUDE.md"
	if err := writeFile(claude, "# my notes\n"); err != nil {
		t.Fatal(err)
	}

	out, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "review-bot"), Briefing: "## Orchestrator: review-bot"})
	if err != nil {
		t.Fatal(err)
	}
	c := out.GetController()
	if c.GetOrchestratorId() != "orch-1" || c.GetName() != "review-bot" || c.GetHostId() != "host-m" || c.GetAttachedAt() == nil || c.GetRun() != nil {
		t.Errorf("controller = %+v", c)
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Error("attach must not change the state (I-C2)")
	}
	// Token: 64 hex, in the worker_tokens bucket only, never in the Sandbox bytes.
	tok, err := e.reg.GetWorkerToken(sb.GetId())
	if err != nil || !hexToken.MatchString(tok) {
		t.Fatalf("token = %q / %v", tok, err)
	}
	stored, _ := e.reg.Get(sb.GetId())
	if blob, _ := proto.Marshal(stored); strings.Contains(string(blob), tok) {
		t.Error("the token must never serialize into the Sandbox record (I-C4)")
	}
	if e.svc.sandboxForToken(tok) != sb.GetId() {
		t.Error("the in-memory index should resolve the token")
	}
	// Exactly the two documented argv rows, in order.
	log := readLog(t, e.log)
	want := []string{
		"mcp add sxb-orch-11111111 --url http://127.0.0.1:8765/orchestrator/w/" + tok + "/mcp --skip-auth",
		"mcp load sxb-orch-11111111 --sandbox worker-one",
	}
	if strings.Join(log, "\n") != strings.Join(want, "\n") {
		t.Errorf("argv = %q, want %q", log, want)
	}
	// The briefing block sits beside the user's content.
	got := readFile(t, claude)
	if !strings.HasPrefix(got, "# my notes\n") || !strings.Contains(got, blockBegin+"\n## Orchestrator: review-bot\n"+blockEnd) {
		t.Errorf("CLAUDE.md = %q", got)
	}
	if e.mgr.emitted() != 1 {
		t.Errorf("attach should emit sandbox_changed once, got %d", e.mgr.emitted())
	}

	// A second orchestrator is refused naming the first, with no sbx call.
	_, err = e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-2", "other")})
	var ac *AlreadyControlledError
	if !errors.As(err, &ac) || ac.Name != "review-bot" || !errors.Is(err, ErrAlreadyControlled) {
		t.Fatalf("second controller = %v", err)
	}
	if len(readLog(t, e.log)) != 2 {
		t.Error("a refused attach must not call sbx")
	}
	// The same orchestrator re-attaches idempotently: re-minted token, re-registered.
	again, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "review-bot-2"), Briefing: "## Orchestrator: review-bot-2"})
	if err != nil || again.GetController().GetName() != "review-bot-2" || !proto.Equal(again.GetController().GetAttachedAt(), c.GetAttachedAt()) {
		t.Errorf("re-attach = %+v / %v", again.GetController(), err)
	}
	tok2, _ := e.reg.GetWorkerToken(sb.GetId())
	if tok2 == tok || e.svc.sandboxForToken(tok) != "" || e.svc.sandboxForToken(tok2) != sb.GetId() {
		t.Error("re-attach should re-mint the token and drop the old one from the index")
	}
	if n := len(readLog(t, e.log)); n != 4 {
		t.Errorf("re-attach should re-register (4 rows), got %d", n)
	}
	if !strings.Contains(readFile(t, claude), "review-bot-2") || strings.Count(readFile(t, claude), blockBegin) != 1 {
		t.Error("re-attach rewrites the single block")
	}
	// Unknown sandbox / missing fields.
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: "nope", Controller: controller("o", "n")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown sandbox = %v", err)
	}
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId()}); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing controller = %v", err)
	}
}

func TestAttachRaceSettlesOneController(t *testing.T) {
	e := newEnv(t, "")
	sb := e.sandbox(t, "22222222-bbbb", "racer", pb.SandboxState_SANDBOX_STATE_RUNNING)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := map[string]int{}
	for i := 0; i < 50; i++ {
		id := "orch-A"
		if i%2 == 1 {
			id = "orch-B"
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller(id, id)}); err == nil {
				mu.Lock()
				wins[id]++
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	if len(wins) != 1 {
		t.Fatalf("exactly one orchestrator may win the CAS, got %v (SC-007)", wins)
	}
	stored, _ := e.reg.Get(sb.GetId())
	for id := range wins {
		if stored.GetController().GetOrchestratorId() != id {
			t.Errorf("record controller = %s, winner %s", stored.GetController().GetOrchestratorId(), id)
		}
	}
}

func TestDetachOrderAndGuards(t *testing.T) {
	e := newEnv(t, `
  "mcp rm "*) echo "boom: rm failed" >&2; exit 1 ;;`)
	sb := e.sandbox(t, "33333333-cccc", "det", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "review-bot"), Briefing: "b"}); err != nil {
		t.Fatal(err)
	}
	tok, _ := e.reg.GetWorkerToken(sb.GetId())
	// Wrong orchestrator ⇒ forbidden, nothing changes.
	if _, err := e.svc.Detach(bg, sb.GetId(), "orch-2", false); !errors.Is(err, ErrForbidden) {
		t.Errorf("wrong id = %v", err)
	}
	if _, err := e.reg.GetWorkerToken(sb.GetId()); err != nil {
		t.Error("a refused detach must not revoke the token")
	}
	// Detach: the token is revoked BEFORE rm runs, so a failing rm still detaches.
	out, err := e.svc.Detach(bg, sb.GetId(), "orch-1", false)
	if err != nil || out.GetController() != nil {
		t.Fatalf("detach = %+v / %v", out.GetController(), err)
	}
	if _, err := e.reg.GetWorkerToken(sb.GetId()); !errors.Is(err, registry.ErrTokenNotFound) {
		t.Error("token should be revoked")
	}
	if e.svc.sandboxForToken(tok) != "" {
		t.Error("token should leave the index")
	}
	log := readLog(t, e.log)
	if log[len(log)-1] != "mcp rm sxb-orch-33333333 --force" {
		t.Errorf("last argv = %q, want mcp rm … --force", log[len(log)-1])
	}
	if strings.Contains(readFile(t, sb.GetWorkspacePath()+"/CLAUDE.md"), blockBegin) {
		t.Error("the briefing block should be stripped")
	}
	for _, l := range log {
		if strings.HasPrefix(l, "stop ") || strings.HasPrefix(l, "create ") || l == "rm det --force" || strings.HasPrefix(l, "rm det") {
			t.Errorf("attach/detach must never stop, create or rm the sandbox (I-C2): %q", l)
		}
	}
	// Not a worker any more.
	if _, err := e.svc.Detach(bg, sb.GetId(), "orch-1", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("detach twice = %v", err)
	}
	if _, err := e.svc.Detach(bg, "nope", "orch-1", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("detach unknown = %v", err)
	}
	// The reasoning sandbox refuses without force.
	r := e.sandbox(t, "44444444-dddd", "reasoning-x", pb.SandboxState_SANDBOX_STATE_RUNNING)
	ctl := controller("orch-1", "review-bot")
	ctl.Reasoning = true
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: r.GetId(), Controller: ctl}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Detach(bg, r.GetId(), "orch-1", false); !errors.Is(err, ErrPrecondition) {
		t.Errorf("reasoning without force = %v", err)
	}
	if out, err := e.svc.Detach(bg, r.GetId(), "orch-1", true); err != nil || out.GetController() != nil {
		t.Errorf("reasoning with force = %v / %v", out, err)
	}
}

func TestUpdateWorker(t *testing.T) {
	e := newEnv(t, "")
	sb := e.sandbox(t, "55555555-eeee", "upd", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if _, err := e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-1"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("update of a non-worker = %v", err)
	}
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "a")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-2"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("update by a foreign orchestrator = %v", err)
	}
	name, briefing := "renamed", "## new briefing"
	out, err := e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-1", Name: &name, Briefing: &briefing,
		SetRun: true, Run: &pb.WorkerRun{Id: "run-1", Action: "review-pr", NeedsReview: true}})
	if err != nil || out.GetController().GetName() != "renamed" || out.GetController().GetRun().GetId() != "run-1" || !out.GetController().GetRun().GetNeedsReview() {
		t.Fatalf("update = %+v / %v", out.GetController(), err)
	}
	if !strings.Contains(readFile(t, sb.GetWorkspacePath()+"/CLAUDE.md"), "## new briefing") {
		t.Error("briefing should be rewritten")
	}
	// set_run with no run clears it; a request without set_run leaves it.
	out, _ = e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-1"})
	if out.GetController().GetRun() == nil {
		t.Error("run should stay without set_run")
	}
	out, _ = e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-1", SetRun: true})
	if out.GetController().GetRun() != nil {
		t.Error("set_run without run should clear it")
	}
	// clear_needs_input (the developer's mark free on a parked worker) sets a
	// NEEDS_INPUT agent back to IDLE, leaves any other status alone, and is
	// refused while a run is on the sandbox.
	if _, err := e.reg.Update(sb.GetId(), func(x *pb.Sandbox) error {
		if x.Agent == nil {
			x.Agent = &pb.AgentSession{}
		}
		x.Agent.Status = pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err = e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-1", ClearNeedsInput: true})
	if err != nil || out.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Errorf("clear_needs_input = %v / %v", out.GetAgent().GetStatus(), err)
	}
	if _, err := e.reg.Update(sb.GetId(), func(x *pb.Sandbox) error { x.Agent.Status = pb.AgentStatus_AGENT_STATUS_WORKING; return nil }); err != nil {
		t.Fatal(err)
	}
	out, _ = e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-1", ClearNeedsInput: true})
	if out.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_WORKING {
		t.Error("clear_needs_input should leave a working agent alone")
	}
	if _, err := e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-1", SetRun: true, Run: &pb.WorkerRun{Id: "run-2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-1", ClearNeedsInput: true}); !errors.Is(err, ErrPrecondition) {
		t.Errorf("clear_needs_input with a run on the sandbox = %v", err)
	}
	if _, err := e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: "nope", OrchestratorId: "orch-1"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update unknown = %v", err)
	}
}

func TestNotRunningDefersLoadUntilBringUpAndDestroyRevokes(t *testing.T) {
	e := newEnv(t, "")
	sb := e.sandbox(t, "66666666-ffff", "stopped-one", pb.SandboxState_SANDBOX_STATE_STOPPED)
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "a")}); err != nil {
		t.Fatal(err)
	}
	log := readLog(t, e.log)
	if len(log) != 1 || !strings.HasPrefix(log[0], "mcp add sxb-orch-66666666 ") {
		t.Fatalf("a stopped sandbox is registered but not loaded: %q", log)
	}
	stored, _ := e.reg.Get(sb.GetId())
	e.svc.OnBringUp(stored)
	log = readLog(t, e.log)
	if len(log) != 2 || log[1] != "mcp load sxb-orch-66666666 --sandbox stopped-one" {
		t.Errorf("bring-up should load the registration: %q", log)
	}
	// Bring-up of an uncontrolled sandbox or one without a token does nothing.
	e.svc.OnBringUp(&pb.Sandbox{Id: "plain"})
	e.svc.OnBringUp(&pb.Sandbox{Id: "no-token", Controller: controller("o", "n")})
	if len(readLog(t, e.log)) != 2 {
		t.Error("nothing should run for uncontrolled sandboxes")
	}
	// Destroy revokes the token and removes the registration.
	e.svc.OnDestroy(stored)
	if _, err := e.reg.GetWorkerToken(sb.GetId()); err == nil {
		t.Error("destroy should revoke the token")
	}
	log = readLog(t, e.log)
	if log[len(log)-1] != "mcp rm sxb-orch-66666666 --force" {
		t.Errorf("destroy should unregister: %q", log)
	}
	e.svc.OnDestroy(&pb.Sandbox{Id: "plain"})
	if len(readLog(t, e.log)) != 3 {
		t.Error("destroying an uncontrolled sandbox runs nothing")
	}
	// A failing `mcp add` still records the controller (retried at bring-up).
	e2 := newEnv(t, `
  "mcp add "*) echo "gateway unavailable" >&2; exit 1 ;;`)
	sb2 := e2.sandbox(t, "77777777-0000", "flaky", pb.SandboxState_SANDBOX_STATE_RUNNING)
	out, err := e2.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb2.GetId(), Controller: controller("orch-1", "a")})
	if err != nil || out.GetController() == nil {
		t.Errorf("attach with a failing gateway = %v / %v", out, err)
	}
}

func TestTokenIndexLoadedAtStart(t *testing.T) {
	e := newEnv(t, "")
	if err := e.reg.PutWorkerToken("sb-x", "deadbeef"); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Deps{Manager: e.mgr, Registry: e.reg, HostID: "h"})
	if svc.sandboxForToken("deadbeef") != "sb-x" || svc.sandboxForToken("nope") != "" {
		t.Error("the token index should be loaded from the bucket at start")
	}
	if svc.HostID() != "h" {
		t.Error("HostID")
	}
	// Apply is Attach with the launching record.
	sb := e.sandbox(t, "88888888-1111", "launched", pb.SandboxState_SANDBOX_STATE_CREATING)
	sb.ContainerRef = "launched"
	sb.State = pb.SandboxState_SANDBOX_STATE_RUNNING
	if err := e.svc.Apply(bg, sb, controller("orch-1", "rb"), "## b"); err != nil {
		t.Fatal(err)
	}
	log := readLog(t, e.log)
	if len(log) != 2 || log[1] != "mcp load sxb-orch-88888888 --sandbox launched" {
		t.Errorf("launch-attach argv = %q", log)
	}
	if ServerName("short") != "sxb-orch-short" || endpointURL("1", "t") != "http://127.0.0.1:1/orchestrator/w/t/mcp" {
		t.Error("naming helpers")
	}
	if sandboxRef(&pb.Sandbox{Id: "id"}) != "id" || sandboxRef(&pb.Sandbox{Id: "id", DisplayName: "n"}) != "n" {
		t.Error("sandboxRef fallbacks")
	}
	// A nil gateway (no CLI) is a no-op everywhere.
	var g *gateway
	if err := g.register(bg, "a", "b", true, "t"); err != nil || g.load(bg, "a", "b") != nil {
		t.Error("nil gateway should be a no-op")
	}
	g.unregister(bg, "a")
}

// PR #17 review run-484: a bring-up reloads the gateway server before it starts
// the agent, and a warm start that lands during the load leaves the start to it.
func TestBringUpStartsTheAgentAfterTheGatewayLoad(t *testing.T) {
	e := newEnv(t, "")
	sb := e.sandbox(t, "99999999-aaaa", "readopted", pb.SandboxState_SANDBOX_STATE_STOPPED)
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "a")}); err != nil {
		t.Fatal(err)
	}
	stored, err := e.reg.Update(sb.GetId(), func(x *pb.Sandbox) error {
		x.State = pb.SandboxState_SANDBOX_STATE_RUNNING // found running after a restart
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loadedFirst, starts := false, 0
	e.svc.agents = agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) {
		starts++
		for _, l := range readLog(t, e.log) {
			if strings.HasPrefix(l, "mcp load sxb-orch-99999999 ") {
				loadedFirst = true
			}
		}
		return newRecSession(), nil
	})
	// While a bring-up is loading the gateway, a warm start does nothing.
	e.svc.markBringUp(stored.GetId(), 1)
	e.svc.WarmStart(stored)
	if starts != 0 {
		t.Fatal("a warm start during the gateway load started the agent")
	}
	e.svc.markBringUp(stored.GetId(), -1)
	if e.svc.bringingUp(stored.GetId()) || len(e.svc.bringing) != 0 {
		t.Errorf("the bring-up mark should be released: %v", e.svc.bringing)
	}
	e.svc.OnBringUp(stored)
	if starts != 1 || !loadedFirst {
		t.Errorf("bring-up: %d starts, gateway loaded first %v", starts, loadedFirst)
	}
	if e.svc.bringingUp(stored.GetId()) {
		t.Error("the bring-up mark outlived the bring-up")
	}
}
