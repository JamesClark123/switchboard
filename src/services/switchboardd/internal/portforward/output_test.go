package portforward

import (
	"strings"
	"sync"
	"testing"
)

func TestTailBufferUnderCapKeepsEverything(t *testing.T) {
	b := newTailBuffer(64)
	if _, err := b.Write([]byte("hello ")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_, _ = b.Write([]byte("world"))

	out, truncated := b.result()
	if out != "hello world" {
		t.Errorf("out = %q, want %q", out, "hello world")
	}
	if truncated {
		t.Error("under-cap output must not be marked truncated")
	}
}

func TestTailBufferKeepsTheTailNotTheHead(t *testing.T) {
	b := newTailBuffer(10)
	_, _ = b.Write([]byte("aaaaaaaaaa")) // fills it
	_, _ = b.Write([]byte("PANIC!"))     // the bytes that actually matter

	out, truncated := b.result()
	if !truncated {
		t.Error("over-cap output must be marked truncated")
	}
	if len(out) != 10 {
		t.Fatalf("len = %d, want 10", len(out))
	}
	if !strings.HasSuffix(out, "PANIC!") {
		t.Errorf("out = %q; the LAST bytes must survive — a service's failure is at the end", out)
	}
}

func TestTailBufferSingleOversizedWrite(t *testing.T) {
	b := newTailBuffer(4)
	_, _ = b.Write([]byte("0123456789"))

	out, truncated := b.result()
	if out != "6789" {
		t.Errorf("out = %q, want %q", out, "6789")
	}
	if !truncated {
		t.Error("an oversized single write must mark truncation")
	}
}

func TestTailBufferReportsFullWriteLength(t *testing.T) {
	// io.Writer contract: a short return would make io.Copy report ErrShortWrite.
	b := newTailBuffer(4)
	n, err := b.Write([]byte("0123456789"))
	if err != nil || n != 10 {
		t.Errorf("Write = (%d, %v), want (10, nil)", n, err)
	}
}

func TestTailBufferZeroLimitDropsEverything(t *testing.T) {
	b := newTailBuffer(0)
	_, _ = b.Write([]byte("x"))
	out, truncated := b.result()
	if out != "" || !truncated {
		t.Errorf("zero limit: out=%q truncated=%v; want empty and truncated", out, truncated)
	}
}

func TestTailBufferConcurrentWriters(t *testing.T) {
	// stdout and stderr stream in from separate goroutines; the buffer must not
	// race or lose its invariant.
	b := newTailBuffer(1024)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_, _ = b.Write([]byte("0123456789"))
			}
		}()
	}
	wg.Wait()

	out, truncated := b.result()
	if len(out) != 1024 {
		t.Errorf("len = %d, want the buffer pinned at its 1024 cap", len(out))
	}
	if !truncated {
		t.Error("10000 bytes into a 1024 cap must mark truncation")
	}
}
