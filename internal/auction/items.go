package auction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TooltipLine is one line of an item tooltip.
type TooltipLine struct {
	Text  string `json:"t"`
	Color string `json:"c,omitempty"` // CSS colour, e.g. "#1eff00"
}

// ItemInfo is the cached tooltip for one item.
type ItemInfo struct {
	Name    string        `json:"name,omitempty"`
	Quality string        `json:"quality,omitempty"` // POOR, COMMON, UNCOMMON, RARE, EPIC, LEGENDARY
	Lines   []TooltipLine `json:"lines,omitempty"`
	Missing bool          `json:"missing,omitempty"` // the API has no data for it
	Fetched time.Time     `json:"fetched"`
}

// ItemCache maps item id to tooltip data.
type ItemCache map[int]*ItemInfo

// LoadItemCache reads a cache file; a missing file is an empty cache.
func LoadItemCache(path string) (ItemCache, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ItemCache{}, nil
	}
	if err != nil {
		return nil, err
	}
	c := ItemCache{}
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}

// Save writes the cache.
func (c ItemCache) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Display is Blizzard's {display_string, color} pair.
type display struct {
	DisplayString string `json:"display_string"`
	Color         *struct {
		R, G, B int
	} `json:"color"`
}

func (d *display) line() (TooltipLine, bool) {
	if d == nil || strings.TrimSpace(d.DisplayString) == "" {
		return TooltipLine{}, false
	}
	l := TooltipLine{Text: d.DisplayString}
	if d.Color != nil {
		l.Color = fmt.Sprintf("#%02x%02x%02x", d.Color.R, d.Color.G, d.Color.B)
	}
	return l, true
}

type named struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// previewItem is the part of /data/wow/item/{id} we render.
type previewItem struct {
	Name          string   `json:"name"`
	Quality       named    `json:"quality"`
	Binding       *named   `json:"binding"`
	Unique        *display `json:"unique_equipped"`
	InventoryType *named   `json:"inventory_type"`
	ItemSubclass  *named   `json:"item_subclass"`
	Armor         *struct {
		Display display `json:"display"`
	} `json:"armor"`
	Weapon *struct {
		Damage      display `json:"damage"`
		AttackSpeed display `json:"attack_speed"`
		DPS         display `json:"dps"`
	} `json:"weapon"`
	Stats []struct {
		Display display `json:"display"`
	} `json:"stats"`
	Spells []struct {
		Description string `json:"description"`
	} `json:"spells"`
	Durability   *display `json:"durability"`
	Requirements *struct {
		Level *display `json:"level"`
		Skill *display `json:"skill"`
	} `json:"requirements"`
	Description string `json:"description"`
}

var qualityColors = map[string]string{
	"POOR": "#9d9d9d", "COMMON": "#ffffff", "UNCOMMON": "#1eff00",
	"RARE": "#0070dd", "EPIC": "#a335ee", "LEGENDARY": "#ff8000",
}

// tooltip renders an item response as tooltip lines.
func tooltip(body []byte) (*ItemInfo, error) {
	var r struct {
		Name    string      `json:"name"`
		Quality named       `json:"quality"`
		Preview previewItem `json:"preview_item"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	p := r.Preview
	name := p.Name
	if name == "" {
		name = r.Name
	}
	q := p.Quality.Type
	if q == "" {
		q = r.Quality.Type
	}
	info := &ItemInfo{Name: name, Quality: q}
	add := func(text, color string) {
		if strings.TrimSpace(text) != "" {
			info.Lines = append(info.Lines, TooltipLine{Text: text, Color: color})
		}
	}
	addD := func(d *display) {
		if l, ok := d.line(); ok {
			info.Lines = append(info.Lines, l)
		}
	}
	add(name, qualityColors[q])
	if p.Binding != nil {
		add(p.Binding.Name, "")
	}
	addD(p.Unique)
	slot := ""
	if p.InventoryType != nil && p.InventoryType.Type != "NON_EQUIP" {
		slot = p.InventoryType.Name
	}
	sub := ""
	if p.ItemSubclass != nil && slot != "" {
		sub = p.ItemSubclass.Name
	}
	if slot != "" || sub != "" {
		add(strings.TrimSpace(slot+"\t"+sub), "")
	}
	if p.Weapon != nil {
		dmg, spd := p.Weapon.Damage.DisplayString, p.Weapon.AttackSpeed.DisplayString
		if dmg != "" || spd != "" {
			add(strings.TrimSpace(dmg+"\t"+spd), "")
		}
		addD(&p.Weapon.DPS)
	}
	if p.Armor != nil {
		addD(&p.Armor.Display)
	}
	for i := range p.Stats {
		addD(&p.Stats[i].Display)
	}
	addD(p.Durability)
	if p.Requirements != nil {
		addD(p.Requirements.Level)
		addD(p.Requirements.Skill)
	}
	for _, s := range p.Spells {
		add(s.Description, "#1eff00")
	}
	if p.Description != "" {
		add(`"`+p.Description+`"`, "#ffd100")
	}
	return info, nil
}

// FetchItems fills the cache with tooltips for ids that are missing or whose
// "missing" marker is older than a day. It saves progress as it goes.
func (b *Blizzard) FetchItems(ctx context.Context, cache ItemCache, ids []int, workers int, log io.Writer) (fetched, failed int, err error) {
	tok, err := b.token(ctx)
	if err != nil {
		return 0, 0, err
	}
	var todo []int
	for _, id := range ids {
		c := cache[id]
		if c == nil || (c.Missing && time.Since(c.Fetched) > 24*time.Hour) {
			todo = append(todo, id)
		}
	}
	sort.Ints(todo)
	if workers < 1 {
		workers = 1
	}
	base := b.APIBase
	if base == "" {
		base = fmt.Sprintf("https://%s.api.blizzard.com", b.Target.Region)
	}
	var mu sync.Mutex
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				info, ferr := b.fetchItem(ctx, base, tok, id)
				mu.Lock()
				switch {
				case ferr == nil:
					cache[id] = info
					fetched++
				case errors.Is(ferr, errNotFound):
					cache[id] = &ItemInfo{Missing: true, Fetched: time.Now().UTC()}
					failed++
				default:
					failed++
					if log != nil && failed <= 5 {
						fmt.Fprintf(log, "item %d: %v\n", id, ferr)
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, id := range todo {
		select {
		case jobs <- id:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	return fetched, failed, ctx.Err()
}

func (b *Blizzard) fetchItem(ctx context.Context, base, tok string, id int) (*ItemInfo, error) {
	u := fmt.Sprintf("%s/data/wow/item/%d?namespace=static-forever-%s&locale=en_US", base, id, b.Target.Region)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := b.client().Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			info, err := tooltip(body)
			if err != nil {
				return nil, err
			}
			info.Fetched = time.Now().UTC()
			return info, nil
		case resp.StatusCode == http.StatusNotFound:
			return nil, errNotFound
		case resp.StatusCode == http.StatusTooManyRequests && attempt < 3:
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		default:
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(body))
		}
	}
}
