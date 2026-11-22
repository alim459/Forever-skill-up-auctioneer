package auction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Source fetches a price snapshot.
type Source interface {
	Name() string
	Fetch(ctx context.Context) (*Snapshot, error)
}

// Target identifies one WoW: Forever auction house.
//
// Forever has no realms: auction houses hang off a "ruleset". The ids are not
// documented by Blizzard; community findings so far:
//
//	ruleset 2 = Normal; house 114 = US Normal, Horde.
//
// Houses seen in the API: 110–121 and 123–125 (Alliance, Horde, Blackwater).
type Target struct {
	Region  string // us, eu, kr, tw
	Ruleset int
	House   int
}

// Blizzard reads the official Battle.net Game Data API.
//
//	GET https://{region}.api.blizzard.com/data/wow/ruleset/{ruleset}/auctions/{house}?namespace=dynamic-forever-{region}
//	GET .../auctions/{house}/commodities?namespace=dynamic-forever-{region}
type Blizzard struct {
	ClientID     string
	ClientSecret string
	Target       Target
	// RawDir, when set, receives the untouched JSON responses.
	RawDir string

	HTTP     *http.Client
	APIBase  string // default https://{region}.api.blizzard.com
	OAuthURL string // default https://oauth.battle.net/token
}

func (b *Blizzard) Name() string { return "Blizzard Battle.net Game Data API" }

func (b *Blizzard) client() *http.Client {
	if b.HTTP != nil {
		return b.HTTP
	}
	return &http.Client{Timeout: 3 * time.Minute}
}

func (b *Blizzard) token(ctx context.Context) (string, error) {
	u := b.OAuthURL
	if u == "" {
		u = "https://oauth.battle.net/token"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u,
		strings.NewReader(url.Values{"grant_type": {"client_credentials"}}.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(b.ClientID, b.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth: HTTP %d: %s", resp.StatusCode, truncate(body))
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return "", fmt.Errorf("oauth: no access_token in response")
	}
	return t.AccessToken, nil
}

// rawAuctions is the response shape of both auction endpoints.
type rawAuctions struct {
	Auctions []struct {
		ID   int64 `json:"id"`
		Item struct {
			ID int `json:"id"`
		} `json:"item"`
		Quantity  int64  `json:"quantity"`
		UnitPrice int64  `json:"unit_price"`
		Buyout    int64  `json:"buyout"`
		Bid       int64  `json:"bid"`
		TimeLeft  string `json:"time_left"`
	} `json:"auctions"`
}

func (b *Blizzard) get(ctx context.Context, token, path, rawName string) (*rawAuctions, time.Time, error) {
	t := b.Target
	base := b.APIBase
	if base == "" {
		base = fmt.Sprintf("https://%s.api.blizzard.com", t.Region)
	}
	u := fmt.Sprintf("%s%s?namespace=dynamic-forever-%s&locale=en_US", base, path, t.Region)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := b.client().Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, time.Time{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, time.Time{}, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, truncate(body))
	}
	if b.RawDir != "" {
		if err := os.MkdirAll(b.RawDir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(b.RawDir, rawName), body, 0o644)
		}
	}
	var ra rawAuctions
	if err := json.Unmarshal(body, &ra); err != nil {
		return nil, time.Time{}, fmt.Errorf("GET %s: bad JSON: %w", path, err)
	}
	lm, _ := http.ParseTime(resp.Header.Get("Last-Modified"))
	return &ra, lm, nil
}

var errNotFound = errors.New("not found")

func (b *Blizzard) Fetch(ctx context.Context) (*Snapshot, error) {
	if b.ClientID == "" || b.ClientSecret == "" {
		return nil, errors.New("blizzard: BLIZZARD_CLIENT_ID and BLIZZARD_CLIENT_SECRET are required")
	}
	tok, err := b.token(ctx)
	if err != nil {
		return nil, err
	}
	t := b.Target
	path := fmt.Sprintf("/data/wow/ruleset/%d/auctions/%d", t.Ruleset, t.House)
	tag := fmt.Sprintf("%s-%d", t.Region, t.House)

	house, lm1, err := b.get(ctx, tok, path, "auctions-"+tag+".json")
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("blizzard: auction house %d on ruleset %d (%s) not found", t.House, t.Ruleset, t.Region)
		}
		return nil, fmt.Errorf("blizzard: %w", err)
	}
	comm, lm2, err := b.get(ctx, tok, path+"/commodities", "commodities-"+tag+".json")
	if err != nil && !errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("blizzard: %w", err)
	}

	units := map[int][]unit{}
	add := func(ra *rawAuctions) {
		if ra == nil {
			return
		}
		for _, a := range ra.Auctions {
			q := a.Quantity
			if q <= 0 {
				q = 1
			}
			var p float64
			switch {
			case a.UnitPrice > 0:
				p = float64(a.UnitPrice)
			case a.Buyout > 0:
				p = float64(a.Buyout) / float64(q)
			default:
				continue // bid-only listing: no buyout price to compare
			}
			units[a.Item.ID] = append(units[a.Item.ID], unit{p, q})
		}
	}
	add(house)
	add(comm)

	updated := lm1
	if lm2.After(updated) {
		updated = lm2
	}
	now := time.Now().UTC()
	if updated.IsZero() {
		updated = now
	}
	return &Snapshot{
		Source: b.Name(), Region: t.Region, Ruleset: t.Ruleset, House: t.House,
		Updated: updated.UTC(), Fetched: now, Items: aggregate(units),
	}, nil
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
