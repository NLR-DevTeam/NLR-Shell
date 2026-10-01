package store

import (
	"encoding/base64"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Encrypt protects s with DPAPI for the current Windows user. It returns an
// empty string for empty input or on failure.
func Encrypt(s string) string {
	if s == "" {
		return ""
	}
	in := []byte(s)
	var out windows.DataBlob
	err := windows.CryptProtectData(&windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return ""
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return base64.StdEncoding.EncodeToString(unsafe.Slice(out.Data, out.Size))
}

// Decrypt reverses Encrypt. It returns an empty string on failure.
func Decrypt(s string) string {
	if s == "" {
		return ""
	}
	in, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(in) == 0 {
		return ""
	}
	var out windows.DataBlob
	err = windows.CryptUnprotectData(&windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return ""
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return string(unsafe.Slice(out.Data, out.Size))
}
