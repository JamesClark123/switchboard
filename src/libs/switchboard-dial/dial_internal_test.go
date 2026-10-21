package dial

import (
	"os/exec"
	"testing"
	"time"
)

func TestStdioAddrAndDeadlines(t *testing.T) {
	if (stdioAddr{}).Network() != "stdio" || (stdioAddr{}).String() != "stdio" {
		t.Error("stdioAddr should report the stdio network")
	}
	c := &stdioConn{}
	if c.LocalAddr().Network() != "stdio" || c.RemoteAddr().String() != "stdio" {
		t.Error("conn addrs should be stdio")
	}
	if c.SetDeadline(time.Now()) != nil || c.SetReadDeadline(time.Now()) != nil || c.SetWriteDeadline(time.Now()) != nil {
		t.Error("deadline setters should be no-ops returning nil")
	}
}

func TestNewStdioConnStartError(t *testing.T) {
	if _, err := newStdioConn(exec.Command("/no/such/binary/switchboard-test")); err == nil {
		t.Error("expected start error for a missing binary")
	}
}

func TestTailBufferKeepsTail(t *testing.T) {
	var tb tailBuffer
	big := make([]byte, 5000)
	for i := range big {
		big[i] = 'a'
	}
	_, _ = tb.Write(big)
	_, _ = tb.Write([]byte("END"))
	s := tb.String()
	if len(s) != 4096 || s[len(s)-3:] != "END" {
		t.Errorf("tail = %d bytes ending %q", len(s), s[len(s)-3:])
	}
	if lastLine("a\n\n  b  \n\n") != "b" || lastLine("") != "" {
		t.Error("lastLine should return the last non-blank line")
	}
	if askpassProgram() == "" {
		t.Error("askpassProgram should resolve a path")
	}
}
