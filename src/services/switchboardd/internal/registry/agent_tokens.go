package registry

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// agentTokensBucket holds the per-agent hook tokens (SWI-17, research R13):
//   - "<sandbox id>/_gen" = the sandbox's agent generation counter (8 bytes,
//     big endian). It only grows, across daemon restarts too, so a generation
//     spawned after a restart is always greater than any recorded hold_gen;
//   - "<sandbox id>/<generation %020d>" = that generation's AgentToken (JSON).
//
// Kept OUT of the Sandbox record, like the worker tokens, so a token never
// serialises into anything that travels on the wire (ListSandboxes).
var agentTokensBucket = []byte("agent_tokens")

const agentGenSuffix = "_gen"

// ErrAgentTokenNotFound is returned when a sandbox has no token for a generation.
var ErrAgentTokenNotFound = errors.New("agent token not found")

// AgentToken is one agent generation's hook token and the main agent process
// bound to it (research R13): the PID of the `claude` process the generation's
// first SessionStart came from, and, when a probe bound it, that process's
// start time (/proc/<pid>/stat field 22).
type AgentToken struct {
	Gen       int64  `json:"-"`
	Token     string `json:"token"`
	MainPID   int    `json:"main_pid,omitempty"`
	MainStart string `json:"main_start,omitempty"`
}

// AgentGens is a sandbox's generation counter and its unretired tokens, in
// generation order.
type AgentGens struct {
	Counter int64
	Tokens  []AgentToken
}

func agentGenKey(sandboxID string) []byte { return []byte(sandboxID + "/" + agentGenSuffix) }

func agentTokenKey(sandboxID string, gen int64) []byte {
	return []byte(fmt.Sprintf("%s/%020d", sandboxID, gen))
}

// PutAgentToken stores a generation's token and raises the sandbox's counter to
// at least its generation, in one transaction.
func (r *Registry) PutAgentToken(sandboxID string, t AgentToken) error {
	if sandboxID == "" || strings.Contains(sandboxID, "/") || t.Gen <= 0 || t.Token == "" {
		return errors.New("sandbox id, generation and token are required")
	}
	blob, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(agentTokensBucket)
		if cur := genOf(b.Get(agentGenKey(sandboxID))); cur < t.Gen {
			buf := make([]byte, 8)
			binary.BigEndian.PutUint64(buf, uint64(t.Gen))
			if err := b.Put(agentGenKey(sandboxID), buf); err != nil {
				return err
			}
		}
		return b.Put(agentTokenKey(sandboxID, t.Gen), blob)
	})
}

// SetAgentMain records the main agent process of a stored generation. A
// retired generation (no token) is not brought back: ErrAgentTokenNotFound.
func (r *Registry) SetAgentMain(sandboxID string, gen int64, pid int, start string) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(agentTokensBucket)
		key := agentTokenKey(sandboxID, gen)
		v := b.Get(key)
		if v == nil {
			return ErrAgentTokenNotFound
		}
		var t AgentToken
		if err := json.Unmarshal(v, &t); err != nil {
			return err
		}
		t.MainPID, t.MainStart = pid, start
		blob, err := json.Marshal(t)
		if err != nil {
			return err
		}
		return b.Put(key, blob)
	})
}

// DeleteAgentToken retires one generation's token. A missing one is not an
// error. The counter stays.
func (r *Registry) DeleteAgentToken(sandboxID string, gen int64) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(agentTokensBucket).Delete(agentTokenKey(sandboxID, gen))
	})
}

// DeleteAgentTokens retires every token of a sandbox. With purge the counter
// goes too (the sandbox was destroyed); otherwise it stays, so generations
// keep growing.
func (r *Registry) DeleteAgentTokens(sandboxID string, purge bool) error {
	prefix := []byte(sandboxID + "/")
	return r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(agentTokensBucket)
		var keys [][]byte
		c := b.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			if !purge && bytes.Equal(k, agentGenKey(sandboxID)) {
				continue
			}
			keys = append(keys, append([]byte(nil), k...))
		}
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListAgentTokens returns every sandbox's counter and unretired tokens (the
// hook server's start-up index; no store read per hook).
func (r *Registry) ListAgentTokens() (map[string]*AgentGens, error) {
	out := map[string]*AgentGens{}
	of := func(id string) *AgentGens {
		g := out[id]
		if g == nil {
			g = &AgentGens{}
			out[id] = g
		}
		return g
	}
	err := r.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(agentTokensBucket).ForEach(func(k, v []byte) error {
			id, rest, ok := strings.Cut(string(k), "/")
			if !ok {
				return nil
			}
			if rest == agentGenSuffix {
				of(id).Counter = genOf(v)
				return nil
			}
			gen, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				return nil // not a generation key
			}
			var t AgentToken
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			t.Gen = gen
			of(id).Tokens = append(of(id).Tokens, t)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// genOf decodes a stored counter (0 when absent or malformed).
func genOf(v []byte) int64 {
	if len(v) != 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(v))
}
