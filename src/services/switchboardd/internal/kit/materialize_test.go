package kit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

const sampleSpec = "schemaVersion: \"1\"\nkind: mixin\nname: ruff\n"

func TestWriteMaterializesSpecYaml(t *testing.T) {
	m := &Materializer{Root: t.TempDir()}
	dir, err := m.Write(&pb.KitSpec{Id: "ruff", SpecYaml: sampleSpec})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(m.Root, "ruff"); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	got, err := os.ReadFile(filepath.Join(dir, "spec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sampleSpec {
		t.Errorf("spec.yaml = %q, want it written verbatim", got)
	}
}

// The client owns the kit; the materialized dir is a cache of it. An edit that
// removes content must not leave the old file behind.
func TestWriteRebuildsDirOnEdit(t *testing.T) {
	m := &Materializer{Root: t.TempDir()}
	dir, err := m.Write(&pb.KitSpec{Id: "ruff", SpecYaml: sampleSpec})
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "stale.txt")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write(&pb.KitSpec{Id: "ruff", SpecYaml: "kind: mixin\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale file survived a kit rewrite; the dir was not rebuilt")
	}
}

// Kit ids arrive from a network peer and are joined onto Root — anything that could
// escape it must be refused, not sanitized.
func TestWriteRejectsUnsafeIDs(t *testing.T) {
	root := t.TempDir()
	m := &Materializer{Root: root}
	for _, id := range []string{
		"", "..", ".", "../evil", "a/b", `a\b`, "/abs", "Ruff", "kit name", "kit.yaml",
	} {
		if _, err := m.Write(&pb.KitSpec{Id: id, SpecYaml: sampleSpec}); err == nil {
			t.Errorf("Write(%q) succeeded; want rejection", id)
		}
	}
	// Nothing may have been created outside the root.
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil")); !os.IsNotExist(err) {
		t.Error("a rejected id still produced a directory outside the kit root")
	}
}

func TestWriteRejectsEmptySpec(t *testing.T) {
	m := &Materializer{Root: t.TempDir()}
	if _, err := m.Write(&pb.KitSpec{Id: "ruff", SpecYaml: "  \n"}); err == nil {
		t.Error("expected an empty spec.yaml to be rejected")
	}
}

func TestWriteRequiresRoot(t *testing.T) {
	m := &Materializer{}
	if _, err := m.Write(&pb.KitSpec{Id: "ruff", SpecYaml: sampleSpec}); err == nil {
		t.Error("expected an unconfigured kit root to be rejected")
	}
}

// An external source is sbx's to resolve — it must pass through untouched.
func TestResolvePassesExternalSourcesThrough(t *testing.T) {
	m := &Materializer{Root: t.TempDir()}
	for _, src := range []string{
		"./my-kit/",
		"my-kit-1.0.zip",
		"git+https://github.com/docker/sbx-kits-contrib.git#ref=v0.1.0&dir=code-server",
		"ghcr.io/myorg/my-kit:1.0",
	} {
		got, err := m.Resolve(&pb.KitRef{Ref: &pb.KitRef_Source{Source: src}})
		if err != nil {
			t.Fatalf("Resolve(%q): %v", src, err)
		}
		if got != src {
			t.Errorf("Resolve(%q) = %q, want it unchanged", src, got)
		}
	}
}

func TestResolveInlineSpecMaterializes(t *testing.T) {
	m := &Materializer{Root: t.TempDir()}
	got, err := m.Resolve(&pb.KitRef{Ref: &pb.KitRef_Spec{Spec: &pb.KitSpec{Id: "ruff", SpecYaml: sampleSpec}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(got, "spec.yaml")); err != nil {
		t.Errorf("inline kit was not materialized: %v", err)
	}
}

func TestResolveRejectsEmptyRef(t *testing.T) {
	m := &Materializer{Root: t.TempDir()}
	if _, err := m.Resolve(&pb.KitRef{}); err == nil {
		t.Error("expected a KitRef with neither spec nor source to be rejected")
	}
	if _, err := m.Resolve(&pb.KitRef{Ref: &pb.KitRef_Source{Source: "  "}}); err == nil {
		t.Error("expected a blank source to be rejected")
	}
}

// sbx composes stacked kits, so author order is meaningful and must survive.
func TestResolveAllPreservesOrder(t *testing.T) {
	m := &Materializer{Root: t.TempDir()}
	refs := []*pb.KitRef{
		{Ref: &pb.KitRef_Source{Source: "first"}},
		{Ref: &pb.KitRef_Spec{Spec: &pb.KitSpec{Id: "second", SpecYaml: sampleSpec}}},
		{Ref: &pb.KitRef_Source{Source: "third"}},
	}
	got, err := m.ResolveAll(refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "first" || got[2] != "third" {
		t.Fatalf("ResolveAll = %v, want order preserved", got)
	}
	if filepath.Base(got[1]) != "second" {
		t.Errorf("inline kit resolved to %q, want it materialized as .../second", got[1])
	}
}

// A bad kit must name which one failed — the launch flow reports this before copying.
func TestResolveAllReportsWhichKitFailed(t *testing.T) {
	m := &Materializer{Root: t.TempDir()}
	_, err := m.ResolveAll([]*pb.KitRef{
		{Ref: &pb.KitRef_Source{Source: "ok"}},
		{Ref: &pb.KitRef_Spec{Spec: &pb.KitSpec{Id: "../evil", SpecYaml: sampleSpec}}},
	})
	if err == nil {
		t.Fatal("expected an unsafe kit id to fail ResolveAll")
	}
	if want := "kit 2"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q should identify the failing kit (%q)", err, want)
	}
}
