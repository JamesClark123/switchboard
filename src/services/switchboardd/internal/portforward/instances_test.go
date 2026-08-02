package portforward

import (
	"sync"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestInstanceStoreAssignsSequentialIDs(t *testing.T) {
	s := NewInstanceStore()
	a, _ := s.Create("sb1", "web")
	b, _ := s.Create("sb1", "api")
	if a.GetId() != "svc-1" || b.GetId() != "svc-2" {
		t.Errorf("ids = %q, %q; want svc-1, svc-2", a.GetId(), b.GetId())
	}
	if a.GetState() != pb.ServiceState_SERVICE_STATE_STARTING {
		t.Errorf("a new instance starts in STARTING, got %v", a.GetState())
	}
}

func TestInstanceStoreCreateIsIdempotentWhileNonTerminal(t *testing.T) {
	s := NewInstanceStore()
	first, created := s.Create("sb1", "web")
	if !created {
		t.Fatal("first Create must report created")
	}

	second, created := s.Create("sb1", "web")
	if created {
		t.Error("starting an already-STARTING service must NOT create a second instance")
	}
	if second.GetId() != first.GetId() {
		t.Errorf("idempotent start must return the existing instance, got %q want %q", second.GetId(), first.GetId())
	}

	// Still idempotent once RUNNING — that is the case FR-048 actually cares about.
	s.Update(first.GetId(), func(i *pb.ServiceInstance) {
		i.State = pb.ServiceState_SERVICE_STATE_RUNNING
		i.LocalPort = 49221
	})
	third, created := s.Create("sb1", "web")
	if created || third.GetId() != first.GetId() || third.GetLocalPort() != 49221 {
		t.Errorf("a RUNNING service must keep its instance and local address, got created=%v id=%q port=%d",
			created, third.GetId(), third.GetLocalPort())
	}
}

func TestInstanceStoreCreateAfterTerminalMakesFreshInstance(t *testing.T) {
	s := NewInstanceStore()
	first, _ := s.Create("sb1", "web")
	s.Update(first.GetId(), func(i *pb.ServiceInstance) { i.State = pb.ServiceState_SERVICE_STATE_FAILED })

	second, created := s.Create("sb1", "web")
	if !created {
		t.Fatal("a terminal instance must be replaceable by a new attempt")
	}
	if second.GetId() == first.GetId() {
		t.Error("the new attempt must get a fresh id")
	}
	if _, ok := s.Get(first.GetId()); ok {
		t.Error("the replaced instance must no longer be addressable by id")
	}
}

func TestInstanceStoreConcurrentCreateYieldsExactlyOne(t *testing.T) {
	s := NewInstanceStore()
	const n = 50

	var wg sync.WaitGroup
	var mu sync.Mutex
	createdCount := 0
	ids := map[string]struct{}{}

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			inst, created := s.Create("sb1", "web")
			mu.Lock()
			defer mu.Unlock()
			if created {
				createdCount++
			}
			ids[inst.GetId()] = struct{}{}
		}()
	}
	wg.Wait()

	if createdCount != 1 {
		t.Errorf("exactly one concurrent Create may win, got %d", createdCount)
	}
	if len(ids) != 1 {
		t.Errorf("all callers must see the same instance, got %d distinct ids", len(ids))
	}
}

func TestInstanceStoreListFiltersBySandbox(t *testing.T) {
	s := NewInstanceStore()
	s.Create("sb1", "web")
	s.Create("sb2", "web")
	s.Create("sb1", "api")

	if got := len(s.ListBySandbox("sb1")); got != 2 {
		t.Errorf("sb1 list = %d, want 2", got)
	}
	if got := len(s.ListBySandbox("sb2")); got != 1 {
		t.Errorf("sb2 list = %d, want 1", got)
	}
	if got := len(s.ListBySandbox("")); got != 3 {
		t.Errorf("empty sandbox id must list all, got %d", got)
	}
}

func TestInstanceStoreListIsFirstStartOrder(t *testing.T) {
	s := NewInstanceStore()
	s.Create("sb1", "api")
	web, _ := s.Create("sb1", "web")
	// Restart "api" — its position must not move.
	s.Update(web.GetId(), func(i *pb.ServiceInstance) { i.State = pb.ServiceState_SERVICE_STATE_STOPPED })
	s.Create("sb1", "web")

	list := s.ListBySandbox("sb1")
	if list[0].GetServiceName() != "api" || list[1].GetServiceName() != "web" {
		t.Errorf("order = %q,%q; want api,web", list[0].GetServiceName(), list[1].GetServiceName())
	}
}

func TestInstanceStoreActiveExcludesTerminal(t *testing.T) {
	s := NewInstanceStore()
	a, _ := s.Create("sb1", "web")
	s.Create("sb1", "api")
	s.Update(a.GetId(), func(i *pb.ServiceInstance) { i.State = pb.ServiceState_SERVICE_STATE_STOPPED })

	active := s.ActiveBySandbox("sb1")
	if len(active) != 1 || active[0].GetServiceName() != "api" {
		t.Errorf("active = %v, want only api", active)
	}
}

func TestInstanceStoreClonesOnTheWayOut(t *testing.T) {
	s := NewInstanceStore()
	inst, _ := s.Create("sb1", "web")
	inst.LocalPort = 1234 // mutate the caller's copy

	stored, _ := s.Get(inst.GetId())
	if stored.GetLocalPort() != 0 {
		t.Error("mutating a returned instance must not affect the stored record")
	}
}

func TestInstanceStoreGetByServiceAndUnknownIDs(t *testing.T) {
	s := NewInstanceStore()
	s.Create("sb1", "web")

	if _, ok := s.GetByService("sb1", "web"); !ok {
		t.Error("GetByService must find the current instance")
	}
	if _, ok := s.GetByService("sb1", "nope"); ok {
		t.Error("GetByService must miss an unknown service")
	}
	if _, ok := s.Get("svc-999"); ok {
		t.Error("Get must miss an unknown id")
	}
	if _, ok := s.Update("svc-999", func(*pb.ServiceInstance) {}); ok {
		t.Error("Update must miss an unknown id")
	}
}
