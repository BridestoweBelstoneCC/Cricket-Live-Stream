package clubhouse

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// adapterDescription is what Windows says a network card is ("Realtek PCIe 2.5GbE Family
// Controller", "VirtualBox Host-Only Ethernet Adapter"), by the name Go reports for it
// (Windows' friendly name, "Ethernet 3"). "" if it can't be found.
func adapterDescription(friendlyName string) string {
	for _, a := range adapters() {
		if a.name == friendlyName {
			return a.desc
		}
	}
	return ""
}

type adapter struct {
	name, desc string
	ethernet   bool // IF_TYPE_ETHERNET_CSMACD: a wired port, or something posing as one
}

// adapters lists every network adapter Windows has, connected or not.
func adapters() []adapter {
	size := uint32(16 << 10)
	for try := 0; try < 3; try++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|
			windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue // size now holds what it needs
		}
		if err != nil {
			return nil
		}
		var out []adapter
		for a := first; a != nil; a = a.Next {
			out = append(out, adapter{name: windows.UTF16PtrToString(a.FriendlyName),
				desc: windows.UTF16PtrToString(a.Description), ethernet: a.IfType == windows.IF_TYPE_ETHERNET_CSMACD})
		}
		return out
	}
	return nil
}
