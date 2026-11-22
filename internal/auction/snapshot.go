// Package auction fetches WoW: Forever auction house data and reduces it to
// per-item prices.
package auction

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Price is the aggregated price of one item on one auction house.
// All money values are copper per single item.
type Price struct {
	MinBuyout  int64    `json:"minBuyout"`
	Market     int64    `json:"market"`   // average of the cheapest 15% of units listed
	Quantity   int64    `json:"quantity"` // units listed
	SoldPerDay *float64 `json:"soldPerDay,omitempty"`
}

// Snapshot is a full price snapshot for one auction house.
type Snapshot struct {
	Source    string        `json:"source"`
	HouseName string        `json:"houseName,omitempty"` // e.g. "Alliance Auction House", when the API gives one
	Region    string        `json:"region"`
	Ruleset   int           `json:"ruleset"`
	House     int           `json:"house"`
	Updated   time.Time     `json:"updated"` // when the data was produced upstream
	Fetched   time.Time     `json:"fetched"` // when we downloaded it
	Items     map[int]Price `json:"items"`
}

// Lookup returns the price for an item, if listed.
func (s *Snapshot) Lookup(itemID int) (Price, bool) {
	if s == nil {
		return Price{}, false
	}
	p, ok := s.Items[itemID]
	return p, ok && p.Market > 0
}

// unit is one listing reduced to a per-item price and a quantity.
type unit struct {
	price float64
	qty   int64
}

// aggregate turns raw listings into prices using the "cheapest 15% of units"
// market value rule (the same rule the Forever Goblin site documents).
func aggregate(units map[int][]unit) map[int]Price {
	out := make(map[int]Price, len(units))
	for id, us := range units {
		if len(us) == 0 {
			continue
		}
		sort.Slice(us, func(i, j int) bool { return us[i].price < us[j].price })
		var total int64
		for _, u := range us {
			total += u.qty
		}
		if total <= 0 {
			continue
		}
		take := us[0].qty
		if total >= 14 {
			take = int64(float64(total)*0.15 + 0.5)
			if take < 1 {
				take = 1
			}
		}
		var got int64
		var acc float64
		for _, u := range us {
			n := u.qty
			if got+n > take {
				n = take - got
			}
			acc += u.price * float64(n)
			got += n
			if got >= take {
				break
			}
		}
		out[id] = Price{
			MinBuyout: int64(us[0].price + 0.5),
			Market:    int64(acc/float64(got) + 0.5),
			Quantity:  total,
		}
	}
	return out
}

// FileName is the default file name for a processed snapshot.
func FileName(region string, house int) string {
	return fmt.Sprintf("prices-%s-%d.json", region, house)
}

// Save writes the snapshot as JSON.
func (s *Snapshot) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a snapshot written by Save.
func Load(path string) (*Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &s, nil
}
