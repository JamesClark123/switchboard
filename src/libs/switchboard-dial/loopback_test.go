package dial

import "testing"

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"localhost", "LOCALHOST", "localhost.", "app.localhost", "App.LocalHost.", "127.0.0.1", "127.0.0.2", "::1", "::ffff:127.0.0.1"} {
		if !IsLoopbackHost(h) {
			t.Errorf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"", ".", "example.com", "localhost.example.com", "notlocalhost", "10.0.0.1", "192.168.1.5", "evil.com.localhost.evil.com", "localhost.."} {
		if IsLoopbackHost(h) {
			t.Errorf("%q must not be loopback", h)
		}
	}
}
