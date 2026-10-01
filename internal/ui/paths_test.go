package ui

import (
	"os"
	"path/filepath"
	"runtime"
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
	// Absolute paths and file URIs look different per platform: a Windows
	// path needs a drive, and its URI carries it as the first segment.
	root, uriPath := "/", "/"
	if runtime.GOOS == "windows" {
		root, uriPath = `C:\`, "/C:/"
	}
	abs := filepath.Join(root, "tmp", "a.txt")
	spaced := filepath.Join(root, "tmp", "b b.txt")

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "uri list",
			in:   "file://" + uriPath + "tmp/a.txt\r\nfile://" + uriPath + "tmp/b%20b.txt\n",
			want: []string{abs, spaced},
		},
		{
			name: "localhost and comments",
			in:   "# comment\nfile://localhost" + uriPath + "tmp/a.txt\n\n",
			want: []string{abs},
		},
		{
			name: "remote host is skipped",
			in:   "file://otherhost" + uriPath + "tmp/a.txt\n" + abs + "\n",
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
