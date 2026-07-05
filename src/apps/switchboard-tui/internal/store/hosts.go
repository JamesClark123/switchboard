package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// KnownHost is a saved connection to a daemon (FR-002d, FR-004). The client
// persists these so a user need not re-enter connection details each session.
// No secrets are stored — SSH auth is delegated to the user's ssh agent/config.
type KnownHost struct {
	ID          string   `toml:"id"`
	DisplayName string   `toml:"display_name"`
	Kind        string   `toml:"kind"` // "local" => Unix socket; "ssh" => dial-stdio
	SocketPath  string   `toml:"socket_path,omitempty"`
	SSHTarget   string   `toml:"ssh_target,omitempty"`
	SSHOptions  []string `toml:"ssh_options,omitempty"`
	// LastConnectedUnix is the last successful connect time as Unix seconds (0 if
	// never). Stored as an int to sidestep go-toml's *time.Time round-trip quirk.
	LastConnectedUnix int64 `toml:"last_connected_unix,omitempty"`
}

// LastConnected returns the last-connected time, or the zero time if never.
func (h *KnownHost) LastConnected() time.Time {
	if h.LastConnectedUnix == 0 {
		return time.Time{}
	}
	return time.Unix(h.LastConnectedUnix, 0)
}

// validate enforces the kind/target invariants (data-model: exactly one of
// socket_path / ssh_target set per kind).
func (h *KnownHost) validate() error {
	switch h.Kind {
	case "local":
		if h.SocketPath == "" {
			return errors.New("local host requires socket_path")
		}
		if h.SSHTarget != "" {
			return errors.New("local host must not set ssh_target")
		}
	case "ssh":
		if h.SSHTarget == "" {
			return errors.New("ssh host requires ssh_target")
		}
		if h.SocketPath != "" {
			return errors.New("ssh host must not set socket_path")
		}
	default:
		return fmt.Errorf("invalid host kind %q (want local|ssh)", h.Kind)
	}
	return nil
}

type hostsFile struct {
	Hosts []KnownHost `toml:"hosts"`
}

const hostsFileName = "hosts.toml"

// HostStore persists known hosts in hosts.toml under the config dir.
type HostStore struct {
	s *Store
}

// Hosts returns a HostStore backed by this Store.
func (s *Store) Hosts() *HostStore { return &HostStore{s: s} }

func (h *HostStore) load() ([]KnownHost, error) {
	var f hostsFile
	if err := h.s.LoadTOML(hostsFileName, &f); err != nil {
		return nil, err
	}
	return f.Hosts, nil
}

func (h *HostStore) write(hosts []KnownHost) error {
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].DisplayName < hosts[j].DisplayName })
	return h.s.SaveTOML(hostsFileName, hostsFile{Hosts: hosts})
}

// Save upserts a host (matched by ID). A blank ID is derived from the display
// name or connection target. The host is validated before persisting.
func (h *HostStore) Save(host KnownHost) (*KnownHost, error) {
	if host.ID == "" {
		seed := host.DisplayName
		if seed == "" {
			if host.Kind == "ssh" {
				seed = host.SSHTarget
			} else {
				seed = "local"
			}
		}
		host.ID = slug(seed)
	}
	if host.DisplayName == "" {
		host.DisplayName = host.ID
	}
	if err := host.validate(); err != nil {
		return nil, err
	}
	hosts, err := h.load()
	if err != nil {
		return nil, err
	}
	replaced := false
	for i := range hosts {
		if hosts[i].ID == host.ID {
			hosts[i] = host
			replaced = true
			break
		}
	}
	if !replaced {
		hosts = append(hosts, host)
	}
	if err := h.write(hosts); err != nil {
		return nil, err
	}
	return &host, nil
}

// Get returns the host with id, or ErrHostNotFound.
func (h *HostStore) Get(id string) (*KnownHost, error) {
	hosts, err := h.load()
	if err != nil {
		return nil, err
	}
	for i := range hosts {
		if hosts[i].ID == id {
			return &hosts[i], nil
		}
	}
	return nil, ErrHostNotFound
}

// List returns all known hosts ordered by display name.
func (h *HostStore) List() ([]KnownHost, error) {
	hosts, err := h.load()
	if err != nil {
		return nil, err
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].DisplayName < hosts[j].DisplayName })
	return hosts, nil
}

// Delete removes a host by id (deleting a missing id is not an error).
func (h *HostStore) Delete(id string) error {
	hosts, err := h.load()
	if err != nil {
		return err
	}
	out := hosts[:0]
	for _, host := range hosts {
		if host.ID != id {
			out = append(out, host)
		}
	}
	return h.write(out)
}

// Touch records the last-connected time for ordering/recents.
func (h *HostStore) Touch(id string, at time.Time) error {
	hosts, err := h.load()
	if err != nil {
		return err
	}
	for i := range hosts {
		if hosts[i].ID == id {
			hosts[i].LastConnectedUnix = at.Unix()
			return h.write(hosts)
		}
	}
	return ErrHostNotFound
}

// EnsureLocal makes sure a "local" host pointing at socketPath exists, adding it
// on first run so the TUI always has at least the local daemon available.
func (h *HostStore) EnsureLocal(socketPath string) (*KnownHost, error) {
	hosts, err := h.load()
	if err != nil {
		return nil, err
	}
	for i := range hosts {
		if hosts[i].Kind == "local" {
			return &hosts[i], nil
		}
	}
	return h.Save(KnownHost{ID: "local", DisplayName: "localhost", Kind: "local", SocketPath: socketPath})
}

// ErrHostNotFound is returned when a host id is absent.
var ErrHostNotFound = errors.New("host not found")

// Summary renders a one-line connection descriptor for display.
func (h *KnownHost) Summary() string {
	if h.Kind == "ssh" {
		t := h.SSHTarget
		if len(h.SSHOptions) > 0 {
			t += " " + strings.Join(h.SSHOptions, " ")
		}
		return "ssh " + t
	}
	return "local " + h.SocketPath
}
