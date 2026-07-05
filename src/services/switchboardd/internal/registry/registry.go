// Package registry is the daemon's durable sandbox store (FR-002a/b). It persists
// each sandbox (as the proto Sandbox message) in an embedded bbolt KV file so the
// registry survives daemon restarts and powers re-adoption (see readopt.go).
package registry

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

var sandboxBucket = []byte("sandboxes")

// ErrNotFound is returned when a sandbox id is absent from the registry.
var ErrNotFound = errors.New("sandbox not found")

// Registry is a bbolt-backed store of sandboxes.
type Registry struct {
	db *bolt.DB
}

// Open creates/opens the registry at <dataDir>/registry.db and ensures buckets.
func Open(dataDir string) (*Registry, error) {
	path := filepath.Join(dataDir, "registry.db")
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open registry %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, e := tx.CreateBucketIfNotExists(sandboxBucket)
		return e
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Registry{db: db}, nil
}

// Close releases the underlying database file.
func (r *Registry) Close() error { return r.db.Close() }

// Put inserts or replaces a sandbox record.
func (r *Registry) Put(s *pb.Sandbox) error {
	if s.GetId() == "" {
		return errors.New("sandbox id is required")
	}
	blob, err := proto.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal sandbox: %w", err)
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(sandboxBucket).Put([]byte(s.GetId()), blob)
	})
}

// Get returns the sandbox with id, or ErrNotFound.
func (r *Registry) Get(id string) (*pb.Sandbox, error) {
	var out *pb.Sandbox
	err := r.db.View(func(tx *bolt.Tx) error {
		blob := tx.Bucket(sandboxBucket).Get([]byte(id))
		if blob == nil {
			return ErrNotFound
		}
		s := &pb.Sandbox{}
		if e := proto.Unmarshal(blob, s); e != nil {
			return e
		}
		out = s
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes a sandbox record. Deleting a missing id is not an error.
func (r *Registry) Delete(id string) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(sandboxBucket).Delete([]byte(id))
	})
}

// List returns every sandbox, ordered by creation time then id for stability.
func (r *Registry) List() ([]*pb.Sandbox, error) {
	var out []*pb.Sandbox
	err := r.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(sandboxBucket).ForEach(func(_, blob []byte) error {
			s := &pb.Sandbox{}
			if e := proto.Unmarshal(blob, s); e != nil {
				return e
			}
			out = append(out, s)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].GetCreatedAt().AsTime(), out[j].GetCreatedAt().AsTime()
		if ti.Equal(tj) {
			return out[i].GetId() < out[j].GetId()
		}
		return ti.Before(tj)
	})
	return out, nil
}

// Update applies mutate to the stored sandbox and writes it back atomically.
func (r *Registry) Update(id string, mutate func(*pb.Sandbox) error) (*pb.Sandbox, error) {
	var out *pb.Sandbox
	err := r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(sandboxBucket)
		blob := b.Get([]byte(id))
		if blob == nil {
			return ErrNotFound
		}
		s := &pb.Sandbox{}
		if e := proto.Unmarshal(blob, s); e != nil {
			return e
		}
		if e := mutate(s); e != nil {
			return e
		}
		nb, e := proto.Marshal(s)
		if e != nil {
			return e
		}
		out = s
		return b.Put([]byte(id), nb)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
