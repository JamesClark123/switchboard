package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The sbx CLI is not installed in this environment, so these argv assertions are
// the guard against the documented port-forwarding surface drifting away from what
// the runner builds. See specs/006-port-forwarding/contracts/sbx-ports-cli.md.

func TestPublishPortArgv(t *testing.T) {
	bin, log := argRecordingSbx(t, 0)
	r := &SbxRunner{Bin: bin}
	if err := r.PublishPort(context.Background(), "sb1", 51234, 3000); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(readArgs(t, log), " ")
	if got != "ports sb1 --publish 127.0.0.1:51234:3000/tcp" {
		t.Errorf("args = %q, want the documented publish form", got)
	}
}

func TestPublishPortPinsLoopbackHostIP(t *testing.T) {
	// Omitting the host IP would bind every interface and put a forwarded sandbox
	// service on the LAN. The spec assumes only the developer's machine can reach it.
	bin, log := argRecordingSbx(t, 0)
	r := &SbxRunner{Bin: bin}
	if err := r.PublishPort(context.Background(), "sb1", 40000, 8080); err != nil {
		t.Fatal(err)
	}
	spec := readArgs(t, log)[3]
	if !strings.HasPrefix(spec, "127.0.0.1:") {
		t.Errorf("port spec %q must pin the loopback host IP", spec)
	}
	if strings.HasPrefix(spec, "0.0.0.0:") || spec == "40000:8080/tcp" {
		t.Errorf("port spec %q must never bind all interfaces", spec)
	}
}

func TestUnpublishPortMirrorsPublishExactly(t *testing.T) {
	pubBin, pubLog := argRecordingSbx(t, 0)
	unpubBin, unpubLog := argRecordingSbx(t, 0)

	if err := (&SbxRunner{Bin: pubBin}).PublishPort(context.Background(), "sb1", 51234, 3000); err != nil {
		t.Fatal(err)
	}
	if err := (&SbxRunner{Bin: unpubBin}).UnpublishPort(context.Background(), "sb1", 51234, 3000); err != nil {
		t.Fatal(err)
	}

	pub, unpub := readArgs(t, pubLog), readArgs(t, unpubLog)
	if pub[1] != unpub[1] {
		t.Errorf("sandbox ref differs: %q vs %q", pub[1], unpub[1])
	}
	if pub[2] != "--publish" || unpub[2] != "--unpublish" {
		t.Errorf("flags = %q / %q, want --publish / --unpublish", pub[2], unpub[2])
	}
	if pub[3] != unpub[3] {
		t.Errorf("the port triple must mirror exactly: published %q, unpublished %q", pub[3], unpub[3])
	}
}

func TestPublishPortSurfacesFailure(t *testing.T) {
	bin, _ := argRecordingSbx(t, 1)
	r := &SbxRunner{Bin: bin}
	if err := r.PublishPort(context.Background(), "sb1", 51234, 3000); err == nil {
		t.Error("a non-zero sbx exit must surface as an error so allocation can retry")
	}
}

func TestExecArgv(t *testing.T) {
	bin, log := argRecordingSbx(t, 0)
	r := &SbxRunner{Bin: bin}

	cmd := r.Exec(context.Background(), "sb1", []string{"/bin/sh", "-c", "echo hi"})
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	got := readArgs(t, log)
	want := []string{"exec", "sb1", "--", "/bin/sh", "-c", "echo hi"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}

func TestExecDoesNotStartTheCommand(t *testing.T) {
	// Exec must BUILD the command so the caller can attach pipes and a process
	// group before it runs; starting it here would make the in-sandbox launcher
	// unable to capture output or record the PGID.
	bin, log := argRecordingSbx(t, 0)
	r := &SbxRunner{Bin: bin}

	cmd := r.Exec(context.Background(), "sb1", []string{"true"})
	if cmd.Process != nil {
		t.Error("Exec must not start the process")
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("stub sbx must not have been invoked by Exec alone")
	}
}
