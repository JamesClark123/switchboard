package registry

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

// Feature 009 buckets (research R9, data-model "bbolt layout"):
//   - orchestratorsBucket: key = orchestrator id, value = proto Orchestrator.
//   - runsBucket: key = "<orchestrator id>/<seq %020d>", value = proto Run, so a
//     prefix scan yields runs in order of receipt; the per-orchestrator
//     sequence counter lives under "<orchestrator id>/_seq" in the same bucket.
//   - workerTokensBucket: key = sandbox id, value = the per-worker gateway
//     token. Kept OUT of the Sandbox record so the token never serializes into
//     anything that travels on the wire (I-C4).
var (
	orchestratorsBucket = []byte("orchestrators")
	runsBucket          = []byte("orchestrator_runs")
	workerTokensBucket  = []byte("worker_tokens")
)

// ErrOrchestratorNotFound / ErrRunNotFound / ErrTokenNotFound are the absent-key
// sentinels of the feature-009 buckets.
var (
	ErrOrchestratorNotFound = errors.New("orchestrator not found")
	ErrRunNotFound          = errors.New("run not found")
	ErrTokenNotFound        = errors.New("worker token not found")
)

const runSeqSuffix = "_seq"

// runKey renders the receipt-ordered key for a run.
func runKey(orchID string, seq uint64) []byte {
	return []byte(fmt.Sprintf("%s/%020d", orchID, seq))
}

// RunSeq extracts the receipt sequence from a run id of the form
// "run-<orchestrator8>-<seq>" (data-model "Run"), or 0 when it has none.
func RunSeq(runID string) uint64 {
	i := strings.LastIndexByte(runID, '-')
	if i < 0 {
		return 0
	}
	n, err := strconv.ParseUint(runID[i+1:], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// --- orchestrators ---

// PutOrchestrator inserts or replaces an orchestrator record.
func (r *Registry) PutOrchestrator(o *pb.Orchestrator) error {
	if o.GetId() == "" {
		return errors.New("orchestrator id is required")
	}
	blob, err := proto.Marshal(o)
	if err != nil {
		return fmt.Errorf("marshal orchestrator: %w", err)
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(orchestratorsBucket).Put([]byte(o.GetId()), blob)
	})
}

// GetOrchestrator returns the record with id, or ErrOrchestratorNotFound.
func (r *Registry) GetOrchestrator(id string) (*pb.Orchestrator, error) {
	var out *pb.Orchestrator
	err := r.db.View(func(tx *bolt.Tx) error {
		blob := tx.Bucket(orchestratorsBucket).Get([]byte(id))
		if blob == nil {
			return ErrOrchestratorNotFound
		}
		o := &pb.Orchestrator{}
		if e := proto.Unmarshal(blob, o); e != nil {
			return e
		}
		out = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListOrchestrators returns every record, ordered by creation time then id.
func (r *Registry) ListOrchestrators() ([]*pb.Orchestrator, error) {
	var out []*pb.Orchestrator
	err := r.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(orchestratorsBucket).ForEach(func(_, blob []byte) error {
			o := &pb.Orchestrator{}
			if e := proto.Unmarshal(blob, o); e != nil {
				return e
			}
			out = append(out, o)
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

// UpdateOrchestrator applies mutate to the stored record and writes it back in
// the same transaction.
func (r *Registry) UpdateOrchestrator(id string, mutate func(*pb.Orchestrator) error) (*pb.Orchestrator, error) {
	var out *pb.Orchestrator
	err := r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(orchestratorsBucket)
		blob := b.Get([]byte(id))
		if blob == nil {
			return ErrOrchestratorNotFound
		}
		o := &pb.Orchestrator{}
		if e := proto.Unmarshal(blob, o); e != nil {
			return e
		}
		if e := mutate(o); e != nil {
			return e
		}
		nb, e := proto.Marshal(o)
		if e != nil {
			return e
		}
		out = o
		return b.Put([]byte(id), nb)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RunTx is the run bucket of one orchestrator inside an UpdateOrchestratorRuns
// transaction (SWI-18 research R7).
type RunTx interface {
	// NextSeq increments and returns the receipt sequence, as NextRunSeq.
	NextSeq() (uint64, error)
	// Put inserts or replaces a run of this orchestrator.
	Put(*pb.Run) error
	// Get returns one run, or ErrRunNotFound.
	Get(runID string) (*pb.Run, error)
	// Queued returns the QUEUED runs in receipt order.
	Queued() ([]*pb.Run, error)
}

type runTx struct {
	b      *bolt.Bucket
	orchID string
}

func (t runTx) NextSeq() (uint64, error) { return nextSeq(t.b, t.orchID) }

func (t runTx) Put(run *pb.Run) error {
	if run.GetOrchestratorId() != t.orchID {
		return errors.New("run belongs to another orchestrator")
	}
	return putRun(t.b, run)
}

func (t runTx) Get(runID string) (*pb.Run, error) { return getRun(t.b, t.orchID, runID) }

func (t runTx) Queued() ([]*pb.Run, error) { return queuedRuns(t.b, t.orchID) }

// UpdateOrchestratorRuns runs fn with the record and its run bucket in one bolt
// write transaction, then writes the record back (SWI-18 research R7). An error
// from fn writes nothing to either bucket. fn must not call the registry: bolt's
// writer lock is not re-entrant.
func (r *Registry) UpdateOrchestratorRuns(id string, fn func(rec *pb.Orchestrator, runs RunTx) error) (*pb.Orchestrator, error) {
	var out *pb.Orchestrator
	err := r.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(orchestratorsBucket)
		blob := b.Get([]byte(id))
		if blob == nil {
			return ErrOrchestratorNotFound
		}
		o := &pb.Orchestrator{}
		if e := proto.Unmarshal(blob, o); e != nil {
			return e
		}
		if e := fn(o, runTx{b: tx.Bucket(runsBucket), orchID: id}); e != nil {
			return e
		}
		nb, e := proto.Marshal(o)
		if e != nil {
			return e
		}
		out = o
		return b.Put([]byte(id), nb)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ViewOrchestratorRuns hands fn the record and its QUEUED runs (receipt order)
// read in one bolt View, so both come from the same commit (SWI-18 research R7).
func (r *Registry) ViewOrchestratorRuns(id string, fn func(rec *pb.Orchestrator, queued []*pb.Run) error) error {
	return r.db.View(func(tx *bolt.Tx) error {
		blob := tx.Bucket(orchestratorsBucket).Get([]byte(id))
		if blob == nil {
			return ErrOrchestratorNotFound
		}
		o := &pb.Orchestrator{}
		if e := proto.Unmarshal(blob, o); e != nil {
			return e
		}
		queued, e := queuedRuns(tx.Bucket(runsBucket), id)
		if e != nil {
			return e
		}
		return fn(o, queued)
	})
}

// DeleteOrchestrator removes the record and every run (and the sequence
// counter) under its prefix in one transaction. A missing id is not an error.
func (r *Registry) DeleteOrchestrator(id string) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		if e := tx.Bucket(orchestratorsBucket).Delete([]byte(id)); e != nil {
			return e
		}
		runs := tx.Bucket(runsBucket)
		prefix := []byte(id + "/")
		var keys [][]byte
		c := runs.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			keys = append(keys, append([]byte(nil), k...))
		}
		for _, k := range keys {
			if e := runs.Delete(k); e != nil {
				return e
			}
		}
		return nil
	})
}

// --- runs ---

// NextRunSeq increments and returns the orchestrator's receipt sequence (1, 2,
// …), persisted under "<id>/_seq" so it is monotonic across restarts.
func (r *Registry) NextRunSeq(orchID string) (uint64, error) {
	var seq uint64
	err := r.db.Update(func(tx *bolt.Tx) error {
		var e error
		seq, e = nextSeq(tx.Bucket(runsBucket), orchID)
		return e
	})
	return seq, err
}

func nextSeq(b *bolt.Bucket, orchID string) (uint64, error) {
	var seq uint64
	key := []byte(orchID + "/" + runSeqSuffix)
	if cur := b.Get(key); len(cur) == 8 {
		seq = binary.BigEndian.Uint64(cur)
	}
	seq++
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, seq)
	return seq, b.Put(key, buf)
}

// PutRun inserts or replaces a run under its receipt-ordered key. The run must
// carry orchestrator_id and an id of the form run-<x>-<seq>.
func (r *Registry) PutRun(run *pb.Run) error {
	return r.db.Update(func(tx *bolt.Tx) error { return putRun(tx.Bucket(runsBucket), run) })
}

func putRun(b *bolt.Bucket, run *pb.Run) error {
	if run.GetOrchestratorId() == "" || RunSeq(run.GetId()) == 0 {
		return errors.New("run needs an orchestrator id and a sequenced run id")
	}
	blob, err := proto.Marshal(run)
	if err != nil {
		return fmt.Errorf("marshal run: %w", err)
	}
	return b.Put(runKey(run.GetOrchestratorId(), RunSeq(run.GetId())), blob)
}

// GetRun returns one run, or ErrRunNotFound.
func (r *Registry) GetRun(orchID, runID string) (*pb.Run, error) {
	var out *pb.Run
	err := r.db.View(func(tx *bolt.Tx) error {
		var e error
		out, e = getRun(tx.Bucket(runsBucket), orchID, runID)
		return e
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func getRun(b *bolt.Bucket, orchID, runID string) (*pb.Run, error) {
	blob := b.Get(runKey(orchID, RunSeq(runID)))
	if blob == nil {
		return nil, ErrRunNotFound
	}
	run := &pb.Run{}
	if e := proto.Unmarshal(blob, run); e != nil {
		return nil, e
	}
	if run.GetId() != runID {
		return nil, ErrRunNotFound
	}
	return run, nil
}

// ListRuns returns the orchestrator's runs in order of receipt (key order).
func (r *Registry) ListRuns(orchID string) ([]*pb.Run, error) {
	var out []*pb.Run
	err := r.db.View(func(tx *bolt.Tx) error {
		var e error
		out, e = scanRuns(tx.Bucket(runsBucket), orchID, func(*pb.Run) bool { return true })
		return e
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanRuns returns the orchestrator's runs that keep accepts, in receipt order.
func scanRuns(b *bolt.Bucket, orchID string, keep func(*pb.Run) bool) ([]*pb.Run, error) {
	var out []*pb.Run
	prefix := []byte(orchID + "/")
	seqKey := []byte(orchID + "/" + runSeqSuffix)
	c := b.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		if bytes.Equal(k, seqKey) {
			continue
		}
		run := &pb.Run{}
		if e := proto.Unmarshal(v, run); e != nil {
			return nil, e
		}
		if keep(run) {
			out = append(out, run)
		}
	}
	return out, nil
}

func queuedRuns(b *bolt.Bucket, orchID string) ([]*pb.Run, error) {
	return scanRuns(b, orchID, func(r *pb.Run) bool { return r.GetState() == pb.RunState_RUN_STATE_QUEUED })
}

// DeleteRun removes one run. A missing run is not an error.
func (r *Registry) DeleteRun(orchID, runID string) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(runsBucket).Delete(runKey(orchID, RunSeq(runID)))
	})
}

// --- worker tokens (daemon-local, never on the wire) ---

// PutWorkerToken stores the sandbox's gateway token.
func (r *Registry) PutWorkerToken(sandboxID, token string) error {
	if sandboxID == "" || token == "" {
		return errors.New("sandbox id and token are required")
	}
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(workerTokensBucket).Put([]byte(sandboxID), []byte(token))
	})
}

// GetWorkerToken returns the sandbox's token, or ErrTokenNotFound.
func (r *Registry) GetWorkerToken(sandboxID string) (string, error) {
	var out string
	err := r.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(workerTokensBucket).Get([]byte(sandboxID))
		if v == nil {
			return ErrTokenNotFound
		}
		out = string(v)
		return nil
	})
	return out, err
}

// ListWorkerTokens returns every stored token keyed by sandbox id (the MCP
// endpoint's startup index).
func (r *Registry) ListWorkerTokens() (map[string]string, error) {
	out := map[string]string{}
	err := r.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(workerTokensBucket).ForEach(func(k, v []byte) error {
			out[string(k)] = string(v)
			return nil
		})
	})
	return out, err
}

// DeleteWorkerToken revokes the sandbox's token. A missing key is not an error.
func (r *Registry) DeleteWorkerToken(sandboxID string) error {
	return r.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(workerTokensBucket).Delete([]byte(sandboxID))
	})
}
