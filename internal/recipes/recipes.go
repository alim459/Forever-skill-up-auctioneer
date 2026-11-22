// Package recipes loads WoW: Forever profession recipe data: reagents,
// yields, required skill and the orange/yellow/green/grey skill-up thresholds.
//
// The default source is the compiled dataset published by the Forever-Tracker
// project (https://github.com/OrbitalBoom/Forever-Tracker,
// js/professions-data.js), which is built from LibProfessionDB (MIT) and
// wago.tools exports of the live Forever client. It is downloaded at run time
// and cached; it is not bundled in this repository.
package recipes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultURL is where the recipe dataset is downloaded from.
const DefaultURL = "https://raw.githubusercontent.com/OrbitalBoom/Forever-Tracker/main/js/professions-data.js"

// Reagent is one input of a recipe.
type Reagent struct {
	Item  int
	Count int
}

// Recipe is one craftable recipe.
type Recipe struct {
	SpellID     int
	Name        string
	Profession  int
	CraftedItem int // 0 for enchants and other non-item results
	YieldMin    int
	YieldMax    int
	// Thresholds are the skill levels at which the recipe turns
	// orange, yellow, green and grey.
	Thresholds    [4]int
	Required      int  // skill needed to learn it
	RequiredGuess bool // the data had no learn skill; Required is the yellow threshold
	Reagents      []Reagent
	Sources       string // letters: t trainer, v vendor, d drop, q quest, c/a other; "" unknown
	SourceNames   []string
	SkillUps      int     // points per successful skill-up (usually 1)
	CooldownHours float64 // > 0 for transmutes and other cooldown crafts
	TeachItem     int     // the recipe item that teaches it, if any
}

// AvgYield is the expected number of items one craft makes.
func (r *Recipe) AvgYield() float64 {
	lo, hi := r.YieldMin, r.YieldMax
	if lo <= 0 {
		lo = 1
	}
	if hi < lo {
		hi = lo
	}
	return float64(lo+hi) / 2
}

// Colour names the difficulty at a skill level.
func (r *Recipe) Colour(skill int) string {
	d := r.Thresholds
	switch {
	case skill < r.Required:
		return "unlearned"
	case skill >= d[3]:
		return "grey"
	case skill >= d[2]:
		return "green"
	case skill >= d[1]:
		return "yellow"
	default:
		return "orange"
	}
}

// Chance is the probability that a craft at this skill gives a skill-up:
// 1 while orange, (grey − skill)/(grey − yellow) through yellow and green,
// 0 at grey (the classic formula).
func (r *Recipe) Chance(skill int) float64 {
	d := r.Thresholds
	if skill < r.Required || skill >= d[3] {
		return 0
	}
	if skill < d[1] || d[3] <= d[1] {
		return 1
	}
	return float64(d[3]-skill) / float64(d[3]-d[1])
}

// Item is static item data.
type Item struct {
	ID           int
	Name         string
	Quality      int
	VendorBuy    int64 // copper for one vendor stack
	VendorSell   int64 // copper for one item
	SoldByVendor bool
	VendorStack  int
}

// VendorUnitPrice is the vendor's price for a single item, if it sells it.
func (it *Item) VendorUnitPrice() (float64, bool) {
	if it == nil || !it.SoldByVendor || it.VendorBuy <= 0 {
		return 0, false
	}
	st := it.VendorStack
	if st <= 0 {
		st = 1
	}
	return float64(it.VendorBuy) / float64(st), true
}

// Profession groups recipes.
type Profession struct {
	ID      int
	Name    string
	Recipes []*Recipe
}

// DB is the loaded dataset.
type DB struct {
	Built       string
	ClientBuild string
	Professions map[int]*Profession
	Items       map[int]*Item
}

// Item returns item data or nil.
func (db *DB) Item(id int) *Item { return db.Items[id] }

// ItemName returns a display name for an item id.
func (db *DB) ItemName(id int) string {
	if it := db.Items[id]; it != nil && it.Name != "" {
		return it.Name
	}
	return fmt.Sprintf("Item %d", id)
}

// FindProfession resolves a name (case-insensitive, prefix allowed) or numeric id.
func (db *DB) FindProfession(q string) (*Profession, error) {
	q = strings.TrimSpace(strings.ToLower(q))
	if id, err := strconv.Atoi(q); err == nil {
		if p := db.Professions[id]; p != nil {
			return p, nil
		}
	}
	norm := func(s string) string { return strings.ReplaceAll(strings.ToLower(s), " ", "") }
	var hits []*Profession
	for _, p := range db.Professions {
		n := norm(p.Name)
		if n == norm(q) {
			return p, nil
		}
		if strings.HasPrefix(n, norm(q)) {
			hits = append(hits, p)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	return nil, fmt.Errorf("unknown profession %q (have: %s)", q, strings.Join(db.ProfessionNames(), ", "))
}

// ProfessionNames lists profession names sorted.
func (db *DB) ProfessionNames() []string {
	var out []string
	for _, p := range db.Professions {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

// --- parsing -------------------------------------------------------------

type rawRecipe struct {
	ID int      `json:"id"`
	N  string   `json:"n"`
	I  int      `json:"i"`
	Y  []int    `json:"y"`
	D  []int    `json:"d"`
	R  int      `json:"r"`
	G  [][]int  `json:"g"`
	S  string   `json:"s"`
	TI int      `json:"ti"`
	U  int      `json:"u"`
	SN []string `json:"sn"`
	CD float64  `json:"cd"`
}

type rawDB struct {
	Meta struct {
		Built       string `json:"built"`
		ClientBuild string `json:"clientBuild"`
	} `json:"meta"`
	Professions map[string]struct {
		Name    string      `json:"name"`
		Recipes []rawRecipe `json:"recipes"`
	} `json:"professions"`
	// items[id] = [name, quality, vendorBuyCopper, vendorSellCopper, soldByVendor, vendorStack]
	Items map[string][]json.RawMessage `json:"items"`
}

// Parse reads the dataset. It accepts plain JSON or the JavaScript file
// ("window.PROF_DATA = {...};").
func Parse(b []byte) (*DB, error) {
	start := bytes.IndexByte(b, '{')
	end := bytes.LastIndexByte(b, '}')
	if start < 0 || end <= start {
		return nil, errors.New("recipes: no JSON object found")
	}
	var raw rawDB
	if err := json.Unmarshal(b[start:end+1], &raw); err != nil {
		return nil, fmt.Errorf("recipes: %w", err)
	}
	db := &DB{Built: raw.Meta.Built, ClientBuild: raw.Meta.ClientBuild,
		Professions: map[int]*Profession{}, Items: map[int]*Item{}}

	for idStr, row := range raw.Items {
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		it := &Item{ID: id, VendorStack: 1}
		get := func(i int, v any) {
			if i < len(row) {
				_ = json.Unmarshal(row[i], v)
			}
		}
		var sold, stack int
		get(0, &it.Name)
		get(1, &it.Quality)
		get(2, &it.VendorBuy)
		get(3, &it.VendorSell)
		get(4, &sold)
		get(5, &stack)
		it.SoldByVendor = sold != 0
		if stack > 0 {
			it.VendorStack = stack
		}
		db.Items[id] = it
	}

	for idStr, rp := range raw.Professions {
		pid, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		p := &Profession{ID: pid, Name: rp.Name}
		for _, rr := range rp.Recipes {
			if len(rr.D) < 4 {
				continue
			}
			r := &Recipe{
				SpellID: rr.ID, Name: rr.N, Profession: pid, CraftedItem: rr.I,
				Required: rr.R, Sources: rr.S, SourceNames: rr.SN,
				SkillUps: rr.U, CooldownHours: rr.CD, TeachItem: rr.TI,
			}
			copy(r.Thresholds[:], rr.D[:4])
			if len(rr.Y) >= 2 {
				r.YieldMin, r.YieldMax = rr.Y[0], rr.Y[1]
			} else {
				r.YieldMin, r.YieldMax = 1, 1
			}
			if r.SkillUps <= 0 {
				r.SkillUps = 1
			}
			if r.Required <= 0 {
				// No learn skill recorded: assume it is learnable at yellow.
				r.Required = r.Thresholds[1]
				r.RequiredGuess = true
			}
			for _, g := range rr.G {
				if len(g) >= 2 && g[0] > 0 && g[1] > 0 {
					r.Reagents = append(r.Reagents, Reagent{Item: g[0], Count: g[1]})
				}
			}
			p.Recipes = append(p.Recipes, r)
		}
		sort.Slice(p.Recipes, func(i, j int) bool {
			if p.Recipes[i].Required != p.Recipes[j].Required {
				return p.Recipes[i].Required < p.Recipes[j].Required
			}
			return p.Recipes[i].Name < p.Recipes[j].Name
		})
		db.Professions[pid] = p
	}
	if len(db.Professions) == 0 {
		return nil, errors.New("recipes: dataset has no professions")
	}
	return db, nil
}

// LoadFile parses a dataset on disk.
func LoadFile(path string) (*DB, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Download fetches the dataset from url, checks it parses, and writes it to path.
func Download(ctx context.Context, url, path string) (*DB, error) {
	if url == "" {
		url = DefaultURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("download recipes: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download recipes: HTTP %d from %s", resp.StatusCode, url)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	db, err := Parse(b)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return db, os.WriteFile(path, b, 0o644)
}

// SourceLabel turns source letters into words.
func SourceLabel(s string) string {
	if s == "" {
		return "unknown"
	}
	names := map[rune]string{'t': "trainer", 'v': "vendor", 'd': "drop", 'q': "quest", 'c': "other", 'a': "other"}
	seen := map[string]bool{}
	var out []string
	for _, c := range s {
		n := names[c]
		if n == "" {
			n = string(c)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return strings.Join(out, "/")
}
