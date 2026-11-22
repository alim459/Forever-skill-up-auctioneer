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
    entries+=("{\"region\":\"$REGION\",\"house\":$h,\"ruleset\":$ok,\"file\":\"$f\"}")
    echo "house $h: ok (ruleset ${ok/#0/unknown, via Forever Goblin})"
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
( IFS=,; echo "{\"updated\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"houses\":[${entries[*]}]}" ) > "$OUT/data/houses.json"
touch "$OUT/.nojekyll"
echo "Site built in $OUT with ${#entries[@]} auction houses."
