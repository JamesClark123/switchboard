package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// statefulSbx is a fake `sbx` whose MCP registrations live in a state dir:
// `mcp add` writes <name>.srv (unless add.fail exists), `mcp rm` deletes it
// (unless rm.fail exists), `mcp ls` prints the table from the state, `mcp
// inspect` prints <name>.inspect (or {}), and `mcp auth status` prints
// <name>.auth (or unauthorized). <step>.hang makes a step sleep; notice makes
// the JSON reads print an update notice on stderr; rm.fail.breakls makes a
// failing rm break later lists.
const statefulScript = `#!/usr/bin/env bash
echo "$*" >> "$LOG"
S="$STATE"
case "$1 $2" in
"mcp ls")
  [ -e "$S/ls.fail" ] && { echo "ls broke" >&2; exit 1; }
  [ -e "$S/ls.hang" ] && sleep 30
  printf '%-16s%-16s%-14s%-12s%s\n' NAME TRANSPORT STATUS GATEWAY DETAIL
  for f in "$S"/*.srv; do [ -e "$f" ] || continue; n=$(basename "$f" .srv)
    printf '%-16s%-16s%-14s%-12s%s\n' "$n" "$(cat "$f")" ready local x; done ;;
"mcp add")
  [ -e "$S/add.hang" ] && sleep 30
  [ -e "$S/add.fail" ] && { echo "add refused by host" >&2; exit 1; }
  case "$*" in *--url*) t="remote http" ;; *) t="stdio" ;; esac
  echo "$t" > "$S/$3.srv"; echo "MCP server \"$3\" added." ;;
"mcp rm")
  [ -e "$S/rm.fail" ] && { echo "rm refused by host" >&2; [ -e "$S/rm.fail.removes" ] && rm -f "$S/$3.srv"; [ -e "$S/rm.fail.breakls" ] && touch "$S/ls.fail"; exit 1; }
  [ -e "$S/$3.srv" ] || { echo "MCP server \"$3\" is not registered" >&2; exit 1; }
  rm -f "$S/$3.srv"; echo "MCP server \"$3\" removed." ;;
"mcp inspect")
  [ -e "$S/inspect.fail" ] && { echo "inspect broke" >&2; exit 1; }
  [ -e "$S/notice" ] && echo "a newer sbx is available" >&2
  if [ -e "$S/$3.inspect" ]; then cat "$S/$3.inspect"; else echo '{}'; fi ;;
"mcp auth")
  if [ "$3" = "status" ]; then
    [ -e "$S/notice" ] && echo "a newer sbx is available" >&2
    if [ -e "$S/$4.auth" ]; then cat "$S/$4.auth"; else echo "[{\"server_name\":\"$4\",\"status\":\"unauthorized\"}]"; fi
  else
    [ -e "$S/auth.hang" ] && sleep 30
    echo "authorized"
  fi ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`

// fakeSecrets is the Secrets interface over a map, with failure injection and
// a call log. Values are kept to assert stores, never logged.
type fakeSecrets struct {
	mu      sync.Mutex
	vals    map[string]string
	failSet map[string]bool
	failRm  map[string]bool
	listErr error
	calls   []string
	events  int
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{vals: map[string]string{}, failSet: map[string]bool{}, failRm: map[string]bool{}}
}

func (f *fakeSecrets) ListGlobal(context.Context) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "ls")
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := map[string]bool{}
	for n := range f.vals {
		out[n] = true
	}
	return out, nil
}

func (f *fakeSecrets) SetQuiet(_ context.Context, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "set "+name)
	if f.failSet[name] {
		// Echoes the value, as a CLI might: the save must redact it.
		return errors.New("sbx secret set " + name + ": store refused: " + value)
	}
	f.vals[name] = value
	return nil
}

func (f *fakeSecrets) RemoveQuiet(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "rm "+name)
	if f.failRm[name] {
		return "denied", errors.New("sbx secret rm " + name + ": denied")
	}
	if _, ok := f.vals[name]; !ok {
		return "secret not found", errors.New("sbx secret rm " + name + ": secret not found")
	}
	delete(f.vals, name)
	return "removed", nil
}

func (f *fakeSecrets) EmitChanged() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events++
}

func (f *fakeSecrets) has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.vals[name]
	return ok
}

func (f *fakeSecrets) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// faultyStore wraps the registry so tests can fail settings writes.
type faultyStore struct {
	*registry.Registry
	mu       sync.Mutex
	failNext int // fail this many UpdateMcpSettings calls
}

func (f *faultyStore) UpdateMcpSettings(mutate func(*pb.McpGatewaySettings) error) (*pb.McpGatewaySettings, error) {
	f.mu.Lock()
	if f.failNext > 0 {
		f.failNext--
		f.mu.Unlock()
		return nil, errors.New("registry write failed")
	}
	f.mu.Unlock()
	return f.Registry.UpdateMcpSettings(mutate)
}

func (f *faultyStore) setFail(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = n
}

type saveEnv struct {
	t       *testing.T
	m       *Manager
	reg     *registry.Registry
	store   *faultyStore
	sec     *fakeSecrets
	state   string
	log     string
	bin     string
	mu      sync.Mutex
	events  []*pb.Event
	ended   []string
	cancel  context.CancelFunc
	ctx     context.Context
	options []Option
}

// newSaveEnv builds a manager on the stateful fake sbx and fake secrets.
func newSaveEnv(t *testing.T, opts ...Option) *saveEnv {
	t.Helper()
	dir := t.TempDir()
	e := &saveEnv{t: t, sec: newFakeSecrets(), state: filepath.Join(dir, "state"), log: filepath.Join(dir, "argv.log"), bin: filepath.Join(dir, "sbx")}
	if err := os.MkdirAll(e.state, 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(statefulScript, "#!/usr/bin/env bash\n", "#!/usr/bin/env bash\nLOG='"+e.log+"'\nSTATE='"+e.state+"'\n", 1)
	if err := os.WriteFile(e.bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	e.reg = reg
	e.store = &faultyStore{Registry: reg}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	t.Cleanup(e.cancel)
	e.options = opts
	e.m = e.newManager()
	return e
}

// newManager starts a manager on the same registry and host (a daemon restart).
func (e *saveEnv) newManager() *Manager {
	opts := append([]Option{
		WithSecrets(e.sec), WithContext(e.ctx),
		WithReplaceEnded(func(name string, registered bool) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.ended = append(e.ended, name+map[bool]string{true: ":registered", false: ":removed"}[registered])
		}),
	}, e.options...)
	return NewManager(&CLI{Bin: e.bin}, e.store, "host-1", func(ev *pb.Event) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.events = append(e.events, ev)
	}, opts...)
}

func (e *saveEnv) touch(names ...string) {
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(e.state, n), nil, 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *saveEnv) rm(names ...string) {
	for _, n := range names {
		_ = os.Remove(filepath.Join(e.state, n))
	}
}

func (e *saveEnv) write(name, content string) {
	if err := os.WriteFile(filepath.Join(e.state, name), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *saveEnv) registered(name string) bool {
	_, err := os.Stat(filepath.Join(e.state, name+".srv"))
	return err == nil
}

func (e *saveEnv) argv() []string { return readLog(e.t, e.log) }

func (e *saveEnv) clearLog() { _ = os.Remove(e.log) }

func (e *saveEnv) settings() *pb.McpGatewaySettings {
	s, err := e.reg.GetMcpSettings()
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *saveEnv) outcomes() []*pb.McpSaveOutcome {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*pb.McpSaveOutcome
	for _, ev := range e.events {
		if o := ev.GetMcpGatewayChanged().GetOutcome(); o != nil {
			out = append(out, o)
		}
	}
	return out
}

func (e *saveEnv) endedHooks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ended...)
}

// remote is a remote definition.
func remote(url string, headers ...string) *pb.McpServerDefinition {
	return &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: url, Headers: headers}
}

// add registers name through a new-server save and fails the test on error.
func (e *saveEnv) add(name string, def *pb.McpServerDefinition, mark bool, secrets map[string]string) *pb.McpSaveOutcome {
	e.t.Helper()
	out, err := e.m.Save(context.Background(), &pb.SaveMcpServerRequest{Name: name, Definition: def, IsNew: true, AttachByDefault: mark, SecretValues: secrets})
	if err != nil {
		e.t.Fatalf("add %s: %v", name, err)
	}
	return out
}

// open reads the edit view and fails the test on error.
func (e *saveEnv) open(name string) *pb.McpDefinitionView {
	e.t.Helper()
	v, err := e.m.Definition(context.Background(), name, false)
	if err != nil {
		e.t.Fatalf("definition %s: %v", name, err)
	}
	return v
}

// edit saves an edit opened from view.
func (e *saveEnv) edit(view *pb.McpDefinitionView, def *pb.McpServerDefinition, mark bool, secrets map[string]string, mod ...func(*pb.SaveMcpServerRequest)) (*pb.McpSaveOutcome, error) {
	req := &pb.SaveMcpServerRequest{Name: view.GetName(), Definition: def, AttachByDefault: mark, Fingerprint: view.GetFingerprint(), SecretValues: secrets,
		ConfirmDiscardAuthorization: true, ConfirmDropUnsupported: true}
	for _, f := range mod {
		f(req)
	}
	return e.m.Save(context.Background(), req)
}

func refusalReason(err error) pb.McpSaveRefusal_Reason {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Reason
	}
	return pb.McpSaveRefusal_REASON_UNSPECIFIED
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func hasPrefix(list []string, p string) bool {
	for _, x := range list {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}

// waitFor polls cond for up to 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
