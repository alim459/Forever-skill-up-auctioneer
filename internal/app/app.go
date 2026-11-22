// Package app wires prices, recipes and the advisor together for the CLI and
// the web UI.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/alim459/forever-skill-up-auctioneer/internal/auction"
	"github.com/alim459/forever-skill-up-auctioneer/internal/recipes"
)

// Config is shared by all commands.
type Config struct {
	DataDir      string
	Target       auction.Target
	Source       string // auto, blizzard, goblin
	ClientID     string
	ClientSecret string
	RecipesURL   string
	RecipesFile  string        // explicit dataset path; overrides the cache
	MaxAge       time.Duration // re-fetch prices older than this (0 = never automatically)
}

// App caches loaded data.
type App struct {
	Cfg Config

	mu     sync.Mutex
	db     *recipes.DB
	prices *auction.Snapshot
}

func New(cfg Config) *App { return &App{Cfg: cfg} }

func (a *App) recipesPath() string {
	if a.Cfg.RecipesFile != "" {
		return a.Cfg.RecipesFile
	}
	return filepath.Join(a.Cfg.DataDir, "professions-data.js")
}

func (a *App) pricesPath() string {
	return filepath.Join(a.Cfg.DataDir, auction.FileName(a.Cfg.Target.Region, a.Cfg.Target.House))
}

// Recipes returns the recipe DB, downloading it on first use.
func (a *App) Recipes(ctx context.Context) (*recipes.DB, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.db != nil {
		return a.db, nil
	}
	db, err := recipes.LoadFile(a.recipesPath())
	if errors.Is(err, fs.ErrNotExist) && a.Cfg.RecipesFile == "" {
		fmt.Fprintln(os.Stderr, "Downloading recipe data…")
		db, err = recipes.Download(ctx, a.Cfg.RecipesURL, a.recipesPath())
	}
	if err != nil {
		return nil, err
	}
	if l, err := recipes.LoadLimitedStock(filepath.Join(a.Cfg.DataDir, "limited-stock.json")); err == nil {
		db.Limited = l
	}
	a.db = db
	return db, nil
}

// UpdateRecipes forces a fresh download of the recipe data.
func (a *App) UpdateRecipes(ctx context.Context) (*recipes.DB, error) {
	db, err := recipes.Download(ctx, a.Cfg.RecipesURL, filepath.Join(a.Cfg.DataDir, "professions-data.js"))
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.db = db
	a.mu.Unlock()
	return db, nil
}

func (a *App) sources() ([]auction.Source, error) {
	raw := filepath.Join(a.Cfg.DataDir, "raw")
	bl := &auction.Blizzard{ClientID: a.Cfg.ClientID, ClientSecret: a.Cfg.ClientSecret, Target: a.Cfg.Target, RawDir: raw}
	gb := &auction.Goblin{Target: a.Cfg.Target, RawDir: raw}
	switch a.Cfg.Source {
	case "blizzard":
		return []auction.Source{bl}, nil
	case "goblin":
		return []auction.Source{gb}, nil
	case "", "auto":
		if a.Cfg.ClientID != "" && a.Cfg.ClientSecret != "" {
			return []auction.Source{bl, gb}, nil
		}
		return []auction.Source{gb}, nil
	}
	return nil, fmt.Errorf("unknown price source %q (auto, blizzard, goblin)", a.Cfg.Source)
}

// RefreshPrices downloads a new snapshot and saves it.
func (a *App) RefreshPrices(ctx context.Context) (*auction.Snapshot, error) {
	srcs, err := a.sources()
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, s := range srcs {
		snap, err := s.Fetch(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(snap.Items) == 0 {
			errs = append(errs, fmt.Errorf("%s: no auctions returned", s.Name()))
			continue
		}
		if err := snap.Save(a.pricesPath()); err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.prices = snap
		a.mu.Unlock()
		return snap, nil
	}
	return nil, errors.Join(errs...)
}

// Prices returns the current snapshot, fetching one if none is saved or the
// saved one is older than MaxAge. If a refresh fails but an older snapshot
// exists, the old one is returned along with a warning on stderr.
func (a *App) Prices(ctx context.Context) (*auction.Snapshot, error) {
	a.mu.Lock()
	snap := a.prices
	a.mu.Unlock()
	if snap == nil {
		if s, err := auction.Load(a.pricesPath()); err == nil {
			snap = s
		}
	}
	stale := snap == nil || (a.Cfg.MaxAge > 0 && time.Since(snap.Fetched) > a.Cfg.MaxAge)
	if stale {
		fresh, err := a.RefreshPrices(ctx)
		if err == nil {
			return fresh, nil
		}
		if snap == nil {
			return nil, fmt.Errorf("no auction data yet and fetching failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "warning: price refresh failed, using snapshot from %s: %v\n",
			snap.Fetched.Local().Format(time.RFC822), err)
	}
	a.mu.Lock()
	a.prices = snap
	a.mu.Unlock()
	return snap, nil
}
