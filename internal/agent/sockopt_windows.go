package agent

import "syscall"

func setSockOpts(fd uintptr) error {
	h := syscall.Handle(fd)
	if err := syscall.SetsockoptInt(h, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		return err
	}
	syscall.SetsockoptInt(h, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1) // best-effort
	return nil
}
