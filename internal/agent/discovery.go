package agent

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"cricketstream/internal/port"
)

// ListenDiscovery opens the UDP discovery port with SO_REUSEADDR and SO_BROADCAST, as
// the original did, so a restarted agent can rebind at once.
func ListenDiscovery(udpPort int) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) { serr = setSockOpts(fd) })
		if err != nil {
			return err
		}
		return serr
	}}
	host := ""
	if port.NetworkHost() != "0.0.0.0" {
		host = port.NetworkHost()
	}
	return lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("%s:%d", host, udpPort))
}
