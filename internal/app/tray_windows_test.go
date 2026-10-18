//go:build windows

package app

import (
	"testing"
	"unsafe"
)

// Shell_NotifyIconW checks cbSize: a struct laid out wrong is refused (or reads past the end).
func TestNotifyIconDataSize(t *testing.T) {
	want := uintptr(976)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		want = 956
	}
	if got := unsafe.Sizeof(notifyIconData{}); got != want {
		t.Fatalf("NOTIFYICONDATAW is %d bytes, want %d", got, want)
	}
}

func TestCopyUTF16Truncates(t *testing.T) {
	var b [4]uint16
	copyUTF16(b[:], "abcdef")
	if b != [4]uint16{'a', 'b', 'c', 0} {
		t.Fatalf("%v", b)
	}
}
