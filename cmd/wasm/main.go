//go:build js && wasm

// Command wasm exposes the advisor to the browser for the hosted (static)
// site. JavaScript loads the recipe dataset and a price snapshot, then calls:
//
//	fsaLoadRecipes(text)    -> "" or error message
//	fsaLoadPrices(jsonText) -> "" or error message
//	fsaStatus()             -> JSON
//	fsaRecommend(query)     -> JSON (same as /api/recommend)
package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"syscall/js"

	"github.com/alim459/forever-skill-up-auctioneer/internal/auction"
	"github.com/alim459/forever-skill-up-auctioneer/internal/recipes"
	"github.com/alim459/forever-skill-up-auctioneer/internal/service"
)

var (
	db   *recipes.DB
	snap *auction.Snapshot
)

func errJSON(err error) string {
	b, _ := json.Marshal(map[string]string{"error": err.Error()})
	return string(b)
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return errJSON(err)
	}
	return string(b)
}

func main() {
	js.Global().Set("fsaLoadRecipes", js.FuncOf(func(_ js.Value, args []js.Value) any {
		d, err := recipes.Parse([]byte(args[0].String()))
		if err != nil {
			return err.Error()
		}
		db = d
		return ""
	}))
	js.Global().Set("fsaLoadLimited", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if db == nil {
			return "recipes not loaded"
		}
		l, err := recipes.ParseLimitedStock([]byte(args[0].String()))
		if err != nil {
			return err.Error()
		}
		db.Limited = l
		return ""
	}))
	js.Global().Set("fsaLoadPrices", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var s auction.Snapshot
		if err := json.Unmarshal([]byte(args[0].String()), &s); err != nil {
			return err.Error()
		}
		snap = &s
		return ""
	}))
	js.Global().Set("fsaStatus", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		if db == nil {
			return errJSON(errors.New("recipes not loaded"))
		}
		return toJSON(service.Status(db, snap, nil))
	}))
	js.Global().Set("fsaRecommend", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if db == nil || snap == nil {
			return errJSON(errors.New("data still loading"))
		}
		q, err := url.ParseQuery(args[0].String())
		if err != nil {
			return errJSON(err)
		}
		out, err := service.Recommend(db, snap, q)
		if err != nil {
			return errJSON(err)
		}
		return toJSON(out)
	}))
	js.Global().Call("dispatchEvent", js.Global().Get("Event").New("fsa-ready"))
	select {}
}
