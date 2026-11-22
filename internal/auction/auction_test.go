package auction

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAggregateMarket(t *testing.T) {
	// 20 units: 3 cheapest (15%) are 10,10,20 -> 13.33
	u := map[int][]unit{1: {{50, 10}, {10, 2}, {20, 8}}}
	p := aggregate(u)[1]
	if p.MinBuyout != 10 || p.Quantity != 20 || p.Market != 13 {
		t.Fatalf("%+v", p)
	}
	// fewer than 14 units: market = cheapest listing
	p = aggregate(map[int][]unit{2: {{30, 1}, {25, 2}}})[2]
	if p.Market != 25 {
		t.Fatalf("%+v", p)
	}
}

func TestBlizzardFetch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		id, sec, ok := r.BasicAuth()
		if !ok || id != "id" || sec != "secret" || r.FormValue("grant_type") != "client_credentials" {
			http.Error(w, "bad auth", 401)
			return
		}
		w.Write([]byte(`{"access_token":"tok","token_type":"bearer","expires_in":86399}`))
	})
	check := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("namespace") != "dynamic-forever-us" {
			http.Error(w, "nope", 403)
			return false
		}
		w.Header().Set("Last-Modified", "Sun, 22 Nov 2026 17:00:00 GMT")
		return true
	}
	mux.HandleFunc("GET /data/wow/ruleset/2/auctions/114", func(w http.ResponseWriter, r *http.Request) {
		if check(w, r) {
			w.Write([]byte(`{"auctions":[
				{"id":1,"item":{"id":2569},"buyout":400,"quantity":2,"time_left":"LONG"},
				{"id":2,"item":{"id":2569},"bid":100,"quantity":1,"time_left":"SHORT"},
				{"id":3,"item":{"id":9000},"buyout":5000,"quantity":1,"time_left":"LONG"}]}`))
		}
	})
	mux.HandleFunc("GET /data/wow/ruleset/2/auctions/114/commodities", func(w http.ResponseWriter, r *http.Request) {
		if check(w, r) {
			w.Write([]byte(`{"auctions":[
				{"id":4,"item":{"id":2589},"unit_price":20,"quantity":100,"time_left":"LONG"},
				{"id":5,"item":{"id":2589},"unit_price":15,"quantity":5,"time_left":"LONG"}]}`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	raw := t.TempDir()
	b := &Blizzard{ClientID: "id", ClientSecret: "secret", Target: Target{"us", 2, 114},
		RawDir: raw, APIBase: srv.URL, OAuthURL: srv.URL + "/token"}
	snap, err := b.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p := snap.Items[2569]; p.MinBuyout != 200 || p.Quantity != 2 {
		t.Fatalf("cloak %+v", p)
	}
	if p := snap.Items[2589]; p.MinBuyout != 15 || p.Quantity != 105 {
		t.Fatalf("linen %+v", p)
	}
	if snap.Updated.Hour() != 17 {
		t.Fatalf("updated %v", snap.Updated)
	}
	for _, f := range []string{"auctions-us-114.json", "commodities-us-114.json"} {
		if _, err := os.Stat(filepath.Join(raw, f)); err != nil {
			t.Fatalf("raw json not saved: %v", err)
		}
	}
	// round trip
	path := filepath.Join(t.TempDir(), "p.json")
	if err := snap.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil || back.Items[2589].Quantity != 105 {
		t.Fatalf("load: %v %+v", err, back)
	}
}

func TestGoblinFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/m/us-114/prices" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"updated":1795000000,"items":[[2589,22,18,500,0.1,null,140.5],[2569,200,150,3,0,null]]}`))
	}))
	defer srv.Close()
	g := &Goblin{Target: Target{"us", 2, 114}, Base: srv.URL}
	snap, err := g.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := snap.Items[2589]
	if p.Market != 22 || p.MinBuyout != 18 || p.Quantity != 500 || p.SoldPerDay == nil || *p.SoldPerDay != 140.5 {
		t.Fatalf("%+v", p)
	}
	if snap.Items[2569].SoldPerDay != nil {
		t.Fatal("missing soldPerDay should be nil")
	}
}
