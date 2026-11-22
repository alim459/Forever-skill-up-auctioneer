#!/usr/bin/env bash
# Builds the limited-stock vendor table (which patterns a vendor sells only a
# few of, and how fast they restock) from CMaNGOS classic-db (GPL-3.0,
# https://github.com/cmangos/classic-db). Classic data: Forever may differ.
# Usage: scripts/limited-stock.sh <fsa binary> <output json>
set -euo pipefail
BIN="$1"
OUT="$2"
if [ -s "$OUT" ] && [ -z "$(find "$OUT" -mtime +7 2>/dev/null)" ]; then
  echo "limited stock: cached ($OUT)"
  exit 0
fi
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
git clone -q --depth 1 --filter=blob:none --sparse https://github.com/cmangos/classic-db "$TMP/cdb"
git -C "$TMP/cdb" sparse-checkout set Full_DB >/dev/null
SQL="$(ls "$TMP"/cdb/Full_DB/*.sql.gz | head -n1)"
mkdir -p "$(dirname "$OUT")"
"$BIN" vendorstock -sql "$SQL" -out "$OUT"
