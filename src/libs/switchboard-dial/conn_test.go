package dial

import (
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestHasCapability(t *testing.T) {
	c := &Conn{Info: &pb.DaemonInfo{Capabilities: []string{"lanes"}}}
	if !c.HasCapability("lanes") {
		t.Fatal("lanes advertised but not reported")
	}
	if c.HasCapability("other") {
		t.Fatal("unadvertised capability reported")
	}
	if (&Conn{}).HasCapability("lanes") {
		t.Fatal("a conn with no info reports no capability")
	}
}
