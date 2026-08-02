// Package forward owns the client side of feature 006 (Port Forwarding): the
// listeners on the developer's own machine, and the relay that carries their
// traffic to the daemon.
//
// The client is the SOLE allocator of ports on the developer's machine (research
// R1). Each forward binds 127.0.0.1:0 and lets the OS pick, which is what makes
// FR-049's "unique across all running services" structural rather than
// coordinated: two live listeners can never be handed the same port, whether they
// belong to different sandboxes, different services declaring the same listen
// port, or sandboxes on entirely different hosts.
//
// Binding 127.0.0.1 (never 0.0.0.0) is deliberate: this feature exposes a service
// to the developer, not to their network.
//
// Each accepted TCP connection opens one ForwardPort stream on the daemon
// connection the client already holds — a Unix socket locally, or the SSH
// dial-stdio bridge for a remote host. That is why a remote sandbox needs no
// second SSH session and no re-authentication: the bytes ride the transport that
// is already authenticated.
//
// Lifetime: a forward opens when its instance reaches RUNNING and closes on any
// terminal transition, on host disconnect, and on client exit. Closing the
// listener closes every relayed connection. The SERVICE is unaffected by any of
// this — it is daemon-owned and keeps running; only the developer-machine access
// path comes and goes, which is why a reconnect may hand out a different local
// port.
package forward
