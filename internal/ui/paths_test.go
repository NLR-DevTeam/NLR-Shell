package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseLocalPaths(t *testing.T) {
	home := t.TempDir()
	// os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got, err := os.UserHomeDir(); err != nil || got != home {
		t.Skipf("home override ignored (%q, %v)", got, err)
	}
	abs := filepath.Join(string(filepath.Separator), "tmp", "a.txt")

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "uri list",
			in:   "file:///tmp/a.txt\r\nfile:///tmp/b%20b.txt\n",
			want: []string{abs, filepath.FromSlash("/tmp/b b.txt")},
		},
		{
			name: "localhost and comments",
			in:   "# comment\nfile://localhost/tmp/a.txt\n\n",
			want: []string{abs},
		},
		{
			name: "remote host is skipped",
			in:   "file://otherhost/tmp/a.txt\n" + abs + "\n",
			want: []string{abs},
		},
		{
			name: "plain and home paths",
			in:   "~/x.txt\n~\nrelative\nhello world\n",
			want: []string{filepath.Join(home, "x.txt"), home},
		},
		{
			name: "quoted and duplicated",
			in:   "\"" + abs + "\"\n" + abs + "\n",
			want: []string{abs},
		},
		{
			name: "nothing usable",
			in:   "some text\n\nhttps://example.com/x\n",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseLocalPaths(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %q, want %q", got, c.want)
				}
			}
		})
	}
}
