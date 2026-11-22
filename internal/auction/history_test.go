package auction

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHistory(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	h := &History{Items: map[string][]int64{}}
	snap := func(at time.Time, items map[int]Price) *Snapshot { return &Snapshot{Updated: at, Items: items} }

	// 10 days of hourly snapshots; item 1 always listed, item 2 only in the last hour.
	start := now.Add(-10 * 24 * time.Hour)
	for at := start; !at.After(now); at = at.Add(time.Hour) {
		items := map[int]Price{1: {Market: 100}}
		if at.Equal(now) {
			items[2] = Price{Market: 50}
		}
		if !h.Add(snap(at, items), []int{1, 2, 3}, now) {
			t.Fatal("add refused")
		}
	}
	if h.Add(snap(now, nil), []int{1}, now) {
		t.Fatal("duplicate snapshot accepted")
	}
	// 7 days hourly (169 points incl. both ends) + ~3 older days, one point each.
	if n := len(h.Times); n < 169+3 || n > 169+4 {
		t.Fatalf("times = %d", n)
	}
	if _, ok := h.Items["3"]; ok {
		t.Fatal("never-listed item kept")
	}
	for k, col := range h.Items {
		if len(col) != len(h.Times) {
			t.Fatalf("item %s misaligned", k)
		}
	}
	if s := h.Series(2); len(s) != 1 || s[0][1] != 50 {
		t.Fatalf("series 2 = %v", s)
	}
	p := filepath.Join(t.TempDir(), "h.json")
	if err := h.Save(p); err != nil {
		t.Fatal(err)
	}
	back, err := LoadHistory(p)
	if err != nil || len(back.Times) != len(h.Times) || len(back.Series(1)) != len(h.Times) {
		t.Fatalf("round trip: %v", err)
	}
}
