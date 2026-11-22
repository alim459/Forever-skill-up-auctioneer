package auction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Goblin reads Forever Goblin (forevergoblin.trade), a free community site
// that republishes Blizzard's hourly Forever auction snapshots. No API key is
// needed, which makes it a handy fallback.
//
//	GET https://forevergoblin.trade/api/m/{region}-{house}/prices
//
// Each row is [itemId, marketPrice, lowest, quantity, change24h, ?, soldPerDay].
type Goblin struct {
	Target Target
	RawDir string
	HTTP   *http.Client
	Base   string // default https://forevergoblin.trade
}

func (g *Goblin) Name() string { return "Forever Goblin (forevergoblin.trade)" }

func (g *Goblin) Fetch(ctx context.Context) (*Snapshot, error) {
	base := g.Base
	if base == "" {
		base = "https://forevergoblin.trade"
	}
	t := g.Target
	u := fmt.Sprintf("%s/api/m/%s-%d/prices", base, t.Region, t.House)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "forever-skill-up-auctioneer")
	c := g.HTTP
	if c == nil {
		c = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("goblin: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden && bytes.Contains(body, []byte("Just a moment")) {
		return nil, fmt.Errorf("goblin: blocked by the site's bot protection (common from cloud servers); use Blizzard API keys instead")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("goblin: HTTP %d: %s", resp.StatusCode, truncate(body))
	}
	if g.RawDir != "" {
		if err := os.MkdirAll(g.RawDir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(g.RawDir, fmt.Sprintf("goblin-%s-%d.json", t.Region, t.House)), body, 0o644)
		}
	}
	var d struct {
		Updated float64             `json:"updated"`
		Items   [][]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("goblin: bad JSON: %w", err)
	}
	num := func(row []json.RawMessage, i int) (float64, bool) {
		if i >= len(row) {
			return 0, false
		}
		var f *float64
		if json.Unmarshal(row[i], &f) != nil || f == nil {
			return 0, false
		}
		return *f, true
	}
	items := make(map[int]Price, len(d.Items))
	for _, row := range d.Items {
		id, ok := num(row, 0)
		if !ok {
			continue
		}
		mkt, _ := num(row, 1)
		low, _ := num(row, 2)
		qty, _ := num(row, 3)
		p := Price{MinBuyout: int64(low), Market: int64(mkt), Quantity: int64(qty)}
		if spd, ok := num(row, 6); ok {
			p.SoldPerDay = &spd
		}
		items[int(id)] = p
	}
	now := time.Now().UTC()
	updated := now
	if d.Updated > 0 {
		updated = time.Unix(int64(d.Updated), 0).UTC()
	}
	return &Snapshot{Source: g.Name(), Region: t.Region, Ruleset: t.Ruleset, House: t.House,
		Updated: updated, Fetched: now, Items: items}, nil
}
