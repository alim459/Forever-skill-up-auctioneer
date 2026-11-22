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
	"path/filepath"
	"time"

	"github.com/alim459/forever-skill-up-auctioneer/internal/app"
	"github.com/alim459/forever-skill-up-auctioneer/internal/service"
)

//go:embed static
var static embed.FS

// Static is the UI, also used to build the hosted site.
func Static() fs.FS {
	sub, _ := fs.Sub(static, "static")
	return sub
}

// Handler returns the HTTP handler.
func Handler(a *app.App) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(Static()))
	// Item tooltips, if cached with the "items" command.
	mux.HandleFunc("GET /data/items.json", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(a.Cfg.DataDir, "items.json"))
	})

	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		db, err := a.Recipes(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		snap, perr := a.Prices(r.Context())
		writeJSON(w, service.Status(db, snap, perr))
	})

	mux.HandleFunc("POST /api/refresh", func(w http.ResponseWriter, r *http.Request) {
		snap, err := a.RefreshPrices(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, service.PriceInfo(snap))
	})

	mux.HandleFunc("GET /api/recommend", func(w http.ResponseWriter, r *http.Request) {
		db, err := a.Recipes(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		snap, err := a.Prices(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		out, err := service.Recommend(db, snap, r.URL.Query())
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, out)
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
