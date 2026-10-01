package term

import (
	"strings"
	"testing"
)

func screen(t *Terminal) []string {
	t.Lock()
	defer t.Unlock()
	out := make([]string, t.rows)
	for y := 0; y < t.rows; y++ {
		out[y] = t.lines[y].Text()
	}
	return out
}

func expect(tb testing.TB, t *Terminal, want ...string) {
	tb.Helper()
	got := screen(t)
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			tb.Fatalf("row %d: got %q, want %q\nscreen:\n%s", i, got[i], w, strings.Join(got, "\n"))
		}
	}
}

func TestPrintAndWrap(t *testing.T) {
	tm := New(5, 3, 100, Handler{})
	tm.Write([]byte("abcdefg"))
	expect(t, tm, "abcde", "fg", "")
	if !tm.lines[0].Wrapped {
		t.Fatal("first line should be marked wrapped")
	}
	tm.Write([]byte("\r\nx"))
	expect(t, tm, "abcde", "fg", "x")
	if got := tm.TextRange(0, 0, 2, 5); got != "abcdefg\nx" {
		t.Fatalf("TextRange = %q", got)
	}
}

func TestScrollback(t *testing.T) {
	tm := New(10, 2, 100, Handler{})
	tm.Write([]byte("1\r\n2\r\n3\r\n4"))
	expect(t, tm, "3", "4")
	if tm.HistLen() != 2 || tm.LineAt(0).Text() != "1" || tm.LineAt(1).Text() != "2" {
		t.Fatalf("history wrong: %d", tm.HistLen())
	}
}

func TestCursorAndErase(t *testing.T) {
	tm := New(10, 3, 0, Handler{})
	tm.Write([]byte("hello\x1b[2;3Hworld\x1b[1;1H\x1b[K\x1b[2;5H\x1b[1K"))
	expect(t, tm, "", "     ld", "")
	tm.Write([]byte("\x1b[2J\x1b[Habc\x1b[2D\x1b[1P"))
	expect(t, tm, "ac")
	tm.Write([]byte("\x1b[1G\x1b[2@"))
	expect(t, tm, "  ac")
}

func TestScrollRegion(t *testing.T) {
	tm := New(5, 4, 100, Handler{})
	tm.Write([]byte("a\r\nb\r\nc\r\nd"))
	tm.Write([]byte("\x1b[2;3r\x1b[3;1H\n"))
	expect(t, tm, "a", "c", "", "d")
	if tm.HistLen() != 0 {
		t.Fatal("region scroll must not feed scrollback")
	}
	tm.Write([]byte("\x1b[2;1H\x1bM"))
	expect(t, tm, "a", "", "c", "d")
	tm.Write([]byte("\x1b[r\x1b[1;1H\x1b[1L"))
	expect(t, tm, "", "a", "", "c")
	tm.Write([]byte("\x1b[2M"))
	expect(t, tm, "", "c", "", "")
}

func TestAltScreen(t *testing.T) {
	tm := New(8, 2, 100, Handler{})
	tm.Write([]byte("main\x1b[?1049hALT"))
	expect(t, tm, "ALT", "")
	tm.Write([]byte("\x1b[?1049l!"))
	expect(t, tm, "main!", "")
}

func TestSGR(t *testing.T) {
	tm := New(20, 2, 0, Handler{})
	tm.Write([]byte("\x1b[1;31;48;5;200mA\x1b[0;38;2;1;2;3mB\x1b[38:2::4:5:6mC\x1b[38:5:9;4mD\x1b[4:0mE\x1b[mF"))
	c := tm.lines[0].Cells
	if i, _ := c[0].FG.Index(); i != 1 || c[0].Attr&AttrBold == 0 {
		t.Fatalf("A: %+v", c[0])
	}
	if i, _ := c[0].BG.Index(); i != 200 {
		t.Fatalf("A bg: %+v", c[0])
	}
	if r, g, b, ok := c[1].FG.RGB(); !ok || r != 1 || g != 2 || b != 3 || c[1].Attr != 0 {
		t.Fatalf("B: %+v", c[1])
	}
	if r, g, b, ok := c[2].FG.RGB(); !ok || r != 4 || g != 5 || b != 6 {
		t.Fatalf("C: %+v", c[2])
	}
	if i, _ := c[3].FG.Index(); i != 9 || c[3].Attr&AttrUnderline == 0 {
		t.Fatalf("D: %+v", c[3])
	}
	if c[4].Attr&AttrUnderline != 0 {
		t.Fatalf("E: %+v", c[4])
	}
	if !c[5].FG.IsDefault() || c[5].Attr != 0 {
		t.Fatalf("F: %+v", c[5])
	}
}

func TestWideAndUTF8Split(t *testing.T) {
	tm := New(6, 2, 0, Handler{})
	b := []byte("a中文b")
	tm.Write(b[:2])
	tm.Write(b[2:5])
	tm.Write(b[5:])
	expect(t, tm, "a中文b")
	c := tm.lines[0].Cells
	if c[1].Attr&AttrWide == 0 || c[2].Attr&AttrWideTail == 0 || c[5].R != 'b' {
		t.Fatalf("wide layout wrong: %+v", c)
	}
	// A wide char that does not fit wraps as a whole.
	tm.Write([]byte("\r\n\x1b[2J\x1b[H12345中"))
	expect(t, tm, "12345", "中")
	// Overwriting half of a wide char clears the other half.
	tm.Write([]byte("\x1b[2;2Hx"))
	expect(t, tm, "12345", " x")
}

func TestReplies(t *testing.T) {
	var got []byte
	tm := New(10, 5, 0, Handler{Reply: func(b []byte) { got = append(got, b...) }})
	tm.Write([]byte("\x1b[3;4H\x1b[6n\x1b[c"))
	if string(got) != "\x1b[3;4R\x1b[?62;22c" {
		t.Fatalf("reply = %q", got)
	}
}

func TestOSC(t *testing.T) {
	var title, cwd string
	tm := New(10, 2, 0, Handler{Title: func(s string) { title = s }, Cwd: func(s string) { cwd = s }})
	tm.Write([]byte("\x1b]0;hello\x07\x1b]7;file://host/home/a%20b\x1b\\ok"))
	if title != "hello" || cwd != "/home/a b" {
		t.Fatalf("title=%q cwd=%q", title, cwd)
	}
	expect(t, tm, "ok")
}

func TestResizeKeepsContent(t *testing.T) {
	tm := New(10, 4, 100, Handler{})
	tm.Write([]byte("1\r\n2\r\n3\r\n4"))
	tm.Resize(10, 2)
	expect(t, tm, "3", "4")
	if tm.HistLen() != 2 {
		t.Fatalf("hist=%d", tm.HistLen())
	}
	tm.Resize(10, 4)
	expect(t, tm, "1", "2", "3", "4")
	tm.Resize(3, 4)
	tm.Resize(12, 4)
	tm.Write([]byte("!"))
	expect(t, tm, "1", "2", "3", "4!")
}

func TestDecGraphics(t *testing.T) {
	tm := New(10, 1, 0, Handler{})
	tm.Write([]byte("\x1b(0lqk\x1b(Bx"))
	expect(t, tm, "┌─┐x")
}

func TestKeys(t *testing.T) {
	tm := New(10, 2, 0, Handler{})
	if s := string(tm.EncodeKey(KeyUp, 0)); s != "\x1b[A" {
		t.Fatalf("%q", s)
	}
	tm.Write([]byte("\x1b[?1h"))
	if s := string(tm.EncodeKey(KeyUp, 0)); s != "\x1bOA" {
		t.Fatalf("%q", s)
	}
	if s := string(tm.EncodeKey(KeyRight, ModCtrl)); s != "\x1b[1;5C" {
		t.Fatalf("%q", s)
	}
	if s := string(tm.EncodeKey(KeyDelete, ModShift)); s != "\x1b[3;2~" {
		t.Fatalf("%q", s)
	}
	tm.Write([]byte("\x1b[?1000h\x1b[?1006h"))
	if s := string(tm.EncodeMouse(MouseLeft, 2, 1, 0, false, false)); s != "\x1b[<0;3;2M" {
		t.Fatalf("%q", s)
	}
}

func BenchmarkWrite(b *testing.B) {
	tm := New(120, 40, 10000, Handler{})
	line := []byte("\x1b[32mdrwxr-xr-x\x1b[0m  2 root root  4096 Jan  1 00:00 \x1b[1;34msome-directory-name\x1b[0m 中文\r\n")
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		tm.Write(line)
	}
}
