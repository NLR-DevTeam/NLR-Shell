package store

import "testing"

func TestSecretRoundTrip(t *testing.T) {
	enc := Encrypt("p@ss 密码")
	if enc == "" || enc == "p@ss 密码" {
		t.Fatalf("not encrypted: %q", enc)
	}
	if got := Decrypt(enc); got != "p@ss 密码" {
		t.Fatalf("got %q", got)
	}
	if Decrypt("garbage!") != "" || Encrypt("") != "" {
		t.Fatal("bad input should yield empty")
	}
}

func TestProfiles(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	p := s.SaveProfile(Profile{Name: "web", Host: "10.0.0.1", User: "root"})
	if p.ID == "" || p.Port != 22 || p.Addr() != "root@10.0.0.1" {
		t.Fatalf("%+v", p)
	}
	s.SetPassword(p.ID, "secret")
	s.UpdateSettings(func(st *Settings) { st.FontSize = 18 })

	s2 := Open(dir)
	got, ok := s2.Profile(p.ID)
	if !ok || Decrypt(got.Password) != "secret" || s2.Settings().FontSize != 18 {
		t.Fatalf("reload: %+v", got)
	}
	s2.DeleteProfile(p.ID)
	if len(Open(dir).Profiles()) != 0 {
		t.Fatal("delete not persisted")
	}
}
