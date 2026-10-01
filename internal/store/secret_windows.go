package store

import (
	"encoding/base64"
	"unsafe"

	"golang.org/x/sys/windows"
)

// legacyDecrypt opens a secret written by versions before 1.1, which used
// DPAPI for the current Windows user. Such secrets only open on the
// machine and account that wrote them, so they are re-encrypted with the
// portable key when the store is opened (see migrateSecrets).
func legacyDecrypt(s string) (string, bool) {
	in, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(in) == 0 {
		return "", false
	}
	var out windows.DataBlob
	err = windows.CryptUnprotectData(&windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return "", false
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return string(unsafe.Slice(out.Data, out.Size)), true
}
