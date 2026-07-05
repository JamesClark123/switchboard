package client_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
)

func TestManagerConnectDisconnectAndState(t *testing.T) {
	// Two hosts dialed via an in-process bridge to real fake servers.
	sockA := startFakeOnSocket(t)
	sockB := startFakeOnSocket(t)
	ctx := context.Background()

	socks := map[string]string{"a": sockA, "b": sockB}
	mgr := client.NewManager()
	mgr.SetDialFunc(func(ctx context.Context, e client.HostEntry) (*client.Conn, error) {
		return client.DialCommand(ctx, bridgeCmd(ctx, socks[e.ID]))
	})

	mgr.Upsert(client.HostEntry{ID: "a", DisplayName: "alpha", Kind: "ssh", SSHTarget: "a"})
	mgr.Upsert(client.HostEntry{ID: "b", DisplayName: "beta", Kind: "ssh", SSHTarget: "b"})
	mgr.Upsert(client.HostEntry{ID: "a", DisplayName: "alpha2", Kind: "ssh", SSHTarget: "a"}) // upsert updates in place

	if list := mgr.List(); len(list) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(list))
	}

	if err := mgr.Connect(ctx, "a"); err != nil {
		t.Fatalf("connect a: %v", err)
	}
	if err := mgr.Connect(ctx, "b"); err != nil {
		t.Fatalf("connect b: %v", err)
	}

	hc, ok := mgr.Get("a")
	if !ok || hc.State != client.HostConnected || hc.Conn == nil {
		t.Fatalf("host a not connected: %+v", hc)
	}
	if hc.State.String() != "connected" {
		t.Errorf("state string = %q", hc.State.String())
	}

	// Aggregate across both connected hosts (sorted by display name).
	agg := mgr.AggregateSandboxes(ctx)
	if len(agg) != 2 {
		t.Fatalf("aggregate len = %d", len(agg))
	}
	if agg[0].Host.Entry.DisplayName != "alpha2" || agg[1].Host.Entry.DisplayName != "beta" {
		t.Errorf("aggregate order: %s,%s", agg[0].Host.Entry.DisplayName, agg[1].Host.Entry.DisplayName)
	}

	// Disconnect drops the link; re-listing shows disconnected.
	mgr.Disconnect("a")
	hc, _ = mgr.Get("a")
	if hc.State != client.HostDisconnected || hc.Conn != nil {
		t.Errorf("host a should be disconnected: %+v", hc)
	}

	// Reconnect resyncs (SC-010): connect again succeeds.
	if err := mgr.Connect(ctx, "a"); err != nil {
		t.Fatalf("reconnect a: %v", err)
	}

	// Remove drops the host entirely.
	mgr.Remove("b")
	if _, ok := mgr.Get("b"); ok {
		t.Error("host b should be removed")
	}
	if len(mgr.List()) != 1 {
		t.Errorf("expected 1 host after remove, got %d", len(mgr.List()))
	}
}

func TestManagerAdopt(t *testing.T) {
	sock := startFakeOnSocket(t)
	ctx := context.Background()
	conn, err := client.DialLocal(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	mgr := client.NewManager()
	// Adopt onto a brand-new id (creates the host entry).
	mgr.Adopt("local", conn)
	hc, ok := mgr.Get("local")
	if !ok || hc.State != client.HostConnected || hc.Conn == nil {
		t.Fatalf("adopted host not connected: %+v", hc)
	}
	if len(mgr.List()) != 1 {
		t.Errorf("expected 1 host after adopt, got %d", len(mgr.List()))
	}
	// Adopt again onto the existing id updates in place.
	mgr.Adopt("local", conn)
	if len(mgr.List()) != 1 {
		t.Errorf("re-adopt should not add a duplicate, got %d", len(mgr.List()))
	}
}

func TestManagerConnectErrors(t *testing.T) {
	mgr := client.NewManager()
	if err := mgr.Connect(context.Background(), "ghost"); !errors.Is(err, client.ErrUnknownHost) {
		t.Errorf("connect unknown = %v, want ErrUnknownHost", err)
	}

	mgr.Upsert(client.HostEntry{ID: "bad", DisplayName: "bad", Kind: "ssh", SSHTarget: "x"})
	mgr.SetDialFunc(func(context.Context, client.HostEntry) (*client.Conn, error) {
		return nil, errors.New("dial boom")
	})
	if err := mgr.Connect(context.Background(), "bad"); err == nil {
		t.Error("expected connect error")
	}
	hc, _ := mgr.Get("bad")
	if hc.State != client.HostDisconnected || hc.Err == nil {
		t.Errorf("failed host should record error + disconnected: %+v", hc)
	}

	// Disconnecting / removing unknown hosts is a no-op.
	mgr.Disconnect("ghost")
	mgr.Remove("ghost")

	// AggregateSandboxes includes disconnected hosts with empty lists.
	agg := mgr.AggregateSandboxes(context.Background())
	if len(agg) != 1 || len(agg[0].Sandboxes) != 0 {
		t.Errorf("disconnected host should aggregate empty: %+v", agg)
	}
}
