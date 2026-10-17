package server

import (
	"net"
	"syscall"
)

// setBroadcast allows sending to broadcast addresses (best-effort).
func setBroadcast(pc net.PacketConn) {
	if uc, ok := pc.(*net.UDPConn); ok {
		if raw, err := uc.SyscallConn(); err == nil {
			raw.Control(func(fd uintptr) {
				syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
			})
		}
	}
}
