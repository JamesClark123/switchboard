package store

import (
	"errors"
	"sort"
	"strings"
)

// GroupMember references a sandbox by its global key (host + sandbox id). Groups
// MAY span hosts (FR-002c, FR-018).
type GroupMember struct {
	HostID    string `toml:"host_id"`
	SandboxID string `toml:"sandbox_id"`
}

// Group is a user-defined, cross-host collection of sandboxes for organization
// and navigation. Membership is independent of a sandbox's running state.
type Group struct {
	ID      string        `toml:"id"`
	Name    string        `toml:"name"`
	Members []GroupMember `toml:"members,omitempty"`
	Order   int           `toml:"order"`
}

type groupsFile struct {
	Groups []Group `toml:"groups"`
}

const groupsFileName = "groups.toml"

// GroupStore persists groups in groups.toml under the config dir.
type GroupStore struct {
	s *Store
}

// Groups returns a GroupStore backed by this Store.
func (s *Store) Groups() *GroupStore { return &GroupStore{s: s} }

// ErrGroupNotFound is returned when a group id is absent.
var ErrGroupNotFound = errors.New("group not found")

func (g *GroupStore) load() ([]Group, error) {
	var f groupsFile
	if err := g.s.LoadTOML(groupsFileName, &f); err != nil {
		return nil, err
	}
	return f.Groups, nil
}

func sortGroups(groups []Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Order != groups[j].Order {
			return groups[i].Order < groups[j].Order
		}
		return groups[i].Name < groups[j].Name
	})
}

func (g *GroupStore) write(groups []Group) error {
	sortGroups(groups)
	return g.s.SaveTOML(groupsFileName, groupsFile{Groups: groups})
}

// Save upserts a group (matched by ID; a blank ID is derived from the name).
// Duplicate names are allowed (distinguished by id, spec edge case).
func (g *GroupStore) Save(group Group) (*Group, error) {
	if strings.TrimSpace(group.Name) == "" {
		return nil, errors.New("group name is required")
	}
	if group.ID == "" {
		group.ID = slug(group.Name)
	}
	groups, err := g.load()
	if err != nil {
		return nil, err
	}
	replaced := false
	for i := range groups {
		if groups[i].ID == group.ID {
			groups[i] = group
			replaced = true
			break
		}
	}
	if !replaced {
		group.Order = len(groups)
		groups = append(groups, group)
	}
	if err := g.write(groups); err != nil {
		return nil, err
	}
	return &group, nil
}

// Get returns the group with id, or ErrGroupNotFound.
func (g *GroupStore) Get(id string) (*Group, error) {
	groups, err := g.load()
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].ID == id {
			return &groups[i], nil
		}
	}
	return nil, ErrGroupNotFound
}

// List returns all groups ordered by Order then Name.
func (g *GroupStore) List() ([]Group, error) {
	groups, err := g.load()
	if err != nil {
		return nil, err
	}
	sortGroups(groups)
	return groups, nil
}

// Delete removes a group (deleting a missing id is not an error).
func (g *GroupStore) Delete(id string) error {
	groups, err := g.load()
	if err != nil {
		return err
	}
	out := groups[:0]
	for _, grp := range groups {
		if grp.ID != id {
			out = append(out, grp)
		}
	}
	return g.write(out)
}

func hasMember(members []GroupMember, m GroupMember) bool {
	for _, x := range members {
		if x == m {
			return true
		}
	}
	return false
}

// AddMember adds a sandbox to a group (idempotent).
func (g *GroupStore) AddMember(groupID string, m GroupMember) error {
	groups, err := g.load()
	if err != nil {
		return err
	}
	for i := range groups {
		if groups[i].ID == groupID {
			if !hasMember(groups[i].Members, m) {
				groups[i].Members = append(groups[i].Members, m)
			}
			return g.write(groups)
		}
	}
	return ErrGroupNotFound
}

// RemoveMember removes a sandbox from a group without affecting the sandbox
// (FR: removing from a group does not stop/destroy the sandbox).
func (g *GroupStore) RemoveMember(groupID string, m GroupMember) error {
	groups, err := g.load()
	if err != nil {
		return err
	}
	for i := range groups {
		if groups[i].ID == groupID {
			out := groups[i].Members[:0]
			for _, x := range groups[i].Members {
				if x != m {
					out = append(out, x)
				}
			}
			groups[i].Members = out
			return g.write(groups)
		}
	}
	return ErrGroupNotFound
}

// Prune removes members for which valid returns false (e.g. a destroyed
// sandbox), keeping group membership consistent on sync.
func (g *GroupStore) Prune(valid func(GroupMember) bool) (int, error) {
	groups, err := g.load()
	if err != nil {
		return 0, err
	}
	pruned := 0
	for i := range groups {
		out := groups[i].Members[:0]
		for _, m := range groups[i].Members {
			if valid(m) {
				out = append(out, m)
			} else {
				pruned++
			}
		}
		groups[i].Members = out
	}
	if pruned > 0 {
		if err := g.write(groups); err != nil {
			return 0, err
		}
	}
	return pruned, nil
}
