package vscode

import (
	"os/exec"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestCodeArgs(t *testing.T) {
	// Local: open the folder directly.
	if got := codeArgs("/home/me/switchboard/workspace/abc", ""); len(got) != 1 || got[0] != "/home/me/switchboard/workspace/abc" {
		t.Errorf("local args = %v", got)
	}
	// Remote: open the folder over Remote-SSH.
	got := codeArgs("/srv/ws/abc", "user@box")
	want := []string{"--remote", "ssh-remote+user@box", "/srv/ws/abc"}
	if len(got) != len(want) {
		t.Fatalf("remote args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("remote args = %v, want %v", got, want)
		}
	}
}

func TestOpenLocalAndRemote(t *testing.T) {
	var gotArgs []string
	o := &Opener{CodeBin: "code", Run: func(c *exec.Cmd) error {
		gotArgs = c.Args
		return nil
	}}

	// Local: `code <controlled folder>`.
	if err := o.Open(&pb.VSCodeTarget{WorkspacePath: "/ws/abc"}, ""); err != nil {
		t.Fatal(err)
	}
	if len(gotArgs) != 2 || gotArgs[1] != "/ws/abc" {
		t.Errorf("local args = %v, want [code /ws/abc]", gotArgs)
	}

	// Remote: `code --remote ssh-remote+<target> <controlled folder>`.
	if err := o.Open(&pb.VSCodeTarget{WorkspacePath: "/ws/abc"}, "user@box"); err != nil {
		t.Fatal(err)
	}
	want := []string{"code", "--remote", "ssh-remote+user@box", "/ws/abc"}
	if len(gotArgs) != len(want) {
		t.Fatalf("remote args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("remote args = %v, want %v", gotArgs, want)
		}
	}
}

func TestOpenErrors(t *testing.T) {
	o := &Opener{CodeBin: "code", Run: func(*exec.Cmd) error { return nil }}
	if err := o.Open(&pb.VSCodeTarget{}, ""); err == nil {
		t.Error("expected error for empty workspace path")
	}

	failing := &Opener{CodeBin: "code", Run: func(*exec.Cmd) error { return exec.ErrNotFound }}
	if err := failing.Open(&pb.VSCodeTarget{WorkspacePath: "/ws/abc"}, ""); err == nil {
		t.Error("expected error when the launcher fails")
	}
}

func TestNewOpener(t *testing.T) {
	o := NewOpener("code-insiders")
	if o.CodeBin != "code-insiders" || o.Run == nil {
		t.Errorf("NewOpener wrong: %+v", o)
	}
}
