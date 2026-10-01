//go:build !windows

package store

// legacyDecrypt opens secrets in an older format. Unix builds never had
// one.
func legacyDecrypt(string) (string, bool) { return "", false }
