# Forever Skill-Up Auctioneer

A small Go app for **World of Warcraft: Forever** that:

1. **Scans the auction house** by pulling the live auction JSON (Blizzard's hourly snapshot) and saving it.
2. Takes **your profession and skill level**.
3. Recommends what to craft for **skill-ups at the lowest cost, or at a profit**, plus the most profitable crafts you can make right now. It also gives a full **leveling plan** with a shopping list.

It runs as a local web page (`serve`) or from the command line (`recommend`).

## Quick start

Requires Go 1.24+.

```sh
go build -o forever-auctioneer .

# Optional but recommended: official Blizzard data (free client at https://develop.battle.net/access/clients)
export BLIZZARD_CLIENT_ID=...
export BLIZZARD_CLIENT_SECRET=...

./forever-auctioneer serve            # open http://localhost:8080
```

Or from the terminal:

```sh
./forever-auctioneer prices                                          # fetch a fresh AH snapshot
./forever-auctioneer recommend -prof tailoring -skill 75 -target 150
./forever-auctioneer recommend -prof alchemy -skill 200 -json        # machine-readable
```

On Windows, build with `go build -o forever-auctioneer.exe .` and set the variables with `set BLIZZARD_CLIENT_ID=...`.

## Hosted version (GitHub Pages)

The repo includes a workflow (`.github/workflows/pages.yml`) that publishes the app as a free website:

- Every hour it fetches a fresh snapshot for every Forever auction house it can find and publishes them as JSON.
- The advisor is compiled to WebAssembly and runs in your browser, so no server is needed.
- The page has an "Auction house" picker. House 114 is labelled (US Normal Horde); the others show as "house N" until we know which is which.

One-time setup:

1. **Settings → Pages → Build and deployment → Source: "GitHub Actions".**
2. *(Recommended)* **Settings → Secrets and variables → Actions → New repository secret**: add `BLIZZARD_CLIENT_ID` and `BLIZZARD_CLIENT_SECRET` (free at https://develop.battle.net/access/clients). Without them the site uses Forever Goblin.
3. **Actions → "Update AH prices and publish site" → Run workflow** (after that it runs hourly by itself).
4. Open `https://<your-user>.github.io/<repo-name>/`; the deploy job also shows the link.

Optional repository *variables* (same settings page, "Variables" tab): `FOREVER_REGION` (default `us`) and `FOREVER_HOUSES` (space-separated house ids to fetch).

The hourly schedule runs from the repository's default branch.

## Where the data comes from

| Data | Source |
| --- | --- |
| Auction prices (preferred) | Blizzard Battle.net Game Data API, `GET /data/wow/ruleset/{ruleset}/auctions/{house}` and `.../commodities`, namespace `dynamic-forever-{region}`. Needs `BLIZZARD_CLIENT_ID` and `BLIZZARD_CLIENT_SECRET`. |
| Auction prices (fallback, no key) | [Forever Goblin](https://forevergoblin.trade), `GET /api/m/{region}-{house}/prices`, which republishes Blizzard's hourly snapshots. It also gives "sold per day" volume. |
| Recipes, reagents, skill-up colours, vendor prices | The compiled dataset from [OrbitalBoom/Forever-Tracker](https://github.com/OrbitalBoom/Forever-Tracker) (`js/professions-data.js`). It is built from [LibProfessionDB](https://www.curseforge.com/wow/addons/libprofessiondb) (MIT) and wago.tools exports of the live Forever client. It is **downloaded on first run** into `data/`, not bundled here. Run `recipes` to update it. Game data © Blizzard Entertainment. |

`-source auto` (the default) uses Blizzard when credentials are set and falls back to Forever Goblin if Blizzard fails.

Everything is cached in `data/`:

- `data/raw/auctions-us-114.json`, `data/raw/commodities-us-114.json`: the untouched live AH JSON
- `data/prices-us-114.json`: per-item prices (min buyout, market value, quantity listed)
- `data/professions-data.js`: recipe data

Prices are re-fetched automatically when older than `-max-age` (default 60 minutes; Blizzard publishes hourly).

### Choosing your auction house

WoW: Forever has no realms. Auction houses belong to a *ruleset*, and Blizzard hasn't documented the ids. Known so far:

| Flag | Meaning |
| --- | --- |
| `-region us` | `us`, `eu`, `kr`, `tw` |
| `-ruleset 2` | Normal |
| `-house 114` | US Normal, Horde (default) |

The community has seen houses 110–121 and 123–125 in the API, split across Alliance, Horde and Blackwater. If you're on another faction or ruleset, try nearby ids with `prices -house N` and compare against prices you see in game. You can also set `FOREVER_REGION`, `FOREVER_RULESET` and `FOREVER_HOUSE` once.

## How recommendations work

- **Skill-up chance** uses the classic formula: 100% while orange, then `(grey − skill) / (grey − yellow)` through yellow and green, and 0 at grey. Expected crafts per point = 1 / (chance × skill-ups per craft).
- **Material cost.** Each reagent is priced at the cheapest of the AH market price, the vendor price, or crafting it yourself (e.g. bolts of cloth). Turn off the last with `-no-craft-reagents`.
- **Sale value** = AH market price × average yield × 0.95 (the AH's 5% cut). If the vendor pays more, it uses the vendor price. Enchants have no sale value.
- **Market price** = the average of the cheapest 15% of units listed, so one lowball listing doesn't skew it.
- **Ranking:**
  - Crafts that lose money are ranked by net gold per skill point.
  - Profitable crafts rank above any losing craft, ordered by profit per craft.
  - The plan avoids crafts below a 30% skill-up chance unless nothing else is available. Otherwise "craft 25 shirts for 1 point" would win whenever the shirt is profitable.
- **The plan** picks the best recipe at every skill point and merges the runs. It notes where to train Journeyman (50), Expert (125) and Artisan (200). It warns when you'd craft more than the AH has listed, or more than 3 days of sales.

### Recipe source filter

By default only **trainer**, **vendor** and **unknown-source** recipes are used, because drops and quest rewards may not be in your book. Change this with `-sources` (`t` trainer, `v` vendor, `d` drop, `q` quest, `c` other, `?` unknown, or `all`) or the checkboxes in the UI. Exclude specific recipes you don't have with `-exclude 3915,8776` (spell ids, shown in `-json` output).

### Caveats

- Blizzard's AH API has no sales history, so "sold per day" only appears when using Forever Goblin.
- The API is undocumented for Forever and may change. House ids in particular are community findings.
- Deposits aren't included. Crafting a stack doesn't mean the market will buy a stack: check "Listed".

## Development

```sh
go test ./...
go vet ./...
```

Layout:

- `main.go`: CLI
- `internal/auction`: Blizzard and Forever Goblin clients, price aggregation, snapshots
- `internal/recipes`: recipe dataset loader and skill-up maths
- `internal/advisor`: costing, ranking and the leveling planner
- `internal/service`: shared JSON answers for the server and the browser build
- `internal/web`: local web UI and JSON API (`/api/status`, `/api/recommend`, `POST /api/refresh`)
- `cmd/wasm`: WebAssembly entry point for the hosted site
- `scripts/build-site.sh`: builds the static site (used by the workflow)
