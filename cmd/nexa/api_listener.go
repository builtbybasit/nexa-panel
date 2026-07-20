package main

import (
	"net"

	"github.com/nexa-panel/nexa-panel/internal/platform/unixsocket"
)

// openAPIUnixSocket creates the control-plane ingress seam used by Nginx. The
// socket is group-readable, while stale sockets are removed safely; an existing
// regular file is never overwritten.
func openAPIUnixSocket(path string) (net.Listener, func(), error) {
	return unixsocket.Listen(path, 0o750, 0o660)
}
