//go:build !windows

package clubhouse

// adapterDescription: only Windows gives its cards names ("Ethernet 3") that hide what
// they are; elsewhere the name (eth0, vboxnet0, en5) is enough.
func adapterDescription(string) string { return "" }
