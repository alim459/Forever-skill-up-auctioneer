// Package service builds the JSON answers shared by the local web server and
// the in-browser (WebAssembly) build used for the hosted site.
package service

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alim459/forever-skill-up-auctioneer/internal/advisor"
	"github.com/alim459/forever-skill-up-auctioneer/internal/auction"
	"github.com/alim459/forever-skill-up-auctioneer/internal/recipes"
)

// PriceInfo summarises a snapshot.
func PriceInfo(s *auction.Snapshot) map[string]any {
	return map[string]any{"source": s.Source, "updated": s.Updated, "fetched": s.Fetched,
		"items": len(s.Items), "house": s.House, "region": s.Region, "houseName": s.HouseName}
}

// Professions lists professions for the UI.
func Professions(db *recipes.DB) []map[string]any {
	var out []map[string]any
	for _, p := range db.Professions {
		out = append(out, map[string]any{"id": p.ID, "name": p.Name, "recipes": len(p.Recipes)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out
}

// Status is the /api/status answer.
func Status(db *recipes.DB, snap *auction.Snapshot, pricesErr error) map[string]any {
	out := map[string]any{
		"recipes":     map[string]any{"built": db.Built, "clientBuild": db.ClientBuild},
		"professions": Professions(db),
	}
	if snap != nil {
		out["prices"] = PriceInfo(snap)
	} else if pricesErr != nil {
		out["pricesError"] = pricesErr.Error()
	}
	return out
}

// Recommend answers a query string such as
// prof=197&skill=120&target=175&sources=tv?&cooldowns=0&noCraft=0&exclude=1,2
func Recommend(db *recipes.DB, snap *auction.Snapshot, q url.Values) (map[string]any, error) {
	p, err := db.FindProfession(q.Get("prof"))
	if err != nil {
		return nil, err
	}
	skill := clamp(atoi(q.Get("skill"), 1), 1, 300)
	target := clamp(atoi(q.Get("target"), skill+25), skill, 300)
	opt := advisor.DefaultOptions()
	if s, ok := q["sources"]; ok {
		opt.Sources = strings.Join(s, "")
		if opt.Sources == "all" {
			opt.Sources = ""
		}
	}
	opt.IncludeCooldowns = q.Get("cooldowns") == "1"
	opt.CraftReagents = q.Get("noCraft") != "1"
	opt.Exclude = map[int]bool{}
	for _, f := range strings.Split(q.Get("exclude"), ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			opt.Exclude[n] = true
		}
	}
	opt.Known = map[int]bool{}
	for _, f := range strings.Split(q.Get("known"), ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			opt.Known[n] = true
		}
	}
	adv := advisor.New(db, snap, p, opt)
	return map[string]any{
		"profession":     p.Name,
		"prices":         PriceInfo(snap),
		"recommendation": adv.Recommend(skill),
		"plan":           adv.Plan(skill, target),
		"generated":      time.Now().UTC(),
	}, nil
}

func atoi(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return d
}

func clamp(n, lo, hi int) int { return max(lo, min(n, hi)) }
