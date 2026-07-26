package escapehatch

import (
	"errors"
	"reflect"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestMatchArgsSubcommands(t *testing.T) {
	cmd := &pb.EscapeHatchCommand{Command: "pnpm", Subcommands: []string{"install", "run dev"}}

	// An allowed subcommand tokenizes the TRUSTED entry (multi-word entries split).
	tokens, err := matchArgs(cmd, "run dev")
	if err != nil {
		t.Fatalf("allowed subcommand rejected: %v", err)
	}
	if !reflect.DeepEqual(tokens, []string{"run", "dev"}) {
		t.Errorf("tokens = %v, want [run dev]", tokens)
	}

	// Anything not in the list is rejected (ErrInvalidArgs -> HTTP 400).
	if _, err := matchArgs(cmd, "publish"); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("disallowed subcommand err = %v, want ErrInvalidArgs", err)
	}
	// Empty args when a subcommand is required is a rejection, not a bare run.
	if _, err := matchArgs(cmd, ""); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("empty args err = %v, want ErrInvalidArgs", err)
	}
}

func TestMatchArgsPattern(t *testing.T) {
	cmd := &pb.EscapeHatchCommand{Command: "pnpm", ArgsPattern: "install|dev|build"}

	tokens, err := matchArgs(cmd, "install")
	if err != nil || !reflect.DeepEqual(tokens, []string{"install"}) {
		t.Fatalf("pattern match: tokens=%v err=%v", tokens, err)
	}
	// The pattern is anchored: a partial match must NOT pass.
	if _, err := matchArgs(cmd, "install-extra"); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("partial match err = %v, want ErrInvalidArgs (pattern must be full-string)", err)
	}
	if _, err := matchArgs(cmd, "rm -rf /"); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("non-matching args err = %v, want ErrInvalidArgs", err)
	}
}

func TestMatchArgsNoneAllowed(t *testing.T) {
	cmd := &pb.EscapeHatchCommand{Command: "make build"}
	if tokens, err := matchArgs(cmd, ""); err != nil || tokens != nil {
		t.Errorf("no-arg command with empty args: tokens=%v err=%v", tokens, err)
	}
	if _, err := matchArgs(cmd, "anything"); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("no-arg command given args err = %v, want ErrInvalidArgs", err)
	}
}

func TestMatchWorkspace(t *testing.T) {
	cmd := &pb.EscapeHatchCommand{Workspaces: []string{"src/apps/*", "packages/shared"}}

	cases := []struct {
		sel     string
		wantDir string
		wantErr bool
	}{
		{"src/apps/web", "src/apps/web", false},       // glob match
		{"packages/shared", "packages/shared", false}, // literal match
		{"src/apps/web/nested", "", true},             // glob does not cross a separator
		{"src/services/api", "", true},                // not allowed
		{"", "", true},                                // required when workspaces declared
		{"../escape", "", true},                       // escapes the workspace
		{"/etc", "", true},                            // absolute
	}
	for _, tc := range cases {
		dir, err := matchWorkspace(cmd, tc.sel)
		if tc.wantErr {
			if !errors.Is(err, ErrInvalidWorkspace) {
				t.Errorf("matchWorkspace(%q) err = %v, want ErrInvalidWorkspace", tc.sel, err)
			}
			continue
		}
		if err != nil || dir != tc.wantDir {
			t.Errorf("matchWorkspace(%q) = (%q, %v), want (%q, nil)", tc.sel, dir, err, tc.wantDir)
		}
	}
}

func TestMatchWorkspaceNoneDeclared(t *testing.T) {
	cmd := &pb.EscapeHatchCommand{WorkingDir: "sub/dir"}
	// No workspaces => a selector is rejected, and empty falls back to working_dir.
	if _, err := matchWorkspace(cmd, "src/apps/web"); !errors.Is(err, ErrInvalidWorkspace) {
		t.Errorf("selector on a non-targetable command err = %v, want ErrInvalidWorkspace", err)
	}
	if dir, err := matchWorkspace(cmd, ""); err != nil || dir != "sub/dir" {
		t.Errorf("empty selector = (%q, %v), want (sub/dir, nil)", dir, err)
	}
}
