package clubhouse

// Wake-on-LAN: the screen switches the streaming laptop on with a "magic packet" — six
// 0xFF bytes and then its network card's address sixteen times, broadcast on the club
// network. The card listens for it while the laptop is off, if the laptop is set up for
// it (CLUBHOUSE_SCREEN.md: BIOS, the adapter's power settings, Fast Startup off, and a
// network CABLE — almost no laptop wakes over wifi).

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// ParseMAC reads a network card's address (aa:bb:cc:dd:ee:ff, AA-BB-..., or bare hex).
// Only 6-byte (EUI-48) addresses can be woken.
func ParseMAC(s string) (net.HardwareAddr, error) {
	s = strings.TrimSpace(s)
	if len(s) == 12 && !strings.ContainsAny(s, ":-.") {
		var parts []string
		for i := 0; i < 12; i += 2 {
			parts = append(parts, s[i:i+2])
		}
		s = strings.Join(parts, ":")
	}
	mac, err := net.ParseMAC(s)
	if err != nil {
		return nil, err
	}
	if len(mac) != 6 {
		return nil, fmt.Errorf("%s isn't a 6-byte network card address", s)
	}
	return mac, nil
}

// MagicPacket is the Wake-on-LAN packet for mac: 6 × 0xFF, then mac 16 times (102 bytes).
func MagicPacket(mac net.HardwareAddr) []byte {
	b := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		b = append(b, 0xff)
	}
	for i := 0; i < 16; i++ {
		b = append(b, mac...)
	}
	return b
}

// broadcastTargets is where a wake goes: the all-ones broadcast, and each network's own
// broadcast address (some routers and Windows hosts drop 255.255.255.255 but pass the
// directed one). Swappable for tests.
var broadcastTargets = func() []string {
	out := []string{"255.255.255.255"}
	return append(out, directedBroadcasts()...)
}

// directedBroadcasts is each up, non-loopback IPv4 network's broadcast address.
func directedBroadcasts() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || len(n.Mask) != net.IPv4len && len(n.Mask) != net.IPv6len {
				continue
			}
			ip, mask := n.IP.To4(), n.Mask
			if len(mask) == net.IPv6len {
				mask = mask[12:]
			}
			b := make(net.IP, 4)
			for i := range b {
				b[i] = ip[i] | ^mask[i]
			}
			out = append(out, b.String())
		}
	}
	return out
}

// sendUDP sends one datagram. Go turns SO_BROADCAST on for UDP sockets, so a broadcast
// address needs nothing special. Swappable for tests.
var sendUDP = func(addr string, b []byte) error {
	c, err := net.Dial("udp4", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write(b)
	return err
}

// wakePorts: 9 (discard) is the usual; 7 (echo) is what some cards and older tools use.
var wakePorts = []int{9, 7}

// Wake sends the magic packet for each address to every broadcast target, three times
// over a second (a packet is easily lost and costs nothing). It returns how many were
// sent; an error only when none could be.
func Wake(macs []string) (int, error) {
	var packets [][]byte
	var bad []string
	for _, m := range macs {
		mac, err := ParseMAC(m)
		if err != nil {
			bad = append(bad, m)
			continue
		}
		packets = append(packets, MagicPacket(mac))
	}
	if len(packets) == 0 {
		if len(bad) > 0 {
			return 0, fmt.Errorf("no usable network card address (%s)", strings.Join(bad, ", "))
		}
		return 0, errors.New("no network card address saved for the streaming laptop — pair the screen again")
	}
	sent := 0
	var last error
	for round := 0; round < 3; round++ {
		if round > 0 {
			time.Sleep(wakeGap)
		}
		for _, host := range broadcastTargets() {
			for _, port := range wakePorts {
				for _, p := range packets {
					if err := sendUDP(net.JoinHostPort(host, fmt.Sprint(port)), p); err != nil {
						last = err
						continue
					}
					sent++
				}
			}
		}
	}
	if sent == 0 {
		return 0, fmt.Errorf("couldn't send the wake-up on this network: %v", last)
	}
	return sent, nil
}

var wakeGap = 400 * time.Millisecond

// WakeableNIC is one of this computer's network cards, as the link reports it to the
// screen at pairing: the address to wake it by, and its name, so the screen can say
// "only wifi — a cable is needed" rather than wait for a wake that never comes.
type WakeableNIC struct {
	Name  string `json:"name"`
	MAC   string `json:"mac"`
	IP    string `json:"ip,omitempty"`
	Wired bool   `json:"wired"`
}

// LocalNICs is every up, non-loopback card with an IPv4 address and a 6-byte address,
// the one carrying preferIP first. "Wired" is a guess from the name (Windows calls them
// "Ethernet", Linux eth*/en*); a wifi card is still listed — it's what's there.
func LocalNICs(preferIP string) []WakeableNIC {
	var out []WakeableNIC
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || len(ifc.HardwareAddr) != 6 {
			continue
		}
		ip := ""
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				ip = n.IP.String()
				break
			}
		}
		if ip == "" || isVirtual(ifc.Name) {
			continue
		}
		nic := WakeableNIC{Name: ifc.Name, MAC: ifc.HardwareAddr.String(), IP: ip, Wired: looksWired(ifc.Name)}
		if ip == preferIP {
			out = append([]WakeableNIC{nic}, out...)
		} else {
			out = append(out, nic)
		}
	}
	return out
}

func looksWired(name string) bool {
	n := strings.ToLower(name)
	if strings.Contains(n, "wi-fi") || strings.Contains(n, "wifi") || strings.Contains(n, "wireless") ||
		strings.HasPrefix(n, "wl") {
		return false
	}
	return strings.Contains(n, "ethernet") || strings.HasPrefix(n, "eth") || strings.HasPrefix(n, "en")
}

// isVirtual: adapters no wake can reach — VPNs, Hyper-V/WSL/Docker switches, VirtualBox.
func isVirtual(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"tailscale", "vethernet", "virtualbox", "vmware", "docker", "wsl", "hyper-v",
		"loopback", "zerotier", "utun", "tun", "tap", "bridge", "br-", "virbr"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}
