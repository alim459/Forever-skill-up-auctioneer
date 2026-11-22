// Package web serves a small local UI and JSON API.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alim459/forever-skill-up-auctioneer/internal/advisor"
	"github.com/alim459/forever-skill-up-auctioneer/internal/app"
)

//go:embed static
var static embed.FS

// Handler returns the HTTP handler.
func Handler(a *app.App) http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServerFS(sub))

	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		db, err := a.Recipes(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		type prof struct {
			ID      int    `json:"id"`
			Name    string `json:"name"`
			Recipes int    `json:"recipes"`
		}
		var profs []prof
		for _, p := range db.Professions {
			profs = append(profs, prof{p.ID, p.Name, len(p.Recipes)})
		}
		sort.Slice(profs, func(i, j int) bool { return profs[i].Name < profs[j].Name })
		out := map[string]any{
			"recipes":     map[string]any{"built": db.Built, "clientBuild": db.ClientBuild},
			"professions": profs,
			"target":      a.Cfg.Target,
		}
		if snap, err := a.Prices(r.Context()); err == nil {
			out["prices"] = priceInfo(snap.Source, snap.Updated, snap.Fetched, len(snap.Items))
		} else {
			out["pricesError"] = err.Error()
		}
		writeJSON(w, out)
	})

	mux.HandleFunc("POST /api/refresh", func(w http.ResponseWriter, r *http.Request) {
		snap, err := a.RefreshPrices(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, priceInfo(snap.Source, snap.Updated, snap.Fetched, len(snap.Items)))
	})

	mux.HandleFunc("GET /api/recommend", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		db, err := a.Recipes(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		p, err := db.FindProfession(q.Get("prof"))
		if err != nil {
			fail(w, err)
			return
		}
		skill := clamp(atoi(q.Get("skill"), 1), 1, 300)
		target := clamp(atoi(q.Get("target"), skill+25), skill, 300)
		snap, err := a.Prices(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
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
		adv := advisor.New(db, snap, p, opt)
		writeJSON(w, map[string]any{
			"profession":     p.Name,
			"prices":         priceInfo(snap.Source, snap.Updated, snap.Fetched, len(snap.Items)),
			"recommendation": adv.Recommend(skill),
			"plan":           adv.Plan(skill, target),
		})
	})
	return mux
}

// Serve runs the UI until ctx is cancelled.
func Serve(ctx context.Context, addr string, a *app.App) error {
	srv := &http.Server{Addr: addr, Handler: Handler(a), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	fmt.Printf("Forever Skill-Up Auctioneer running at http://%s\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func priceInfo(src string, updated, fetched time.Time, n int) map[string]any {
	return map[string]any{"source": src, "updated": updated, "fetched": fetched, "items": n}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func fail(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func atoi(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return d
}

func clamp(n, lo, hi int) int { return max(lo, min(n, hi)) }
