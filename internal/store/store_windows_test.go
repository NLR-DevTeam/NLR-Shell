package store

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiEncrypt writes a secret the way versions before 1.1 did.
func dpapiEncrypt(t *testing.T, s string) string {
	t.Helper()
	in := []byte(s)
	var out windows.DataBlob
	if err := windows.CryptProtectData(&windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		t.Fatal(err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return base64.StdEncoding.EncodeToString(unsafe.Slice(out.Data, out.Size))
}

func TestLegacySecretsAreReencrypted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NLRSHELL_DATA", dir)
	old := dpapiEncrypt(t, "旧密码")
	cfg := fileData{Profiles: []Profile{{ID: "p1", Host: "h", Password: old, Passphrase: dpapiEncrypt(t, "pp")}}}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	p, _ := Open(dir).Profile("p1")
	if p.Password == old || Decrypt(p.Password) != "旧密码" || Decrypt(p.Passphrase) != "pp" {
		t.Fatalf("not migrated: %+v", p)
	}
	// The conversion was saved, so the file no longer holds DPAPI blobs.
	p, _ = Open(dir).Profile("p1")
	if Decrypt(p.Password) != "旧密码" {
		t.Fatalf("migration not persisted: %+v", p)
	}
}

func TestMigrateDir(t *testing.T) {
	old, dst := filepath.Join(t.TempDir(), "old"), filepath.Join(t.TempDir(), "data")
	os.MkdirAll(filepath.Join(old, "sub"), 0o700)
	os.WriteFile(filepath.Join(old, "config.json"), []byte(`{"profiles":[]}`), 0o600)
	os.WriteFile(filepath.Join(old, "known_hosts"), []byte("h ssh-ed25519 AAAA\n"), 0o600)
	os.WriteFile(filepath.Join(old, "sub", "x"), []byte("x"), 0o600)
	os.MkdirAll(dst, 0o700)

	migrateDir(old, dst)
	for _, f := range []string{"config.json", "known_hosts", filepath.Join("sub", "x")} {
		if _, err := os.Stat(filepath.Join(dst, f)); err != nil {
			t.Fatalf("%s not copied: %v", f, err)
		}
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old directory not removed: %v", err)
	}

	// Existing data is never overwritten, and the old directory then stays.
	os.MkdirAll(old, 0o700)
	os.WriteFile(filepath.Join(old, "config.json"), []byte(`{"other":1}`), 0o600)
	migrateDir(old, dst)
	if b, _ := os.ReadFile(filepath.Join(dst, "config.json")); string(b) != `{"profiles":[]}` {
		t.Fatalf("existing data overwritten: %s", b)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("old directory removed although nothing was migrated")
	}
}
