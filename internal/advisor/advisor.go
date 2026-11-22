// Package advisor ranks recipes by how cheaply (or profitably) they level a
// profession, using live auction prices.
package advisor

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/alim459/forever-skill-up-auctioneer/internal/auction"
	"github.com/alim459/forever-skill-up-auctioneer/internal/recipes"
)

// AHCut is the share of the sale price the auction house keeps.
const AHCut = 0.05

// Options control which recipes are considered.
type Options struct {
	// Sources lists allowed source letters (t trainer, v vendor, d drop,
	// q quest, c/a other) plus "?" for recipes with no source data.
	// Empty means everything.
	Sources string
	// Exclude lists recipe spell ids the player doesn't have.
	Exclude map[int]bool
	// IncludeCooldowns keeps transmutes and other cooldown crafts in the
	// skill-up ranking and plan (they can't be spammed).
	IncludeCooldowns bool
	// CraftReagents allows pricing an intermediate reagent (e.g. a bolt of
	// cloth) at the cost of crafting it yourself when that is cheaper.
	CraftReagents bool
	// Known lists recipe spell ids the player already has (no learning cost).
	Known map[int]bool
	// MinChance is the skill-up chance below which the plan only uses a
	// recipe when nothing better is available (avoids "craft 25 for 1 point").
	MinChance float64
}

// DefaultOptions: trainer, vendor and unknown-source recipes; craft
// intermediates when cheaper.
func DefaultOptions() Options {
	return Options{Sources: "tv?", CraftReagents: true, MinChance: 0.3}
}

// Advisor evaluates recipes for one profession against one price snapshot.
type Advisor struct {
	DB     *recipes.DB
	Prices *auction.Snapshot
	Prof   *recipes.Profession
	Opt    Options

	byItem map[int][]*recipes.Recipe
}

// New builds an advisor.
func New(db *recipes.DB, prices *auction.Snapshot, prof *recipes.Profession, opt Options) *Advisor {
	a := &Advisor{DB: db, Prices: prices, Prof: prof, Opt: opt, byItem: map[int][]*recipes.Recipe{}}
	for _, r := range prof.Recipes {
		if r.CraftedItem > 0 {
			a.byItem[r.CraftedItem] = append(a.byItem[r.CraftedItem], r)
		}
	}
	return a
}

// Allowed reports whether options permit a recipe at all.
func (a *Advisor) Allowed(r *recipes.Recipe) bool {
	if a.Opt.Exclude[r.SpellID] {
		return false
	}
	if a.Opt.Sources == "" {
		return true
	}
	if r.Sources == "" {
		return strings.Contains(a.Opt.Sources, "?")
	}
	for _, c := range r.Sources {
		if strings.ContainsRune(a.Opt.Sources, c) {
			return true
		}
	}
	return false
}

// ItemCost is how a single unit of an item would be obtained.
type ItemCost struct {
	Unit   float64 `json:"unit"`   // copper per item
	Source string  `json:"source"` // "ah", "vendor", "craft"
	OK     bool    `json:"ok"`
}

// UnitCost returns the cheapest way to get one of an item at a skill level.
func (a *Advisor) UnitCost(item, skill int) ItemCost {
	return a.unitCost(item, skill, map[int]bool{})
}

func (a *Advisor) unitCost(item, skill int, visiting map[int]bool) ItemCost {
	best := ItemCost{Unit: math.Inf(1)}
	if p, ok := a.Prices.Lookup(item); ok {
		best = ItemCost{Unit: float64(p.Market), Source: "ah", OK: true}
	}
	if v, ok := a.DB.Item(item).VendorUnitPrice(); ok && v < best.Unit {
		best = ItemCost{Unit: v, Source: "vendor", OK: true}
	}
	if a.Opt.CraftReagents && !visiting[item] && len(visiting) < 3 {
		visiting[item] = true
		for _, r := range a.byItem[item] {
			if skill < r.Required || !a.Allowed(r) || r.CooldownHours > 0 {
				continue
			}
			total, ok := 0.0, true
			for _, g := range r.Reagents {
				c := a.unitCost(g.Item, skill, visiting)
				if !c.OK {
					ok = false
					break
				}
				total += c.Unit * float64(g.Count)
			}
			if ok {
				if u := total / r.AvgYield(); u < best.Unit {
					best = ItemCost{Unit: u, Source: "craft", OK: true}
				}
			}
		}
		delete(visiting, item)
	}
	if !best.OK {
		return ItemCost{}
	}
	return best
}

// ReagentLine is one reagent of an evaluated recipe.
type ReagentLine struct {
	Item   int     `json:"item"`
	Name   string  `json:"name"`
	Count  int     `json:"count"`
	Unit   float64 `json:"unit"`
	Total  float64 `json:"total"`
	Source string  `json:"source"`
	Priced bool    `json:"priced"`
}

// Option is one recipe evaluated at a skill level.
type Option struct {
	Recipe   *recipes.Recipe `json:"-"`
	SpellID  int             `json:"spellId"`
	Name     string          `json:"name"`
	Item     int             `json:"item"`
	Colour   string          `json:"colour"`
	Chance   float64         `json:"chance"`
	Required int             `json:"required"`
	Sources  string          `json:"sources"`

	Reagents []ReagentLine `json:"reagents"`
	Cost     float64       `json:"cost"`    // materials per craft
	Sale     float64       `json:"sale"`    // net proceeds per craft
	SaleHow  string        `json:"saleHow"` // "ah", "vendor", "none"
	Profit   float64       `json:"profit"`  // Sale − Cost per craft
	Priced   bool          `json:"priced"`  // every reagent has a price

	PointsPerCraft float64 `json:"pointsPerCraft"`
	CostPerPoint   float64 `json:"costPerPoint"` // net copper spent per skill point (negative = you earn); 0 when no skill-up
	// Score orders skill-up options (lower is better). Losing crafts score
	// their cost per point; profitable crafts score −profit per craft, so a
	// low chance doesn't inflate their appeal and they always beat losers.
	Score float64 `json:"score"`

	Listed int64 `json:"listed"` // units of the product on the AH

	// Learning the recipe (one-off). LearnHow: "known", "vendor", "ah"
	// (pattern bought on the AH), "trainer" or "unknown" (no price in data).
	LearnCost   float64  `json:"learnCost"`
	LearnHow    string   `json:"learnHow"`
	LearnPriced bool     `json:"learnPriced"`
	TeachItem   int      `json:"teachItem,omitempty"`
	TeachName   string   `json:"teachName,omitempty"`
	SourceNames []string `json:"sourceNames,omitempty"` // e.g. vendor or trainer NPCs
	// PatternStock is set when the vendor sells the pattern in limited
	// quantity; PatternAH/PatternAHListed give its AH price and supply.
	PatternStock    *recipes.Stock `json:"patternStock,omitempty"`
	PatternAH       float64        `json:"patternAH,omitempty"`
	PatternAHListed int64          `json:"patternAHListed,omitempty"`
	SoldPerDay      *float64       `json:"soldPerDay,omitempty"` // when the source knows it
	AHUnit          float64        `json:"ahUnit"`               // AH market price of the product
}

// Evaluate prices one recipe at a skill level.
func (a *Advisor) Evaluate(r *recipes.Recipe, skill int) Option {
	o := Option{
		Recipe: r, SpellID: r.SpellID, Name: r.Name, Item: r.CraftedItem,
		Colour: r.Colour(skill), Chance: r.Chance(skill), Required: r.Required,
		Sources: recipes.SourceLabel(r.Sources), Priced: true,
	}
	for _, g := range r.Reagents {
		c := a.UnitCost(g.Item, skill)
		line := ReagentLine{Item: g.Item, Name: a.DB.ItemName(g.Item), Count: g.Count,
			Unit: c.Unit, Total: c.Unit * float64(g.Count), Source: c.Source, Priced: c.OK}
		if !c.OK {
			o.Priced = false
			line.Unit, line.Total = 0, 0
		}
		o.Cost += line.Total
		o.Reagents = append(o.Reagents, line)
	}

	o.SaleHow = "none"
	if r.CraftedItem > 0 {
		y := r.AvgYield()
		var ah, vendor float64
		if p, ok := a.Prices.Lookup(r.CraftedItem); ok {
			o.AHUnit = float64(p.Market)
			o.Listed = p.Quantity
			o.SoldPerDay = p.SoldPerDay
			ah = float64(p.Market) * (1 - AHCut) * y
		}
		if it := a.DB.Item(r.CraftedItem); it != nil {
			vendor = float64(it.VendorSell) * y
		}
		switch {
		case ah > 0 && ah >= vendor:
			o.Sale, o.SaleHow = ah, "ah"
		case vendor > 0:
			o.Sale, o.SaleHow = vendor, "vendor"
		}
	}
	o.Profit = o.Sale - o.Cost
	a.learnCost(r, &o)
	o.PointsPerCraft = o.Chance * float64(r.SkillUps)
	if o.PointsPerCraft > 0 {
		o.CostPerPoint = -o.Profit / o.PointsPerCraft
		if o.Profit >= 0 {
			o.Score = -o.Profit
		} else {
			o.Score = o.CostPerPoint
		}
	}
	return o
}

// learnCost prices learning a recipe: the cheaper of the pattern's vendor
// price (when a vendor sells it) and its AH price. Trainer fees aren't in the
// data, so trainer-only recipes are marked unpriced.
func (a *Advisor) learnCost(r *recipes.Recipe, o *Option) {
	o.TeachItem = r.TeachItem
	o.SourceNames = r.SourceNames
	if r.TeachItem > 0 {
		o.TeachName = a.DB.ItemName(r.TeachItem)
	}
	if r.TeachItem > 0 {
		if st, ok := a.DB.Limited[r.TeachItem]; ok && strings.ContainsRune(r.Sources, 'v') {
			o.PatternStock = &st
		}
		if p, ok := a.Prices.Lookup(r.TeachItem); ok {
			o.PatternAH, o.PatternAHListed = float64(p.Market), p.Quantity
		}
	}
	if a.Opt.Known[r.SpellID] {
		o.LearnHow, o.LearnPriced = "known", true
		return
	}
	best, how := math.Inf(1), ""
	if r.TeachItem > 0 {
		if it := a.DB.Item(r.TeachItem); it != nil && strings.ContainsRune(r.Sources, 'v') && it.VendorBuy > 0 {
			best, how = float64(it.VendorBuy), "vendor"
		}
		if p, ok := a.Prices.Lookup(r.TeachItem); ok && float64(p.Market) < best {
			best, how = float64(p.Market), "ah"
		}
	}
	switch {
	case how != "":
		o.LearnCost, o.LearnHow, o.LearnPriced = best, how, true
	case strings.ContainsRune(r.Sources, 't'):
		o.LearnHow = "trainer"
	default:
		o.LearnHow = "unknown"
	}
}

// Recommendation is the answer for one skill level.
type Recommendation struct {
	Skill    int      `json:"skill"`
	SkillUps []Option `json:"skillUps"` // ranked by cost per skill point
	Profit   []Option `json:"profit"`   // learnable recipes that make money, ranked by profit
	Unpriced []Option `json:"unpriced"` // skill-up recipes with a reagent that has no price
}

// Recommend ranks every learnable recipe at a skill level.
func (a *Advisor) Recommend(skill int) Recommendation {
	rec := Recommendation{Skill: skill}
	for _, r := range a.Prof.Recipes {
		if skill < r.Required || !a.Allowed(r) {
			continue
		}
		o := a.Evaluate(r, skill)
		if !o.Priced {
			if o.Chance > 0 {
				rec.Unpriced = append(rec.Unpriced, o)
			}
			continue
		}
		if o.Chance > 0 && (r.CooldownHours == 0 || a.Opt.IncludeCooldowns) {
			rec.SkillUps = append(rec.SkillUps, o)
		}
		if o.Profit > 0 && o.SaleHow == "ah" {
			rec.Profit = append(rec.Profit, o)
		}
	}
	sort.SliceStable(rec.SkillUps, func(i, j int) bool {
		x, y := rec.SkillUps[i], rec.SkillUps[j]
		if x.Score != y.Score {
			return x.Score < y.Score
		}
		return x.Chance > y.Chance
	})
	sort.SliceStable(rec.Profit, func(i, j int) bool { return rec.Profit[i].Profit > rec.Profit[j].Profit })
	return rec
}

// PlanStep is a run of skill points crafted with one recipe.
type PlanStep struct {
	From      int      `json:"from"`
	To        int      `json:"to"`
	Option    Option   `json:"option"` // evaluated at From
	Crafts    int      `json:"crafts"` // expected crafts, rounded up
	Cost      float64  `json:"cost"`
	Sale      float64  `json:"sale"`
	Net       float64  `json:"net"`       // Sale − Cost − LearnCost (positive = profit)
	LearnCost float64  `json:"learnCost"` // recipe cost, charged on the recipe's first step only
	Warnings  []string `json:"warnings,omitempty"`
	TrainNote string   `json:"trainNote,omitempty"`
}

// Plan is a leveling route between two skill levels.
type Plan struct {
	From       int           `json:"from"`
	To         int           `json:"to"`
	Steps      []PlanStep    `json:"steps"`
	Cost       float64       `json:"cost"`
	Sale       float64       `json:"sale"`
	RecipeCost float64       `json:"recipeCost"` // patterns to buy (priced ones)
	Net        float64       `json:"net"`        // Sale − Cost − RecipeCost
	Recipes    []Option      `json:"recipes"`    // each recipe the plan uses, once
	Shopping   []ReagentLine `json:"shopping"`
	StuckAt    int           `json:"stuckAt,omitempty"` // first level with no usable recipe
}

// trainerRanks: skill at which the next rank can be trained, and its new cap.
var trainerRanks = []struct {
	at   int
	name string
	cap  int
}{{50, "Journeyman", 150}, {125, "Expert", 225}, {200, "Artisan", 300}}

// Plan picks the cheapest recipe at every skill point from `from` to `to`
// and merges consecutive points that use the same recipe.
func (a *Advisor) Plan(from, to int) Plan {
	pl := Plan{From: from, To: to}
	type acc struct {
		step   PlanStep
		crafts float64
	}
	var cur *acc
	learned := map[int]bool{} // recipes already paid for in this plan
	used := map[int]bool{}    // recipes chosen so far (no more learning cost)
	flush := func() {
		if cur == nil {
			return
		}
		s := cur.step
		s.Crafts = int(math.Ceil(cur.crafts - 1e-9))
		s.Cost = s.Option.Cost * float64(s.Crafts)
		s.Sale = s.Option.Sale * float64(s.Crafts)
		if !learned[s.Option.SpellID] {
			learned[s.Option.SpellID] = true
			if s.Option.LearnPriced {
				s.LearnCost = s.Option.LearnCost
			}
			pl.Recipes = append(pl.Recipes, s.Option)
		}
		s.Net = s.Sale - s.Cost - s.LearnCost
		if s.Option.SaleHow == "ah" && s.Option.Listed > 0 && int64(s.Crafts) > s.Option.Listed {
			s.Warnings = append(s.Warnings, "crafting more than are listed on the AH; selling all of them may be slow")
		}
		if s.Option.SoldPerDay != nil && *s.Option.SoldPerDay > 0 && float64(s.Crafts) > 3**s.Option.SoldPerDay {
			s.Warnings = append(s.Warnings, "more than 3 days of sales volume")
		}
		pl.Steps = append(pl.Steps, s)
		cur = nil
	}
	for skill := from; skill < to; skill++ {
		best := a.bestAt(skill, to, used)
		if best == nil {
			flush()
			pl.StuckAt = skill
			break
		}
		if cur == nil || cur.step.Option.SpellID != best.SpellID {
			flush()
			cur = &acc{step: PlanStep{From: skill, Option: *best}}
		}
		used[best.SpellID] = true
		cur.step.To = skill + 1
		cur.crafts += 1 / best.PointsPerCraft
	}
	flush()

	for i := range pl.Steps {
		s := &pl.Steps[i]
		for _, tr := range trainerRanks {
			if s.From <= tr.at && tr.at < s.To {
				s.TrainNote = fmt.Sprintf("train %s at %d (raises cap to %d)", tr.name, tr.at, tr.cap)
			}
		}
		pl.Cost += s.Cost
		pl.Sale += s.Sale
		pl.RecipeCost += s.LearnCost
	}
	pl.Net = pl.Sale - pl.Cost - pl.RecipeCost

	shop := map[int]*ReagentLine{}
	var order []int
	for _, s := range pl.Steps {
		for _, g := range s.Option.Reagents {
			l := shop[g.Item]
			if l == nil {
				l = &ReagentLine{Item: g.Item, Name: g.Name, Unit: g.Unit, Source: g.Source, Priced: g.Priced}
				shop[g.Item] = l
				order = append(order, g.Item)
			}
			l.Count += g.Count * s.Crafts
			l.Total += g.Total * float64(s.Crafts)
		}
	}
	for _, id := range order {
		pl.Shopping = append(pl.Shopping, *shop[id])
	}
	sort.SliceStable(pl.Shopping, func(i, j int) bool { return pl.Shopping[i].Total > pl.Shopping[j].Total })
	return pl
}

// bestAt picks the recipe to craft at a skill level. Recipes not yet used in
// the plan carry their learning cost spread over the points they can give
// before turning grey (or reaching the target), so an expensive pattern used
// for a handful of crafts loses to a cheap one.
func (a *Advisor) bestAt(skill, to int, used map[int]bool) *Option {
	rec := a.Recommend(skill)
	if len(rec.SkillUps) == 0 {
		return nil
	}
	score := func(o Option) float64 {
		sc := o.Score
		if !used[o.SpellID] && o.LearnPriced && o.LearnCost > 0 {
			pts := min(o.Recipe.Thresholds[3], to) - skill
			if pts < 1 {
				pts = 1
			}
			sc += o.LearnCost / float64(pts)
		}
		return sc
	}
	var best *Option
	pick := func(minChance float64) {
		for i := range rec.SkillUps {
			o := rec.SkillUps[i]
			if o.Chance < minChance {
				continue
			}
			if best == nil || score(o) < score(*best) {
				oo := o
				best = &oo
			}
		}
	}
	pick(a.Opt.MinChance)
	if best == nil {
		pick(0)
	}
	return best
}
