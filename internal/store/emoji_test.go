package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmojiFontSettings(t *testing.T) {
	for _, data := range []string{`{"settings":{}}`, `{"settings":{"emojiFont":""}}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		s := Open(dir)
		if got := s.Settings().EmojiFont; got != DefaultSettings().EmojiFont || got == "" {
			t.Fatalf("default emoji font: %q", got)
		}
		s.UpdateSettings(func(st *Settings) { st.EmojiFont = "custom emoji font" })
		if got := Open(dir).Settings().EmojiFont; got != "custom emoji font" {
			t.Fatalf("emoji font not persisted: %q", got)
		}
	}
}
