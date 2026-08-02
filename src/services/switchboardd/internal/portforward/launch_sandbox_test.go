package portforward

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestSandboxWrapperShape(t *testing.T) {
	script := sandboxWrapper("api", "pnpm dev --host 0.0.0.0")

	if !strings.HasPrefix(script, "cd 'api' && ") {
		t.Errorf("script must cd into the working dir first:\n%s", script)
	}
	if !strings.Contains(script, "setsid") {
		t.Error("the wrapper must use setsid so the tree can be killed from outside")
	}
	if !strings.Contains(script, "command -v setsid") {
		t.Error("the wrapper must fall back when setsid is unavailable")
	}
	if !strings.Contains(script, `echo "swb-pgid:$$"`) {
		t.Errorf("the wrapper must announce its PGID unexpanded:\n%s", script)
	}
	if !strings.Contains(script, "exec pnpm dev --host 0.0.0.0") {
		t.Errorf("the declared command must be exec'd verbatim:\n%s", script)
	}
}

func TestSandboxWrapperOmitsCdWhenNoWorkingDir(t *testing.T) {
	script := sandboxWrapper("", "pnpm dev")
	if strings.Contains(script, "cd ") {
		t.Errorf("with no working dir the wrapper must not cd — sbx exec already starts in the workspace:\n%s", script)
	}
}

// Service commands are arbitrary author-written shell, and the wrapper nests one
// shell inside another. Bad quoting would not just break exotic commands — it would
// silently change what an ordinary one means.
func TestSandboxWrapperQuotesSingleQuotesInTheCommand(t *testing.T) {
	script := sandboxWrapper("", `sh -c 'echo hi'`)
	if strings.Contains(script, `'sh -c 'echo hi''`) {
		t.Errorf("single quotes must be escaped, not passed through:\n%s", script)
	}
	if !strings.Contains(script, `'\''`) {
		t.Errorf("expected the '\\'' escape form:\n%s", script)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("plain"); got != "'plain'" {
		t.Errorf("shellQuote(plain) = %q", got)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote(it's) = %q", got)
	}
}

func TestLaunchInSandboxCapturesPGIDAndExcludesTheMarker(t *testing.T) {
	s, _, runner, _ := newTestSupervisor(t)
	// The fake Runner runs argv on the host, so the real wrapper executes here.
	proc, err := s.launchInSandbox(context.Background(), "sb1-ref", "", "echo hello; sleep 0.2")
	if err != nil {
		t.Fatal(err)
	}
	if proc.pgid <= 0 {
		t.Fatalf("pgid = %d, want the announced process-group id", proc.pgid)
	}

	select {
	case <-proc.exited:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not finish")
	}

	out, _ := proc.output()
	if !strings.Contains(out, "hello") {
		t.Errorf("captured output = %q, want the command's stdout", out)
	}
	// The marker is switchboard plumbing; a developer reading a crash log should
	// never see it.
	if strings.Contains(out, pgidMarker) {
		t.Errorf("captured output must not contain the PGID marker: %q", out)
	}

	argv := runner.lastExec()
	if len(argv) < 3 || argv[0] != "/bin/sh" || argv[1] != "-c" {
		t.Errorf("exec argv = %v, want /bin/sh -c <script>", argv)
	}
}

func TestLaunchInSandboxCapturesStderr(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t)
	proc, err := s.launchInSandbox(context.Background(), "", "", "echo oops >&2")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-proc.exited:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not finish")
	}
	if out, _ := proc.output(); !strings.Contains(out, "oops") {
		t.Errorf("stderr after the marker line must be captured, got %q", out)
	}
}

func TestLaunchInSandboxRecordsExitError(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t)
	proc, err := s.launchInSandbox(context.Background(), "", "", "exit 3")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-proc.exited:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not finish")
	}
	if proc.err() == nil {
		t.Error("a non-zero exit must be recorded so it can be reported")
	}
	if !proc.hasExited() {
		t.Error("hasExited must be true once the process is reaped")
	}
}

func TestLaunchOnHostExportsThePortAndSetsTheProcessGroup(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t)
	dir := t.TempDir()

	proc, err := s.launchOnHost(context.Background(), dir, "", "echo port=$PORT/$SWITCHBOARD_SERVICE_PORT; pwd", 51999)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-proc.exited:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not finish")
	}
	out, _ := proc.output()
	if !strings.Contains(out, "port=51999/51999") {
		t.Errorf("output = %q, want both PORT vars set to the effective port", out)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("output = %q, want the command to run in the sandbox workspace %q", out, dir)
	}
	if proc.pgid <= 0 {
		t.Error("an on-host process must be its own group leader so the tree can be signalled")
	}
}

func TestResolveHostWorkdirContainment(t *testing.T) {
	ws := t.TempDir()

	if got, err := resolveHostWorkdir(ws, ""); err != nil || got != ws {
		t.Errorf("empty dir must resolve to the workspace, got (%q, %v)", got, err)
	}
	if _, err := resolveHostWorkdir(ws, "/etc"); err == nil {
		t.Error("an absolute working dir must be refused")
	}
	if _, err := resolveHostWorkdir(ws, "../../etc"); err == nil {
		t.Error("an escaping working dir must be refused")
	}
	if _, err := resolveHostWorkdir(ws, "missing"); err == nil {
		t.Error("a non-existent directory must be refused")
	}
}

func TestLaunchOnHostRejectsABadWorkingDir(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t)
	if _, err := s.launchOnHost(context.Background(), t.TempDir(), "../escape", "true", 1); err == nil {
		t.Error("launch must refuse a working dir that escapes the workspace")
	}
}

func TestLaunchDispatchesOnLocation(t *testing.T) {
	s, _, runner, _ := newTestSupervisor(t)
	sb := runningSandbox("sb1")
	sb.WorkspacePath = t.TempDir()

	inSbxSvc := svc("web", "true", 3000, inSandbox)
	if _, err := s.launch(context.Background(), sb, inSbxSvc, "true", true, 3000); err != nil {
		t.Fatal(err)
	}
	if runner.lastExec() == nil {
		t.Error("an in-sandbox service must go through Runner.Exec")
	}

	before := len(runner.execArgv)
	hostSvcDecl := &pb.KitService{Name: "worker", Command: "true", ListenPort: 7000, Location: onHost}
	if _, err := s.launch(context.Background(), sb, hostSvcDecl, "true", false, 7000); err != nil {
		t.Fatal(err)
	}
	if len(runner.execArgv) != before {
		t.Error("an on-host service must NOT go through the sandbox runner")
	}
}
