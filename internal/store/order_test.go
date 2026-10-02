package store

import (
	"strings"
	"testing"
)

func names(ps []Profile) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Group+"/"+p.Name)
	}
	return strings.Join(out, " ")
}

func TestMoveProfile(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	ids := map[string]string{}
	for i, n := range []string{"a", "b", "c", "d"} {
		g := "g1"
		if n == "d" {
			g = "g2"
		}
		ids[n] = s.SaveProfile(Profile{Name: n, Group: g, Host: n, LastUsed: int64(10 - i)}).ID
	}
	if got := names(s.Profiles()); got != "g1/a g1/b g1/c g2/d" {
		t.Fatalf("recent order: %s", got)
	}

	// Dragging c in front of a starts the manual order from the one shown.
	s.MoveProfile(ids["c"], "g1", ids["a"])
	if got := names(s.Profiles()); got != "g1/c g1/a g1/b g2/d" || !s.ManualOrder() {
		t.Fatalf("after move: %s", got)
	}
	// Using a connection no longer reorders them.
	s.Touch(ids["b"])
	// Dropping a at the end of g2 moves it to that group.
	s.MoveProfile(ids["a"], "g2", "")
	if got := names(s.Profiles()); got != "g1/c g1/b g2/d g2/a" {
		t.Fatalf("after move to g2: %s", got)
	}
	// The order survives a restart.
	if got := names(Open(dir).Profiles()); got != "g1/c g1/b g2/d g2/a" {
		t.Fatalf("after reopen: %s", got)
	}
	// Back to most recently used first.
	s.SetManualOrder(false)
	if got := names(s.Profiles()); !strings.HasPrefix(got, "g1/b ") {
		t.Fatalf("recent order again: %s", got)
	}
}
