package auction

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// History is a compact price history for one auction house: Times holds
// snapshot times (unix seconds) and Items[id] the market price (copper) at
// each time, aligned with Times; 0 means not listed.
//
// Points from the last HourlyDays days are kept hourly; older ones are thinned
// to one per day, and anything older than KeepDays is dropped.
type History struct {
	Times []int64            `json:"t"`
	Items map[string][]int64 `json:"i"`
}

const (
	HourlyDays = 7
	KeepDays   = 120
)

// LoadHistory reads a history file; a missing file is an empty history.
func LoadHistory(path string) (*History, error) {
	h := &History{Items: map[string][]int64{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, h); err != nil {
		return nil, err
	}
	if h.Items == nil {
		h.Items = map[string][]int64{}
	}
	return h, nil
}

// Save writes the history.
func (h *History) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Add records a snapshot for the tracked items. It returns false when the
// snapshot is already recorded (Blizzard publishes about once an hour).
func (h *History) Add(s *Snapshot, track []int, now time.Time) bool {
	t := s.Updated.Unix()
	if n := len(h.Times); n > 0 && h.Times[n-1] >= t {
		return false
	}
	n := len(h.Times)
	h.Times = append(h.Times, t)
	for _, id := range track {
		k := strconv.Itoa(id)
		col := h.Items[k]
		for len(col) < n {
			col = append(col, 0)
		}
		var v int64
		if p, ok := s.Lookup(id); ok {
			v = p.Market
		}
		h.Items[k] = append(col, v)
	}
	// Items no longer tracked still need a value for the new time.
	for k, col := range h.Items {
		for len(col) < n+1 {
			col = append(col, 0)
		}
		h.Items[k] = col
	}
	h.compact(now)
	return true
}

// compact thins old points and drops items that were never listed.
func (h *History) compact(now time.Time) {
	hourlyFrom := now.Add(-HourlyDays * 24 * time.Hour).Unix()
	keepFrom := now.Add(-KeepDays * 24 * time.Hour).Unix()
	var keep []int
	lastDay := int64(-1)
	for i, t := range h.Times {
		switch {
		case t < keepFrom:
			continue
		case t < hourlyFrom:
			day := t / 86400
			if day == lastDay {
				continue
			}
			lastDay = day
		}
		keep = append(keep, i)
	}
	if len(keep) != len(h.Times) {
		times := make([]int64, len(keep))
		for j, i := range keep {
			times[j] = h.Times[i]
		}
		for k, col := range h.Items {
			nc := make([]int64, len(keep))
			for j, i := range keep {
				if i < len(col) {
					nc[j] = col[i]
				}
			}
			h.Items[k] = nc
		}
		h.Times = times
	}
	for k, col := range h.Items {
		empty := true
		for _, v := range col {
			if v != 0 {
				empty = false
				break
			}
		}
		if empty {
			delete(h.Items, k)
		}
	}
}

// Series returns (time, price) points for an item, skipping unlisted times.
func (h *History) Series(id int) [][2]int64 {
	col := h.Items[strconv.Itoa(id)]
	var out [][2]int64
	for i, t := range h.Times {
		if i < len(col) && col[i] > 0 {
			out = append(out, [2]int64{t, col[i]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}
