// Command forever-skill-up-auctioneer recommends WoW: Forever crafts that
// level a profession cheaply, or profitably, using live auction house data.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/alim459/forever-skill-up-auctioneer/internal/advisor"
	"github.com/alim459/forever-skill-up-auctioneer/internal/app"
	"github.com/alim459/forever-skill-up-auctioneer/internal/auction"
	"github.com/alim459/forever-skill-up-auctioneer/internal/money"
	"github.com/alim459/forever-skill-up-auctioneer/internal/recipes"
	"github.com/alim459/forever-skill-up-auctioneer/internal/web"
)

const usage = `forever-skill-up-auctioneer — WoW: Forever profession leveling + AH profit advisor

Usage:
  forever-skill-up-auctioneer <command> [flags]

Commands:
  serve        Start the web UI (default http://localhost:8080)
  recommend    Show the best crafts for a profession and skill level
  prices       Fetch a fresh auction house snapshot (saves the live JSON)
  recipes      Re-download the recipe data
  professions  List professions in the recipe data

Examples:
  forever-skill-up-auctioneer prices
  forever-skill-up-auctioneer recommend -prof tailoring -skill 75 -target 150
  forever-skill-up-auctioneer serve

Prices come from Blizzard's API when BLIZZARD_CLIENT_ID and
BLIZZARD_CLIENT_SECRET are set (free at https://develop.battle.net/access/clients),
otherwise from Forever Goblin. Run "<command> -h" for flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = runServe(ctx, args)
	case "recommend", "rec":
		err = runRecommend(ctx, args)
	case "prices", "fetch":
		err = runPrices(ctx, args)
	case "recipes":
		err = runRecipes(ctx, args)
	case "history":
		err = runHistory(ctx, args)
	case "items":
		err = runItems(ctx, args)
	case "api":
		err = runAPI(ctx, args)
	case "professions":
		err = runProfessions(ctx, args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func commonFlags(fs *flag.FlagSet) func() app.Config {
	data := fs.String("data", envOr("FOREVER_DATA_DIR", "data"), "directory for cached recipes and auction JSON")
	region := fs.String("region", envOr("FOREVER_REGION", "us"), "region: us, eu, kr, tw")
	ruleset := fs.Int("ruleset", envInt("FOREVER_RULESET", 2), "ruleset id (2 = Normal)")
	house := fs.Int("house", envInt("FOREVER_HOUSE", 114), "auction house id (114 = US Normal Horde)")
	source := fs.String("source", envOr("FOREVER_PRICE_SOURCE", "auto"), "price source: auto, blizzard, goblin")
	recipesFile := fs.String("recipes-file", "", "use a local recipe dataset instead of the downloaded one")
	recipesURL := fs.String("recipes-url", recipes.DefaultURL, "where to download the recipe dataset from")
	maxAge := fs.Duration("max-age", 60*time.Minute, "re-fetch prices older than this (Blizzard updates hourly); 0 = only when missing")
	return func() app.Config {
		return app.Config{
			DataDir:      *data,
			Target:       auction.Target{Region: strings.ToLower(*region), Ruleset: *ruleset, House: *house},
			Source:       *source,
			ClientID:     os.Getenv("BLIZZARD_CLIENT_ID"),
			ClientSecret: os.Getenv("BLIZZARD_CLIENT_SECRET"),
			RecipesURL:   *recipesURL,
			RecipesFile:  *recipesFile,
			MaxAge:       *maxAge,
		}
	}
}

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfg := commonFlags(fs)
	addr := fs.String("addr", "localhost:8080", "listen address")
	fs.Parse(args)
	a := app.New(cfg())
	if _, err := a.Recipes(ctx); err != nil {
		return err
	}
	return web.Serve(ctx, *addr, a)
}

func runPrices(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("prices", flag.ExitOnError)
	cfg := commonFlags(fs)
	fs.Parse(args)
	a := app.New(cfg())
	snap, err := a.RefreshPrices(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Fetched %d priced items from %s (house %d, %s, snapshot %s).\n",
		len(snap.Items), snap.Source, snap.House, strings.ToUpper(snap.Region), snap.Updated.Local().Format(time.RFC822))
	fmt.Printf("Raw JSON saved under %s/raw/, prices under %s/.\n", a.Cfg.DataDir, a.Cfg.DataDir)
	return nil
}

// runHistory appends the saved price snapshot for a house to its history file.
func runHistory(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	cfg := commonFlags(fs)
	out := fs.String("out", "", "history file (default <data>/history-<region>-<house>.json)")
	fs.Parse(args)
	c := cfg()
	if *out == "" {
		*out = filepath.Join(c.DataDir, fmt.Sprintf("history-%s-%d.json", c.Target.Region, c.Target.House))
	}
	a := app.New(c)
	snap, err := auction.Load(filepath.Join(c.DataDir, auction.FileName(c.Target.Region, c.Target.House)))
	if err != nil {
		return fmt.Errorf("no saved prices for house %d (run prices first): %w", c.Target.House, err)
	}
	db, err := a.Recipes(ctx)
	if err != nil {
		return err
	}
	h, err := auction.LoadHistory(*out)
	if err != nil {
		return err
	}
	added := h.Add(snap, trackedItems(db), time.Now())
	if err := h.Save(*out); err != nil {
		return err
	}
	fmt.Printf("History house %d: %d snapshots (new: %v), %d items.\n", c.Target.House, len(h.Times), added, len(h.Items))
	return nil
}

// trackedItems is every crafted item and reagent in the recipe data.
func trackedItems(db *recipes.DB) []int {
	seen := map[int]bool{}
	var ids []int
	for _, p := range db.Professions {
		for _, r := range p.Recipes {
			for _, id := range append([]int{r.CraftedItem}, reagentIDs(r)...) {
				if id > 0 && !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// runItems caches Blizzard item tooltips (stats) for every crafted item and
// reagent in the recipe data.
func runItems(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("items", flag.ExitOnError)
	cfg := commonFlags(fs)
	out := fs.String("out", "", "cache file (default <data>/items.json)")
	workers := fs.Int("workers", 8, "parallel requests")
	limit := fs.Duration("timeout", 8*time.Minute, "stop after this long (progress is kept)")
	fs.Parse(args)
	c := cfg()
	if *out == "" {
		*out = filepath.Join(c.DataDir, "items.json")
	}
	db, err := app.New(c).Recipes(ctx)
	if err != nil {
		return err
	}
	ids := trackedItems(db)
	cache, err := auction.LoadItemCache(*out)
	if err != nil {
		return err
	}
	tctx, cancel := context.WithTimeout(ctx, *limit)
	defer cancel()
	b := &auction.Blizzard{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Target: c.Target}
	got, failed, ferr := b.FetchItems(tctx, cache, ids, *workers, os.Stderr)
	if err := cache.Save(*out); err != nil {
		return err
	}
	fmt.Printf("Item tooltips: %d items known, %d fetched now, %d failed/missing.\n", len(cache), got, failed)
	if ferr != nil && !errors.Is(ferr, context.DeadlineExceeded) {
		return ferr
	}
	return nil
}

func reagentIDs(r *recipes.Recipe) []int {
	var out []int
	for _, g := range r.Reagents {
		out = append(out, g.Item)
	}
	return out
}

// runAPI prints a raw Game Data API response (diagnostics).
func runAPI(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("api", flag.ExitOnError)
	cfg := commonFlags(fs)
	ns := fs.String("namespace", "", "namespace (default dynamic-forever-<region>)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: api [flags] /data/wow/ruleset/index")
	}
	c := cfg()
	b := &auction.Blizzard{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Target: c.Target}
	body, err := b.GetRaw(ctx, fs.Arg(0), *ns)
	if err != nil {
		return err
	}
	os.Stdout.Write(body)
	fmt.Println()
	return nil
}

func runRecipes(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("recipes", flag.ExitOnError)
	cfg := commonFlags(fs)
	fs.Parse(args)
	db, err := app.New(cfg()).UpdateRecipes(ctx)
	if err != nil {
		return err
	}
	n := 0
	for _, p := range db.Professions {
		n += len(p.Recipes)
	}
	fmt.Printf("Recipe data updated: %d recipes, %d professions (client build %s, built %s).\n",
		n, len(db.Professions), db.ClientBuild, db.Built)
	return nil
}

func runProfessions(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("professions", flag.ExitOnError)
	cfg := commonFlags(fs)
	fs.Parse(args)
	db, err := app.New(cfg()).Recipes(ctx)
	if err != nil {
		return err
	}
	for _, n := range db.ProfessionNames() {
		p, _ := db.FindProfession(n)
		fmt.Printf("%-15s %4d recipes\n", p.Name, len(p.Recipes))
	}
	return nil
}

func runRecommend(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("recommend", flag.ExitOnError)
	cfg := commonFlags(fs)
	prof := fs.String("prof", "", "profession name or id (e.g. tailoring)")
	skill := fs.Int("skill", 1, "your current skill")
	target := fs.Int("target", 0, "plan a route up to this skill (default skill+25, max 300)")
	top := fs.Int("top", 10, "rows per table")
	sources := fs.String("sources", "tv?", "recipe sources you can use: t trainer, v vendor, d drop, q quest, c other, ? unknown; 'all' for everything")
	exclude := fs.String("exclude", "", "comma-separated recipe spell ids you don't have")
	known := fs.String("known", "", "comma-separated recipe spell ids you already know (no pattern cost)")
	cooldowns := fs.Bool("cooldowns", false, "include cooldown crafts (transmutes) in skill-up ranking")
	noCraft := fs.Bool("no-craft-reagents", false, "don't price reagents at the cost of crafting them yourself")
	asJSON := fs.Bool("json", false, "print JSON instead of tables")
	fs.Parse(args)
	if *prof == "" {
		return fmt.Errorf("-prof is required")
	}
	if *target <= 0 {
		*target = min(*skill+25, 300)
	}
	a := app.New(cfg())
	db, err := a.Recipes(ctx)
	if err != nil {
		return err
	}
	p, err := db.FindProfession(*prof)
	if err != nil {
		return err
	}
	snap, err := a.Prices(ctx)
	if err != nil {
		return err
	}
	opt := advisor.DefaultOptions()
	opt.Sources = *sources
	if opt.Sources == "all" {
		opt.Sources = ""
	}
	opt.IncludeCooldowns = *cooldowns
	opt.CraftReagents = !*noCraft
	opt.Exclude = parseIDs(*exclude)
	opt.Known = parseIDs(*known)

	adv := advisor.New(db, snap, p, opt)
	rec := adv.Recommend(*skill)
	plan := adv.Plan(*skill, *target)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"prices": map[string]any{"source": snap.Source, "updated": snap.Updated, "house": snap.House},
			"recommendation": rec, "plan": plan})
	}

	fmt.Printf("%s at %d — prices: %s, house %d, snapshot %s\n\n", p.Name, *skill, snap.Source, snap.House,
		snap.Updated.Local().Format("Jan 2 15:04"))

	fmt.Println("BEST SKILL-UPS RIGHT NOW (cheapest per skill point; negative cost = you profit)")
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "Recipe\tColour\tChance\tMats/craft\tSells for\tProfit/craft\tCost/point\tListed\tPattern")
	for i, o := range rec.SkillUps {
		if i >= *top {
			break
		}
		fmt.Fprintf(tw, "%s\t%s\t%.0f%%\t%s\t%s\t%s\t%s\t%s\t%s\n", o.Name, o.Colour, o.Chance*100,
			money.Format(o.Cost), saleStr(o), money.Format(o.Profit), money.Format(o.CostPerPoint), listed(o), learnStr(o))
	}
	tw.Flush()
	if len(rec.SkillUps) == 0 {
		fmt.Println("  (nothing gives a skill-up with the allowed recipe sources)")
	}

	fmt.Println("\nMOST PROFITABLE CRAFTS YOU CAN MAKE (any colour, sold on the AH)")
	tw = tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "Recipe\tColour\tMats/craft\tSells for\tProfit/craft\tListed\tSold/day")
	for i, o := range rec.Profit {
		if i >= *top {
			break
		}
		spd := "?"
		if o.SoldPerDay != nil {
			spd = strconv.FormatFloat(*o.SoldPerDay, 'f', 1, 64)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", o.Name, o.Colour, money.Format(o.Cost), saleStr(o),
			money.Format(o.Profit), o.Listed, spd)
	}
	tw.Flush()
	if len(rec.Profit) == 0 {
		fmt.Println("  (nothing profitable at current prices)")
	}

	fmt.Printf("\nLEVELING PLAN %d → %d\n", plan.From, plan.To)
	tw = tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "Skill\tCraft\t×\tPattern\tMats\tSell back\tNet")
	for _, s := range plan.Steps {
		pat := ""
		if s.LearnCost > 0 {
			pat = money.Format(s.LearnCost)
		}
		fmt.Fprintf(tw, "%d–%d\t%s\t%d\t%s\t%s\t%s\t%s\n", s.From, s.To, s.Option.Name, s.Crafts, pat,
			money.Format(s.Cost), money.Format(s.Sale), money.Format(s.Net))
		if s.TrainNote != "" {
			fmt.Fprintf(tw, "\t  ↳ %s\t\t\t\t\n", s.TrainNote)
		}
		for _, w := range s.Warnings {
			fmt.Fprintf(tw, "\t  ⚠ %s\t\t\t\t\n", w)
		}
	}
	tw.Flush()
	if plan.StuckAt > 0 {
		fmt.Printf("  Stopped at %d: no priced recipe gives a skill-up there with your sources (try -sources all).\n", plan.StuckAt)
	}
	fmt.Printf("Total: patterns %s, materials %s, sell-back %s, net %s\n", money.Format(plan.RecipeCost),
		money.Format(plan.Cost), money.Format(plan.Sale), money.Format(plan.Net))

	if len(plan.Recipes) > 0 {
		fmt.Println("\nRECIPES THIS PLAN USES (pass -known with ids you already have)")
		tw = tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		for _, o := range plan.Recipes {
			where := strings.Join(o.SourceNames, ", ")
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", o.SpellID, o.Name, learnStr(o), where)
		}
		tw.Flush()
	}

	if len(plan.Shopping) > 0 {
		fmt.Println("\nSHOPPING LIST")
		tw = tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		for _, l := range plan.Shopping {
			fmt.Fprintf(tw, "%d×\t%s\t%s each\t(%s)\t%s\n", l.Count, l.Name, money.Format(l.Unit), l.Source, money.Format(l.Total))
		}
		tw.Flush()
	}
	if len(rec.Unpriced) > 0 {
		fmt.Printf("\n%d skill-up recipes were skipped because a reagent has no AH or vendor price.\n", len(rec.Unpriced))
	}
	return nil
}

func saleStr(o advisor.Option) string {
	switch o.SaleHow {
	case "ah":
		return money.Format(o.Sale)
	case "vendor":
		return money.Format(o.Sale) + " (vendor)"
	}
	return "—"
}

func learnStr(o advisor.Option) string {
	switch o.LearnHow {
	case "known":
		return "known"
	case "vendor", "ah":
		return money.Format(o.LearnCost) + " (" + o.LearnHow + ")"
	case "trainer":
		return "trainer (fee not in data)"
	}
	return "unknown source"
}

func listed(o advisor.Option) string {
	if o.Item == 0 {
		return "—"
	}
	return strconv.FormatInt(o.Listed, 10)
}

func parseIDs(s string) map[int]bool {
	out := map[int]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if n, err := strconv.Atoi(f); err == nil {
			out[n] = true
		}
	}
	return out
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return n
	}
	return d
}
