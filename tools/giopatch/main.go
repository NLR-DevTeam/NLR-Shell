// Command giopatch materializes a patched copy of gioui.org in the user's
// cache directory and writes a Go modfile that points the build at it. The
// repository carries the Linux fixes (window decorations, cursor shape) as a
// small patch instead of vendoring the whole toolkit: run this from the
// module root and pass the printed modfile to the go command.
//
//	go build -modfile="$(go run ./tools/giopatch)" .
//
// build.sh and build.ps1 do this for release builds. A plain go build
// without the modfile uses upstream Gio, which cannot undecorate windows on
// X11 and Wayland.
package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed gioui-fixes.patch
var patch []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "giopatch:", err)
		os.Exit(1)
	}
}

// run patches the module, writes a modfile that replaces gioui.org with the
// patched copy, and prints its path.
func run() error {
	if _, err := os.Stat("go.mod"); err != nil {
		return errors.New("run from the module root (go.mod not found)")
	}
	if b, err := os.ReadFile("go.mod"); err != nil {
		return err
	} else if hasReplace(string(b), "gioui.org") {
		return errors.New("go.mod already replaces gioui.org; remove that replace first")
	}
	dir, version, err := module("gioui.org")
	if err != nil {
		return err
	}
	// Keep one patched copy per module version and patch revision so
	// repeated builds skip the copy and the patch.
	sum := sha256Hex(patch)
	cache := filepath.Join(cacheDir(), fmt.Sprintf("gioui-%s-%s", version, sum[:8]))
	stamp := filepath.Join(cache, ".giopatch-ok")
	if _, err := os.Stat(stamp); err != nil {
		if err := materialize(dir, cache); err != nil {
			os.RemoveAll(cache)
			return err
		}
		if err := os.WriteFile(stamp, []byte(sum+"\n"), 0o644); err != nil {
			return err
		}
	}
	// The modfile is a copy of the module's go.mod plus the replace. It
	// lives next to the patched copy so the repository stays clean; its
	// sum file must sit beside it under the same name.
	modfile := filepath.Join(cache, "go.gio.mod")
	b, err := os.ReadFile("go.mod")
	if err != nil {
		return err
	}
	b = append(bytes.TrimRight(b, "\n"), []byte("\n\nreplace gioui.org => "+filepath.ToSlash(cache)+"\n")...)
	if err := os.WriteFile(modfile, b, 0o644); err != nil {
		return err
	}
	sums, err := os.ReadFile("go.sum")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(filepath.Join(cache, "go.gio.sum"), sums, 0o644); err != nil {
		return err
	}
	fmt.Println(modfile)
	return nil
}

// hasReplace reports whether the modfile has a replace directive for the
// given module path, in either the single-line or the block form.
func hasReplace(modfile, path string) bool {
	inBlock := false
	for _, line := range strings.Split(modfile, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "replace (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock || strings.HasPrefix(line, "replace "):
			rest := strings.TrimPrefix(line, "replace ")
			if strings.HasPrefix(rest, path+" ") || strings.HasPrefix(rest, path+"=>") {
				return true
			}
		}
	}
	return false
}

// module returns the directory and version of the named module.
func module(path string) (string, string, error) {
	dir, version, err := goList(path)
	if err == nil && dir != "" {
		if _, err := os.Stat(dir); err == nil {
			return dir, version, nil
		}
	}
	cmd := exec.Command("go", "mod", "download", path)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("go mod download %s: %w", path, err)
	}
	dir, version, err = goList(path)
	if err != nil || dir == "" {
		return "", "", fmt.Errorf("cannot locate module %s: %v", path, err)
	}
	return dir, version, nil
}

func goList(path string) (string, string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}\t{{.Version}}", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("%s: %v", strings.TrimSpace(stderr.String()), err)
	}
	fields := strings.Split(strings.TrimSpace(stdout.String()), "\t")
	if len(fields) != 2 || fields[0] == "" {
		return "", "", fmt.Errorf("unexpected go list output %q", stdout.String())
	}
	return fields[0], fields[1], nil
}

func cacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "nlrshell")
}

// materialize copies the module to dst and applies the patch to it.
func materialize(src, dst string) error {
	if err := copyTree(src, dst); err != nil {
		return fmt.Errorf("copy module: %w", err)
	}
	files, err := parsePatch(string(patch))
	if err != nil {
		return fmt.Errorf("patch is malformed: %w", err)
	}
	// Apply every file the patch touches; a hard-coded list once made the
	// tool skip hunks silently.
	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		hunks := files[f]
		p := filepath.Join(dst, filepath.FromSlash(f))
		orig, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		patched, err := apply(hunks, orig)
		if err != nil {
			return fmt.Errorf("%s: %w (update tools/giopatch/gioui-fixes.patch)", f, err)
		}
		if err := os.WriteFile(p, patched, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			// Module cache directories are read-only; make the copy
			// writable so the patch can write into it.
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	})
}

// parsePatch splits a unified diff into the hunk text of each file, keyed by
// the module-relative path from the "+++ b/..." header.
func parsePatch(s string) (map[string][]string, error) {
	files := map[string][]string{}
	var hunks []string
	var file string
	flush := func() {
		if file != "" {
			files[file] = hunks
		}
	}
	for _, line := range strings.SplitAfter(s, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			flush()
			hunks, file = nil, ""
		case strings.HasPrefix(line, "+++ "):
			name := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			name = strings.TrimPrefix(name, "b/")
			if name == "" || name == "/dev/null" {
				return nil, fmt.Errorf("unsupported file header %q", strings.TrimSpace(line))
			}
			file = name
		case strings.HasPrefix(line, "@@"):
			if file == "" {
				return nil, errors.New("hunk before a file header")
			}
			hunks = append(hunks, strings.TrimSuffix(line, "\n"))
		default:
			if file == "" || len(hunks) == 0 || line == "" || line == "\n" {
				continue
			}
			if c := line[0]; c != ' ' && c != '-' && c != '+' && c != '\\' {
				return nil, fmt.Errorf("unexpected patch line %q", strings.TrimSuffix(line, "\n"))
			}
			hunks = append(hunks, strings.TrimSuffix(line, "\n"))
		}
	}
	flush()
	if len(files) == 0 {
		return nil, errors.New("no file sections")
	}
	return files, nil
}

// apply applies the hunks of one file section to src. Every hunk must match
// exactly where its header says; a mismatch is an error rather than a
// fuzzed match, because a misplaced hunk would compile into something else
// than intended.
func apply(hunks []string, src []byte) ([]byte, error) {
	lines := strings.Split(string(src), "\n")
	// delta is the line count added by the hunks applied so far: hunk
	// headers carry positions in the original file.
	delta := 0
	for i := 0; i < len(hunks); {
		header := hunks[i]
		if !strings.HasPrefix(header, "@@") {
			return nil, fmt.Errorf("expected a hunk header, got %q", header)
		}
		var start int
		if _, err := fmt.Sscanf(header, "@@ -%d", &start); err != nil {
			return nil, fmt.Errorf("bad hunk header %q", header)
		}
		i++
		var old, new []string
		for i < len(hunks) && !strings.HasPrefix(hunks[i], "@@") {
			line := hunks[i]
			i++
			if line == "" {
				continue
			}
			switch line[0] {
			case ' ':
				old = append(old, line[1:])
				new = append(new, line[1:])
			case '-':
				old = append(old, line[1:])
			case '+':
				new = append(new, line[1:])
			case '\\': // "\ No newline at end of file"
			}
		}
		// Hunk line numbers are 1-based.
		pos := start - 1 + delta
		if len(old) == 0 || pos < 0 || pos+len(old) > len(lines) {
			return nil, fmt.Errorf("hunk at line %d is outside the file", start)
		}
		for j, want := range old {
			if lines[pos+j] != want {
				return nil, fmt.Errorf("hunk at line %d does not match at %q", start, lines[pos+j])
			}
		}
		lines = append(lines[:pos], append(new, lines[pos+len(old):]...)...)
		delta += len(new) - len(old)
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
