package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestThirdLine(t *testing.T) {
	cases := map[string]*pb.Sandbox{
		"mcp: notion · orchestrator: review-bot": {McpServers: []string{"notion"}, Controller: &pb.SandboxController{Name: "review-bot"}},
		"mcp: notion":                          {McpServers: []string{"notion"}},
		"mcp: a, b (exclusive)":                {McpServers: []string{"a", "b"}, McpAttachMode: pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE},
		"orchestrator: review-bot":             {Controller: &pb.SandboxController{Name: "review-bot"}},
		"orchestrator: review-bot (reasoning)": {Controller: &pb.SandboxController{Name: "review-bot", Reasoning: true}},
		"orchestrator: deadbeef":               {Controller: &pb.SandboxController{OrchestratorId: "deadbeef-1234"}},
		"":                                     {},
	}
	for want, sb := range cases {
		if got := thirdLine(sb); got != want {
			t.Errorf("thirdLine = %q, want %q", got, want)
		}
	}
}

func TestSandboxDelegateRendersThreeLinesAndTruncates(t *testing.T) {
	d := newSandboxDelegate()
	if d.Height() != 3 || d.Spacing() != 0 || d.Update(nil, nil) != nil {
		t.Fatal("delegate shape")
	}
	l := list.New(nil, d, 40, 12)
	long := strings.Repeat("server-name, ", 8)
	l.SetItems([]list.Item{
		listItem{id: "a", title: "RUNNING  demo", desc: "duplicate · proj", line3: "mcp: " + long + " · orchestrator: review-bot"},
		listItem{id: "b", title: "STOPPED  other", desc: "duplicate · lib", line3: ""},
	})
	var buf bytes.Buffer
	d.Render(&buf, l, 0, l.Items()[0])
	out := buf.String()
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("a row renders exactly three lines, got %d: %q", len(lines), out)
	}
	if !strings.Contains(lines[2], "…") || strings.Contains(lines[2], "review-bot") {
		t.Errorf("the third line should be truncated with …, never wrapped: %q", lines[2])
	}
	// A row without a third line still occupies three lines (a blank third).
	buf.Reset()
	d.Render(&buf, l, 1, l.Items()[1])
	if lines := strings.Split(buf.String(), "\n"); len(lines) != 3 || strings.TrimSpace(stripANSI(lines[2])) != "" {
		t.Errorf("blank third line expected: %q", buf.String())
	}
	// Zero width renders nothing; a non-list item renders blank lines.
	buf.Reset()
	l.SetWidth(0)
	d.Render(&buf, l, 0, l.Items()[0])
	if buf.Len() != 0 {
		t.Error("zero width renders nothing")
	}
}

// stripANSI removes escape sequences for text assertions.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// The list's rendered height with two rows is pinned: two three-line rows.
func TestListHeightWithThreeLineRows(t *testing.T) {
	d := &fakeDaemon{sandboxes: []*pb.Sandbox{
		{Id: "a", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING, McpServers: []string{"notion"}},
		{Id: "b", DisplayName: "other", State: pb.SandboxState_SANDBOX_STATE_STOPPED},
	}}
	m := withSandboxes(New(d, "/work"), d.sandboxes)
	view := stripANSI(m.list.View())
	if !strings.Contains(view, "mcp: notion") {
		t.Error("the third line should render in the list")
	}
	// The 008 badge is gone from line 2.
	if strings.Contains(view, "mcp: notion (additive)") {
		t.Error("the mcp badge belongs on line 3 only")
	}
	rows := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "demo") || strings.Contains(line, "other") {
			rows++
		}
	}
	if rows != 2 {
		t.Errorf("two rows expected, got %d", rows)
	}
}
