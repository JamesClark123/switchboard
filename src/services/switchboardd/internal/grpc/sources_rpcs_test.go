package grpc_test

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/resources"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// sourcesServer starts a daemon over the in-process socket with an event hub (so
// Subscribe works) and the given runner (so a test can hold a clone mid-flight).
func sourcesServer(t *testing.T, runner *testRunner) (pb.SwitchboardClient, string) {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	ws := filepath.Join(dir, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := sandbox.NewManager(reg, runner, ws, "host-1")
	srv := sbgrpc.NewServer(sbgrpc.Config{
		Manager: mgr, HostID: "host-1", DaemonVersion: "test", WorkspaceRoot: ws,
		KitRoot: filepath.Join(dir, "kits"), Hub: agent.NewHub("host-1"),
	})
	sock := filepath.Join(dir, "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("unix:"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewSwitchboardClient(conn), dir
}

// mkSource creates a seedable directory (optionally a git repo) under dir.
func mkSource(t *testing.T, dir, name string, repo bool) *pb.SourceRef {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if repo {
		if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &pb.SourceRef{Path: p, IsRepo: repo}
}

// launchMode launches a sandbox seeded from srcs in the given mode.
func launchMode(t *testing.T, client pb.SwitchboardClient, mode pb.SeedingMode, srcs ...*pb.SourceRef) *pb.Sandbox {
	t.Helper()
	stream, err := client.LaunchSandbox(context.Background(), &pb.LaunchSandboxRequest{
		Config:                  &pb.ConfigSnapshot{Name: "edit-me", SeedingMode: mode},
		Sources:                 srcs,
		OverrideResourceWarning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var done *pb.Sandbox
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("launch recv: %v", err)
		}
		if d := msg.GetDone(); d != nil {
			done = d
		}
	}
	if done == nil {
		t.Fatal("launch produced no terminal Sandbox")
	}
	return done
}

// addResult drains an AddSandboxSources stream into its parts.
type addResult struct {
	copies  int
	logs    int
	done    *pb.Sandbox
	blocked *pb.ResourceReport
	err     error
}

func addSources(t *testing.T, client pb.SwitchboardClient, id string, override bool, srcs ...*pb.SourceRef) addResult {
	t.Helper()
	stream, err := client.AddSandboxSources(context.Background(), &pb.AddSandboxSourcesRequest{
		SandboxId: id, Sources: srcs, OverrideResourceWarning: override,
	})
	if err != nil {
		return addResult{err: err}
	}
	var r addResult
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return r
		}
		if err != nil {
			r.err = err
			return r
		}
		switch {
		case msg.GetCopy() != nil:
			r.copies++
		case msg.GetLogLine() != "":
			r.logs++
		case msg.GetBlocked() != nil:
			r.blocked = msg.GetBlocked()
		case msg.GetDone() != nil:
			r.done = msg.GetDone()
		}
	}
}

func paths(sb *pb.Sandbox) []string {
	out := make([]string, 0, len(sb.GetSources()))
	for _, s := range sb.GetSources() {
		out = append(out, s.GetPath())
	}
	return out
}

func wantCode(t *testing.T, err error, code codes.Code, mention string) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok || st.Code() != code {
		t.Fatalf("err = %v, want gRPC code %v", err, code)
	}
	if mention != "" && !strings.Contains(st.Message(), mention) {
		t.Errorf("message %q should mention %q", st.Message(), mention)
	}
}

// AddSandboxSources streams copy progress then `done` carrying the extended
// source set; the folder lands in the workspace and the state is unchanged.
func TestAddSandboxSourcesStreamsProgressThenDone(t *testing.T) {
	client, dir := sourcesServer(t, &testRunner{running: map[string]bool{}})
	proj := mkSource(t, dir, "proj", false)
	sb := launchMode(t, client, pb.SeedingMode_SEEDING_MODE_DUPLICATE, proj)
	lib := mkSource(t, dir, "lib", false)

	r := addSources(t, client, sb.GetId(), true, lib)
	if r.err != nil {
		t.Fatalf("add: %v", r.err)
	}
	if r.copies == 0 {
		t.Error("expected copy progress frames before done")
	}
	if r.done == nil {
		t.Fatal("expected a terminal done frame")
	}
	if got := paths(r.done); len(got) != 2 || got[1] != lib.GetPath() {
		t.Errorf("done.sources = %v, want proj + lib", got)
	}
	if r.done.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want RUNNING", r.done.GetState())
	}
	if _, err := os.Stat(filepath.Join(sb.GetWorkspacePath(), "lib", "main.go")); err != nil {
		t.Errorf("added folder missing in the workspace: %v", err)
	}
}

// The resource gate mirrors launch: low disk yields `blocked` (nothing copied)
// without override, and the same request proceeds with it (FR-057).
func TestAddSandboxSourcesBlockedThenOverride(t *testing.T) {
	orig := resources.AvailableBytesFunc
	resources.AvailableBytesFunc = func(string) (uint64, error) { return 1, nil }
	defer func() { resources.AvailableBytesFunc = orig }()

	client, dir := sourcesServer(t, &testRunner{running: map[string]bool{}})
	proj := mkSource(t, dir, "proj", false)
	sb := launchMode(t, client, pb.SeedingMode_SEEDING_MODE_DUPLICATE, proj)
	lib := mkSource(t, dir, "lib", false)
	if err := os.WriteFile(filepath.Join(lib.GetPath(), "big"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	r := addSources(t, client, sb.GetId(), false, lib)
	if r.err != nil {
		t.Fatalf("add: %v", r.err)
	}
	if r.blocked == nil || r.blocked.GetOk() || r.done != nil {
		t.Fatalf("expected a blocked frame and no done; got blocked=%v done=%v", r.blocked, r.done)
	}
	if _, err := os.Stat(filepath.Join(sb.GetWorkspacePath(), "lib")); !os.IsNotExist(err) {
		t.Error("a blocked add must copy nothing")
	}

	r = addSources(t, client, sb.GetId(), true, lib)
	if r.err != nil || r.done == nil {
		t.Fatalf("override should proceed: err=%v done=%v", r.err, r.done)
	}
	if len(r.done.GetSources()) != 2 {
		t.Errorf("sources = %v, want 2 after override", paths(r.done))
	}
}

// Refusals arrive as the documented codes, before anything is copied.
func TestSourceEditRefusalCodes(t *testing.T) {
	client, dir := sourcesServer(t, &testRunner{running: map[string]bool{}})
	proj := mkSource(t, dir, "proj", false)
	sb := launchMode(t, client, pb.SeedingMode_SEEDING_MODE_DUPLICATE, proj)

	// Unknown sandbox.
	r := addSources(t, client, "nope", true, mkSource(t, dir, "lib", false))
	wantCode(t, r.err, codes.NotFound, "")
	if _, err := client.RemoveSandboxSources(context.Background(), &pb.RemoveSandboxSourcesRequest{SandboxId: "nope", SourcePaths: []string{proj.GetPath()}}); err == nil {
		t.Error("expected NOT_FOUND for an unknown sandbox on remove")
	} else {
		wantCode(t, err, codes.NotFound, "")
	}

	// Missing path → INVALID_ARGUMENT; collision → ALREADY_EXISTS.
	r = addSources(t, client, sb.GetId(), true, &pb.SourceRef{Path: filepath.Join(dir, "missing")})
	wantCode(t, r.err, codes.InvalidArgument, "missing")
	twin := mkSource(t, filepath.Join(dir, "other"), "proj", false)
	r = addSources(t, client, sb.GetId(), true, twin)
	wantCode(t, r.err, codes.AlreadyExists, `"proj"`)

	// Clone mode + non-repo → FAILED_PRECONDITION.
	repo := mkSource(t, dir, "repo", true)
	cl := launchMode(t, client, pb.SeedingMode_SEEDING_MODE_CLONE, repo)
	r = addSources(t, client, cl.GetId(), true, mkSource(t, dir, "plain", false))
	wantCode(t, r.err, codes.FailedPrecondition, `"plain"`)

	// Unknown removal target → NOT_FOUND; last source → FAILED_PRECONDITION.
	_, err := client.RemoveSandboxSources(context.Background(), &pb.RemoveSandboxSourcesRequest{SandboxId: sb.GetId(), SourcePaths: []string{filepath.Join(dir, "never")}})
	wantCode(t, err, codes.NotFound, "never")
	_, err = client.RemoveSandboxSources(context.Background(), &pb.RemoveSandboxSourcesRequest{SandboxId: sb.GetId(), SourcePaths: []string{proj.GetPath()}})
	wantCode(t, err, codes.FailedPrecondition, "at least one")
	if _, err := os.Stat(filepath.Join(sb.GetWorkspacePath(), "proj", "main.go")); err != nil {
		t.Error("a refused removal deleted the folder")
	}
}

// While an add holds the sandbox's latch, a second edit is refused with
// FAILED_PRECONDITION naming the in-flight operation (FR-066).
func TestSourceEditsRefusedWhileBusy(t *testing.T) {
	runner := &testRunner{running: map[string]bool{}, cloneEntered: make(chan struct{}, 4)}
	client, dir := sourcesServer(t, runner)
	repoA := mkSource(t, dir, "repo-a", true)
	// The launch clones too (ungated); drain its signal, then arm the gate so the
	// add's clone blocks inside the latch.
	sb := launchMode(t, client, pb.SeedingMode_SEEDING_MODE_CLONE, repoA)
	<-runner.cloneEntered
	gate := make(chan struct{})
	runner.setCloneGate(gate)
	repoB := mkSource(t, dir, "repo-b", true)

	results := make(chan addResult, 1)
	go func() { results <- addSources(t, client, sb.GetId(), true, repoB) }()
	select {
	case <-runner.cloneEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the add never reached the runner")
	}

	_, err := client.RemoveSandboxSources(context.Background(), &pb.RemoveSandboxSourcesRequest{SandboxId: sb.GetId(), SourcePaths: []string{repoA.GetPath()}})
	wantCode(t, err, codes.FailedPrecondition, "add sources in progress")
	second := addSources(t, client, sb.GetId(), true, mkSource(t, dir, "repo-c", true))
	wantCode(t, second.err, codes.FailedPrecondition, "add sources in progress")

	close(gate)
	select {
	case r := <-results:
		if r.err != nil || r.done == nil {
			t.Fatalf("the held add should complete once released: err=%v done=%v", r.err, r.done)
		}
		if len(r.done.GetSources()) != 2 {
			t.Errorf("sources = %v, want repo-a + repo-b", paths(r.done))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held add never completed")
	}
}

// RemoveSandboxSources deletes the copy and returns the updated record.
func TestRemoveSandboxSourcesOverGRPC(t *testing.T) {
	client, dir := sourcesServer(t, &testRunner{running: map[string]bool{}})
	proj, lib := mkSource(t, dir, "proj", false), mkSource(t, dir, "lib", false)
	sb := launchMode(t, client, pb.SeedingMode_SEEDING_MODE_DUPLICATE, proj, lib)

	out, err := client.RemoveSandboxSources(context.Background(), &pb.RemoveSandboxSourcesRequest{
		SandboxId: sb.GetId(), SourcePaths: []string{lib.GetPath()},
	})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := paths(out); len(got) != 1 || got[0] != proj.GetPath() {
		t.Errorf("sources = %v, want {proj}", got)
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want RUNNING", out.GetState())
	}
	if _, err := os.Stat(filepath.Join(sb.GetWorkspacePath(), "lib")); !os.IsNotExist(err) {
		t.Error("the removed folder's copy must be gone")
	}
	if _, err := os.Stat(lib.GetPath()); err != nil {
		t.Error("the origin must be untouched")
	}
}

// FR-062: an active service whose working_dir sits in a selected folder blocks
// the removal, naming the service and the remedy; a root working_dir does not;
// once the instance stops, the same removal proceeds.
func TestRemoveSandboxSourcesServiceGuard(t *testing.T) {
	client, mgr, supervisor, dir := serviceServer(t)
	sb := launchWithServices(t, mgr, dir,
		&pb.KitService{Name: "web", Command: "sleep 30", ListenPort: 3000, Location: pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX, WorkingDir: "proj/app"},
		&pb.KitService{Name: "db", Command: "sleep 30", ListenPort: 5432, Location: pb.ServiceLocation_SERVICE_LOCATION_ON_HOST},
	)
	lib := mkSource(t, dir, "lib", false)
	if _, err := mgr.AddSources(context.Background(), sb.GetId(), []*pb.SourceRef{lib}, nil, nil); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "proj")

	// Both services get a STARTING instance directly in the store — no process needed.
	web, _ := supervisor.Instances().Create(sb.GetId(), "web")
	supervisor.Instances().Create(sb.GetId(), "db")

	_, err := client.RemoveSandboxSources(context.Background(), &pb.RemoveSandboxSourcesRequest{SandboxId: sb.GetId(), SourcePaths: []string{proj}})
	wantCode(t, err, codes.FailedPrecondition, `service "web"`)
	if !strings.Contains(err.Error(), "stop it first") {
		t.Errorf("err = %v, want the remedy", err)
	}
	if _, err := os.Stat(filepath.Join(sb.GetWorkspacePath(), "proj")); err != nil {
		t.Error("nothing may be deleted on refusal")
	}

	// Removing lib is fine: web's working_dir is under proj, and db's is the root.
	if _, err := client.RemoveSandboxSources(context.Background(), &pb.RemoveSandboxSourcesRequest{SandboxId: sb.GetId(), SourcePaths: []string{lib.GetPath()}}); err != nil {
		t.Fatalf("a root working_dir must not block: %v", err)
	}
	if _, err := mgr.AddSources(context.Background(), sb.GetId(), []*pb.SourceRef{lib}, nil, nil); err != nil {
		t.Fatal(err)
	}

	// Stop web: the same removal now proceeds.
	supervisor.Instances().Update(web.GetId(), func(i *pb.ServiceInstance) { i.State = pb.ServiceState_SERVICE_STATE_STOPPED })
	out, err := client.RemoveSandboxSources(context.Background(), &pb.RemoveSandboxSourcesRequest{SandboxId: sb.GetId(), SourcePaths: []string{proj}})
	if err != nil {
		t.Fatalf("removal after the service stopped: %v", err)
	}
	if got := paths(out); len(got) != 1 || got[0] != lib.GetPath() {
		t.Errorf("sources = %v, want {lib}", got)
	}
}

// FR-067: every subscriber receives Event.sandbox_changed carrying the edited
// sources after an add and after a removal.
func TestSourceEditsBroadcastSandboxChanged(t *testing.T) {
	client, dir := sourcesServer(t, &testRunner{running: map[string]bool{}})
	proj := mkSource(t, dir, "proj", false)
	sb := launchMode(t, client, pb.SeedingMode_SEEDING_MODE_DUPLICATE, proj)
	lib := mkSource(t, dir, "lib", false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Subscribe(ctx, &pb.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the subscriber register

	// One reader forwards every sandbox_changed for sb; waitFor blocks until one
	// arrives whose source set matches.
	got := make(chan *pb.Sandbox, 16)
	go func() {
		for {
			ev, err := stream.Recv()
			if err != nil {
				return
			}
			if s := ev.GetSandboxChanged(); s != nil && s.GetId() == sb.GetId() {
				got <- s
			}
		}
	}()
	waitFor := func(want []string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case s := <-got:
				if strings.Join(paths(s), ",") == strings.Join(want, ",") {
					return
				}
			case <-deadline:
				t.Fatalf("no sandbox_changed with sources %v arrived", want)
			}
		}
	}

	if r := addSources(t, client, sb.GetId(), true, lib); r.err != nil {
		t.Fatal(r.err)
	}
	waitFor([]string{proj.GetPath(), lib.GetPath()})

	if _, err := client.RemoveSandboxSources(ctx, &pb.RemoveSandboxSourcesRequest{SandboxId: sb.GetId(), SourcePaths: []string{lib.GetPath()}}); err != nil {
		t.Fatal(err)
	}
	waitFor([]string{proj.GetPath()})
}
