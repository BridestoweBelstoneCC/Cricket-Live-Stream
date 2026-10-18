//go:build windows

package app

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procCryptMsgClose = windows.NewLazySystemDLL("crypt32.dll").NewProc("CryptMsgClose")

// fileSignature checks path's Authenticode signature the way Windows does before running
// it: "" signer and an error if it isn't validly signed; else who signed it (the
// certificate's name, e.g. "SignPath Foundation"). No revocation check: it has to work
// offline at the ground, and the chain is still checked.
func fileSignature(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	info := windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: p}
	data := windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&info),
	}
	verr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	if verr != nil {
		return "", fmt.Errorf("not validly signed: %w", verr)
	}
	return signerName(path)
}

// signerName is the name on the certificate that signed path: the one in its signature
// that didn't issue any of the others (the rest are the chain above it).
func signerName(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var enc, ctype, ftype uint32
	var store windows.Handle
	var msg windows.Handle
	if err := windows.CryptQueryObject(windows.CERT_QUERY_OBJECT_FILE, unsafe.Pointer(p),
		windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED, windows.CERT_QUERY_FORMAT_FLAG_BINARY, 0,
		&enc, &ctype, &ftype, &store, &msg, nil); err != nil {
		return "", err
	}
	defer windows.CertCloseStore(store, 0)
	defer procCryptMsgClose.Call(uintptr(msg))
	name := func(c *windows.CertContext, flags uint32) string {
		buf := make([]uint16, 512)
		n := windows.CertGetNameString(c, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, flags, nil, &buf[0], uint32(len(buf)))
		if n <= 1 {
			return ""
		}
		return windows.UTF16ToString(buf[:n])
	}
	type cert struct{ subject, issuer string }
	var certs []cert
	var c *windows.CertContext
	for {
		c, err = windows.CertEnumCertificatesInStore(store, c)
		if err != nil || c == nil {
			break
		}
		certs = append(certs, cert{name(c, 0), name(c, windows.CERT_NAME_ISSUER_FLAG)})
	}
	for _, x := range certs {
		issuedOthers := false
		for _, y := range certs {
			if y.issuer == x.subject && y.subject != x.subject {
				issuedOthers = true
			}
		}
		if !issuedOthers && x.subject != "" {
			return x.subject, nil
		}
	}
	return "", errors.New("couldn't read who signed it")
}

// fileVersion is the ProductVersion in path's version resource ("" if none).
func fileVersion(path string) string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return ""
	}
	buf := make([]byte, size)
	if windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])) != nil {
		return ""
	}
	var tr unsafe.Pointer
	var trLen uint32
	if windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&tr), &trLen) != nil || trLen < 4 {
		return ""
	}
	lang, cp := *(*uint16)(tr), *(*uint16)(unsafe.Add(tr, 2))
	var val unsafe.Pointer
	var valLen uint32
	if windows.VerQueryValue(unsafe.Pointer(&buf[0]), fmt.Sprintf(`\StringFileInfo\%04x%04x\ProductVersion`, lang, cp),
		unsafe.Pointer(&val), &valLen) != nil || valLen == 0 {
		return ""
	}
	return windows.UTF16PtrToString((*uint16)(val))
}
