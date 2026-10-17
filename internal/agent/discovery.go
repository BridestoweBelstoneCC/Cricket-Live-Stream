package agent

import (
	"context"
	"fmt"
	"net"
	"syscall"
)

// ListenDiscovery opens the UDP discovery port with SO_REUSEADDR and SO_BROADCAST, as
// the original did, so a restarted agent can rebind at once.
func ListenDiscovery(port int) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) { serr = setSockOpts(fd) })
		if err != nil {
			return err
		}
		return serr
	}}
	return lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf(":%d", port))
}
