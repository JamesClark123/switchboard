package mcp

import (
	"reflect"
	"strings"
	"testing"
)

// The exact diagnostic from SWI-2.
const collisionFixture = `tool "list_issues" from server "linear" collides with an existing tool already registered for server "github"
tool "list_releases" from server "linear" collides with an existing tool already registered for server "github"`

func TestLooksLikeToolCollision(t *testing.T) {
	if !LooksLikeToolCollision(collisionFixture) {
		t.Error("fixture should be recognised")
	}
	for _, no := range []string{"", `MCP server "ghost" is not registered`, "connection refused"} {
		if LooksLikeToolCollision(no) {
			t.Errorf("%q is not a collision", no)
		}
	}
}

func TestParseToolCollisions(t *testing.T) {
	got := ParseToolCollisions(collisionFixture + "\n" + collisionFixture)
	want := []ToolCollision{
		{Tool: "list_issues", Server: "linear", Other: "github"},
		{Tool: "list_releases", Server: "linear", Other: "github"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if ParseToolCollisions("nothing here") != nil {
		t.Error("no triples expected")
	}
}

func TestCollisionHint(t *testing.T) {
	h := CollisionHint("error: " + collisionFixture)
	for _, want := range []string{"collision with github (list_issues, list_releases)", "filter tools on one server via its headers", "README › MCP tool collisions"} {
		if !strings.Contains(h, want) {
			t.Errorf("hint %q missing %q", h, want)
		}
	}
	if CollisionHint("something else") != "" {
		t.Error("non-collision must give no hint")
	}
	// Reworded runtime text still gets a generic hint.
	if g := CollisionHint("tool x collides with an existing tool"); !strings.Contains(g, "tool name collision between MCP servers") {
		t.Errorf("generic hint = %q", g)
	}
	// Several clashing servers are all named.
	multi := `tool "a" from server "n" collides with an existing tool already registered for server "x"` + "\n" +
		`tool "b" from server "n" collides with an existing tool already registered for server "y"`
	if h := CollisionHint(multi); !strings.Contains(h, "x (a); y (b)") {
		t.Errorf("multi hint = %q", h)
	}
}
