package registry

import (
	"errors"
	"sync"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestMcpSettingsAbsentIsDefault(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	s, err := r.GetMcpSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.GetMarks()) != 0 || s.GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_UNSPECIFIED {
		t.Errorf("absent settings = %+v, want zero value", s)
	}
}

func TestMcpSettingsRoundTrip(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	out, err := r.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		s.DefaultAttachMode = pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE
		if s.Marks == nil {
			s.Marks = map[string]bool{}
		}
		s.Marks["notion"] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.GetMarks()["notion"] {
		t.Error("update result should carry the mark")
	}
	got, err := r.GetMcpSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetMarks()["notion"] || got.GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE {
		t.Errorf("persisted settings = %+v", got)
	}
	// A failing mutate leaves the record untouched.
	if _, err := r.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error { delete(s.Marks, "notion"); return errors.New("nope") }); err == nil {
		t.Fatal("expected the mutate error")
	}
	again, _ := r.GetMcpSettings()
	if !again.GetMarks()["notion"] {
		t.Error("a failed update must not persist")
	}
}

func TestMcpSettingsConcurrentUpdatesSerialize(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = r.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
				if s.Marks == nil {
					s.Marks = map[string]bool{}
				}
				s.Marks[string(rune('a'+i))] = true
				return nil
			})
		}(i)
	}
	wg.Wait()
	got, _ := r.GetMcpSettings()
	if len(got.GetMarks()) != 20 {
		t.Errorf("marks = %d, want 20 (updates lost)", len(got.GetMarks()))
	}
}
