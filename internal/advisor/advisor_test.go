package advisor

import (
	"math"
	"testing"

	"github.com/alim459/forever-skill-up-auctioneer/internal/auction"
	"github.com/alim459/forever-skill-up-auctioneer/internal/recipes"
)

func fixture(t *testing.T) (*recipes.DB, *auction.Snapshot) {
	t.Helper()
	db, err := recipes.LoadFile("testdata/professions.js")
	if err != nil {
		t.Fatal(err)
	}
	snap := &auction.Snapshot{Items: map[int]auction.Price{
		2589: {Market: 20, MinBuyout: 18, Quantity: 500}, // linen cloth 20c
		2996: {Market: 60, MinBuyout: 55, Quantity: 40},  // bolt 60c (crafting = 40c)
		2569: {Market: 200, MinBuyout: 150, Quantity: 3}, // cloak sells well
		2568: {Market: 30, MinBuyout: 30, Quantity: 10},  // vest sells badly
	}}
	return db, snap
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestChance(t *testing.T) {
	r := &recipes.Recipe{Required: 10, Thresholds: [4]int{10, 45, 57, 70}}
	cases := map[int]float64{5: 0, 10: 1, 44: 1, 45: 1, 57: 13.0 / 25, 69: 1.0 / 25, 70: 0}
	for s, want := range cases {
		if got := r.Chance(s); !near(got, want) {
			t.Errorf("Chance(%d) = %v, want %v", s, got, want)
		}
	}
	if r.Colour(50) != "yellow" || r.Colour(60) != "green" || r.Colour(70) != "grey" || r.Colour(20) != "orange" {
		t.Error("colours wrong")
	}
}

func TestUnitCostPrefersCrafting(t *testing.T) {
	db, snap := fixture(t)
	p, _ := db.FindProfession("tail")
	a := New(db, snap, p, DefaultOptions())
	c := a.UnitCost(2996, 1)
	if c.Source != "craft" || !near(c.Unit, 40) {
		t.Fatalf("bolt cost = %+v, want craft 40c", c)
	}
	if c := a.UnitCost(2320, 1); c.Source != "vendor" || !near(c.Unit, 10) {
		t.Fatalf("thread = %+v, want vendor 10c", c)
	}
	opt := DefaultOptions()
	opt.CraftReagents = false
	if c := New(db, snap, p, opt).UnitCost(2996, 1); c.Source != "ah" || !near(c.Unit, 60) {
		t.Fatalf("no-craft bolt = %+v", c)
	}
}

func TestRecommend(t *testing.T) {
	db, snap := fixture(t)
	p, _ := db.FindProfession("Tailoring")
	a := New(db, snap, p, DefaultOptions())
	rec := a.Recommend(30)

	if len(rec.SkillUps) == 0 || rec.SkillUps[0].Name != "Linen Cloak" {
		t.Fatalf("best skill-up = %+v", rec.SkillUps)
	}
	cloak := rec.SkillUps[0]
	// mats 40+10 = 50, sale 200*0.95 = 190
	if !near(cloak.Cost, 50) || !near(cloak.Sale, 190) || !near(cloak.Profit, 140) || cloak.SaleHow != "ah" {
		t.Fatalf("cloak = %+v", cloak)
	}
	for _, o := range rec.SkillUps {
		if o.Name == "Fancy Drop Robe" {
			t.Fatal("drop recipe should be filtered by default sources")
		}
	}
	if len(rec.Unpriced) != 1 || rec.Unpriced[0].Name != "Mystery Shirt" {
		t.Fatalf("unpriced = %+v", rec.Unpriced)
	}
	// Vest: AH 30*0.95=28.5 < vendor 30, so it is sold to the vendor.
	for _, o := range rec.SkillUps {
		if o.Name == "Brown Linen Vest" && o.SaleHow != "vendor" {
			t.Fatalf("vest sale = %s", o.SaleHow)
		}
	}
}

func TestPlan(t *testing.T) {
	db, snap := fixture(t)
	p, _ := db.FindProfession("Tailoring")
	a := New(db, snap, p, DefaultOptions())
	pl := a.Plan(1, 60)
	if pl.StuckAt != 0 {
		t.Fatalf("stuck at %d", pl.StuckAt)
	}
	if len(pl.Steps) == 0 || pl.Steps[0].From != 1 || pl.Steps[len(pl.Steps)-1].To != 60 {
		t.Fatalf("steps = %+v", pl.Steps)
	}
	for i := 1; i < len(pl.Steps); i++ {
		if pl.Steps[i].From != pl.Steps[i-1].To {
			t.Fatal("gap in plan")
		}
	}
	// 1..34 orange cloak: 34 crafts, each +140c
	if s := pl.Steps[0]; s.Option.Name != "Linen Cloak" || s.Crafts < 34 {
		t.Fatalf("first step %+v", s)
	}
	if pl.Steps[0].Warnings == nil {
		t.Fatal("expected market-depth warning (only 3 cloaks listed)")
	}
	notes := 0
	for _, s := range pl.Steps {
		if s.TrainNote != "" {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("expected one Journeyman note crossing 50, got %d", notes)
	}
	if len(pl.Shopping) == 0 {
		t.Fatal("no shopping list")
	}
	if a.Plan(1, 200).StuckAt == 0 {
		t.Fatal("fixture can't reach 200; plan should report where it got stuck")
	}
}

func TestLearnCost(t *testing.T) {
	db, snap := fixture(t)
	p, _ := db.FindProfession("Tailoring")
	a := New(db, snap, p, DefaultOptions())
	rec := a.Recommend(30)
	var cloak, vest Option
	for _, o := range rec.SkillUps {
		switch o.Name {
		case "Linen Cloak":
			cloak = o
		case "Brown Linen Vest":
			vest = o
		}
	}
	if cloak.LearnHow != "vendor" || cloak.LearnCost != 500 || cloak.TeachName != "Pattern: Linen Cloak" {
		t.Fatalf("cloak learn = %+v", cloak)
	}
	if vest.LearnHow != "trainer" || vest.LearnPriced {
		t.Fatalf("vest learn = %s %v", vest.LearnHow, vest.LearnPriced)
	}
	// Cheaper on the AH than at the vendor.
	snap.Items[9100] = auction.Price{Market: 300, Quantity: 1}
	if o := a.Evaluate(cloak.Recipe, 30); o.LearnHow != "ah" || o.LearnCost != 300 {
		t.Fatalf("ah pattern = %s %v", o.LearnHow, o.LearnCost)
	}
	pl := a.Plan(1, 60)
	if pl.RecipeCost != 300 {
		t.Fatalf("recipe cost = %v", pl.RecipeCost)
	}
	if math.Abs(pl.Net-(pl.Sale-pl.Cost-pl.RecipeCost)) > 1e-6 {
		t.Fatal("net excludes recipe cost")
	}
	n := 0
	for _, r := range pl.Recipes {
		if r.Name == "Linen Cloak" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("cloak listed %d times in plan recipes", n)
	}
	// Already known: free.
	opt := DefaultOptions()
	opt.Known = map[int]bool{2: true}
	if o := New(db, snap, p, opt).Evaluate(cloak.Recipe, 30); o.LearnHow != "known" || o.LearnCost != 0 {
		t.Fatalf("known = %+v", o.LearnHow)
	}
}
