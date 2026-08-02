package portforward

import (
	"fmt"
	"path/filepath"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// PortPlaceholder is the token an author writes in an on-host command to ask for a
// freshly allocated host port instead of the declared one (research R4). Its
// presence is what lets two sandboxes run the same on-host service concurrently;
// its absence is what makes a second instance fail with PORT_IN_USE. Either way
// the behaviour is the author's explicit, visible choice.
const PortPlaceholder = "{{port}}"

// ServicesFromRefs extracts each kit's service list from a slice of KitRefs, in
// attach order. Only client-authored kits (KitRef.spec) can declare services;
// external `source` kits are opaque to switchboard and contribute none.
func ServicesFromRefs(refs []*pb.KitRef) [][]*pb.KitService {
	lists := make([][]*pb.KitService, 0, len(refs))
	for _, ref := range refs {
		if spec := ref.GetSpec(); spec != nil {
			lists = append(lists, spec.GetServices())
		}
	}
	return lists
}

// Resolve merges the service lists of a sandbox's attached kits, in attach order,
// into the single set the daemon persists and enforces (FR-044).
//
// On a name collision the later list wins (the more-recently-attached kit's
// service overrides the earlier one), matching escape-hatch command resolution.
// First-appearance order is preserved so the client's service list stays stable
// across re-attaches — an overriding service keeps the earlier one's position but
// takes the later definition's value.
func Resolve(lists ...[]*pb.KitService) ([]*pb.KitService, error) {
	order := make([]string, 0)
	byName := make(map[string]*pb.KitService)
	for _, list := range lists {
		for _, svc := range list {
			if svc == nil {
				continue
			}
			name := svc.GetName()
			if _, seen := byName[name]; !seen {
				order = append(order, name)
			}
			byName[name] = svc // later wins
		}
	}

	out := make([]*pb.KitService, 0, len(order))
	for _, name := range order {
		svc := byName[name]
		if err := validate(svc); err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, nil
}

// validate enforces the daemon-side invariants a resolved service must satisfy.
// Richer, developer-facing checks (kebab-case, duplicate detection within one kit)
// live client-side in the editor; these are the minimum the daemon refuses to
// attach, re-checked here so a hand-edited sidecar cannot bypass the editor.
func validate(svc *pb.KitService) error {
	if svc.GetName() == "" {
		return fmt.Errorf("service has an empty name")
	}
	if strings.TrimSpace(svc.GetCommand()) == "" {
		return fmt.Errorf("service %q has an empty command", svc.GetName())
	}
	if p := svc.GetListenPort(); p == 0 || p > 65535 {
		return fmt.Errorf("service %q has listen_port %d, which is outside 1-65535", svc.GetName(), p)
	}
	if svc.GetLocation() == pb.ServiceLocation_SERVICE_LOCATION_UNSPECIFIED {
		return fmt.Errorf("service %q must set a location (in-sandbox or on-host)", svc.GetName())
	}
	if dir := svc.GetWorkingDir(); dir != "" && !workspaceRelOK(dir) {
		return fmt.Errorf("service %q has a working_dir %q that is absolute or escapes the workspace", svc.GetName(), dir)
	}
	// {{port}} exists to dodge host-port collisions between sandboxes. An in-sandbox
	// service has its own network namespace, so it cannot collide — and substituting
	// a port there would break the publish mapping, since the daemon publishes the
	// DECLARED port. Reject it rather than silently ignoring it.
	if svc.GetLocation() == pb.ServiceLocation_SERVICE_LOCATION_IN_SANDBOX &&
		strings.Contains(svc.GetCommand(), PortPlaceholder) {
		return fmt.Errorf("service %q uses %s but runs in the sandbox, where its port cannot collide", svc.GetName(), PortPlaceholder)
	}
	return nil
}

// workspaceRelOK reports whether dir is a workspace-relative path that stays
// inside the workspace. Mirrors escapehatch.resolveWorkdir's containment rule, but
// as a pure path check: it runs at attach time, when the workspace may not exist
// on this host yet.
func workspaceRelOK(dir string) bool {
	if filepath.IsAbs(dir) {
		return false
	}
	cleaned := filepath.Clean(dir)
	return cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

// Lookup returns the sandbox's declared service by name, or (nil, false). This is
// the security boundary: only a name already on the sandbox's persisted service
// set can resolve to something startable (FR-045).
func Lookup(sb *pb.Sandbox, name string) (*pb.KitService, bool) {
	for _, svc := range sb.GetServices() {
		if svc.GetName() == name {
			return svc, true
		}
	}
	return nil, false
}
