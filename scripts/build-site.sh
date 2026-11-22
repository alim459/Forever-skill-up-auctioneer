#!/usr/bin/env bash
# Builds the static site for GitHub Pages into $1 (default: site/).
#
# The site is the normal web UI plus:
#   app.wasm, wasm_exec.js  the Go advisor compiled to WebAssembly
#   data/prices-*.json      a fresh price snapshot per auction house
#   data/houses.json        the list of houses that were fetched
#
# Env:
#   BLIZZARD_CLIENT_ID / BLIZZARD_CLIENT_SECRET  use Blizzard's API (else Forever Goblin)
#   FOREVER_REGION    default us
#   FOREVER_HOUSES    space-separated house ids to try (default: all seen so far)
#   FOREVER_RULESETS  rulesets to try per house with Blizzard (default: 1 2 3 4 5)
set -euo pipefail

OUT="${1:-site}"
REGION="${FOREVER_REGION:-us}"
HOUSES="${FOREVER_HOUSES:-110 111 112 113 114 115 116 117 118 119 120 121 123 124 125}"
RULESETS="${FOREVER_RULESETS:-1 2 3 4 5}"

rm -rf "$OUT"
mkdir -p "$OUT/data" bin
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

go build -o bin/fsa .
cp internal/web/static/* "$OUT/"
echo 'window.FSA_STATIC = true;' > "$OUT/config.js"
WASM_EXEC="$(go env GOROOT)/lib/wasm/wasm_exec.js"
[ -f "$WASM_EXEC" ] || WASM_EXEC="$(go env GOROOT)/misc/wasm/wasm_exec.js"
cp "$WASM_EXEC" "$OUT/"
GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o "$OUT/app.wasm" ./cmd/wasm

# Ruleset metadata (names, which houses belong to which ruleset). Saved for the
# site and printed to the log so house ids can be mapped to faction/ruleset.
if [ -n "${BLIZZARD_CLIENT_ID:-}" ] && [ -n "${BLIZZARD_CLIENT_SECRET:-}" ]; then
  mkdir -p "$OUT/data/rulesets"
  if ./bin/fsa api -region "$REGION" /data/wow/ruleset/index > "$OUT/data/rulesets/index.json" 2>&1; then
    echo "ruleset index: $(head -c 2000 "$OUT/data/rulesets/index.json")"
  else
    echo "ruleset index failed: $(tail -n1 "$OUT/data/rulesets/index.json")"
  fi
  ids="$(jq -r '.rulesets[]?.id' "$OUT/data/rulesets/index.json" 2>/dev/null | sort -n | tr '\n' ' ')"
  for rs in ${ids:-$RULESETS}; do
    if ./bin/fsa api -region "$REGION" "/data/wow/ruleset/$rs" > "$OUT/data/rulesets/$rs.json" 2>&1; then
      echo "ruleset $rs: $(jq -c '{id,name,status:.status.name}' "$OUT/data/rulesets/$rs.json")"
    else
      echo "ruleset $rs failed: $(tail -n1 "$OUT/data/rulesets/$rs.json")"; rm -f "$OUT/data/rulesets/$rs.json"
    fi
    if ./bin/fsa api -region "$REGION" "/data/wow/ruleset/$rs/auctions/index" > "$WORK/aidx.json" 2>&1; then
      echo "ruleset $rs auctions: $(jq -c '[.auctions[] | "\(.id) \(.name)"]' "$WORK/aidx.json")"
      cp "$WORK/aidx.json" "$OUT/data/rulesets/$rs-auctions.json"
    fi
  done
fi

entries=()
for h in $HOUSES; do
  ok=""
  if [ -n "${BLIZZARD_CLIENT_ID:-}" ] && [ -n "${BLIZZARD_CLIENT_SECRET:-}" ]; then
    for rs in $RULESETS; do
      if err=$(./bin/fsa prices -data "$WORK" -source blizzard -region "$REGION" -ruleset "$rs" -house "$h" 2>&1); then
        ok="$rs"; break
      fi
      echo "  house $h ruleset $rs (Blizzard): $(echo "$err" | tail -n1)"
    done
  fi
  if [ -z "$ok" ]; then
    if err=$(./bin/fsa prices -data "$WORK" -source goblin -region "$REGION" -house "$h" 2>&1); then
      ok="0"
    else
      echo "  house $h (Forever Goblin): $(echo "$err" | tail -n1)"
    fi
  fi
  if [ -n "$ok" ]; then
    f="prices-$REGION-$h.json"
    cp "$WORK/$f" "$OUT/data/$f"
    hist="${HISTORY_DIR:-.cache/history}/history-$REGION-$h.json"
    if ./bin/fsa history -data "$WORK" -region "$REGION" -house "$h" -out "$hist" >/dev/null; then
      cp "$hist" "$OUT/data/history-$REGION-$h.json"
    else
      echo "  house $h: history update failed"
    fi
    name="$(jq -r '.houseName // ""' "$WORK/$f")"
    entries+=("$(jq -cn --arg r "$REGION" --argjson h "$h" --argjson rs "$ok" --arg f "$f" --arg n "$name" '{region:$r,house:$h,ruleset:$rs,file:$f,name:$n}')")
    echo "house $h: ok (ruleset ${ok/#0/unknown, via Forever Goblin}) name='$name' items=$(jq '.items|length' "$WORK/$f")"
  else
    echo "house $h: no data"
  fi
done

if [ ${#entries[@]} -eq 0 ]; then
  echo "No auction house data could be fetched; not publishing." >&2
  if [ -z "${BLIZZARD_CLIENT_ID:-}" ]; then
    echo "Add BLIZZARD_CLIENT_ID and BLIZZARD_CLIENT_SECRET as repository secrets (Settings > Secrets and variables > Actions)." >&2
    echo "Forever Goblin blocks requests from GitHub's servers, so Blizzard keys are required for the hosted site." >&2
  fi
  exit 1
fi
# Limited-stock vendor patterns (cached; refreshed weekly).
LIMITED="${LIMITED_CACHE:-.cache/limited-stock.json}"
./scripts/limited-stock.sh ./bin/fsa "$LIMITED" || echo "limited stock: build failed"
[ -f "$LIMITED" ] && cp "$LIMITED" "$OUT/data/limited-stock.json"

# Item tooltips (stats) from Blizzard, cached between runs in $ITEM_CACHE so
# only new items are fetched.
ITEM_CACHE="${ITEM_CACHE:-.cache/items.json}"
if [ -n "${BLIZZARD_CLIENT_ID:-}" ] && [ -n "${BLIZZARD_CLIENT_SECRET:-}" ]; then
  ./bin/fsa items -data "$WORK" -region "$REGION" -out "$ITEM_CACHE" || echo "item tooltips: fetch failed (keeping cache)"
fi
[ -f "$ITEM_CACHE" ] && cp "$ITEM_CACHE" "$OUT/data/items.json"

( IFS=,; echo "{\"updated\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"houses\":[${entries[*]}]}" ) > "$OUT/data/houses.json"
touch "$OUT/.nojekyll"
echo "Site built in $OUT with ${#entries[@]} auction houses."
