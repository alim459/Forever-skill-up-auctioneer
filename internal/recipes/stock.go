package recipes

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Stock describes a vendor item sold in limited quantity.
type Stock struct {
	Max     int `json:"max"`     // copies the vendor holds
	Restock int `json:"restock"` // seconds until one more copy appears
}

// LimitedStock maps item id to its limited-stock data.
type LimitedStock map[int]Stock

// tuple matches the leading (entry,item,maxcount,incrtime, of a vendor row.
var tuple = regexp.MustCompile(`\((\d+),(\d+),(\d+),(\d+),`)

// ParseVendorSQL extracts limited-stock items from a CMaNGOS world database
// dump (npc_vendor and npc_vendor_template tables, gzipped or plain). Where
// several vendors sell an item, the shortest restock time is kept.
func ParseVendorSQL(r io.Reader) (LimitedStock, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	if b, err := br.Peek(2); err == nil && b[0] == 0x1f && b[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		br = bufio.NewReaderSize(gz, 1<<20)
	}
	out := LimitedStock{}
	for {
		line, err := br.ReadString('\n')
		if strings.HasPrefix(line, "INSERT INTO `npc_vendor` VALUES") || strings.HasPrefix(line, "INSERT INTO `npc_vendor_template` VALUES") {
			for _, m := range tuple.FindAllStringSubmatch(line, -1) {
				item, _ := strconv.Atoi(m[2])
				max, _ := strconv.Atoi(m[3])
				restock, _ := strconv.Atoi(m[4])
				if max <= 0 {
					continue
				}
				if cur, ok := out[item]; !ok || restock < cur.Restock {
					out[item] = Stock{Max: max, Restock: restock}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no limited-stock vendor rows found")
	}
	return out, nil
}

// LoadLimitedStock reads a JSON file written by SaveJSON; a missing file is
// an empty table.
func LoadLimitedStock(path string) (LimitedStock, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return LimitedStock{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseLimitedStock(b)
}

// ParseLimitedStock decodes the JSON form ({"itemId": {"max":1,"restock":3600}}).
func ParseLimitedStock(b []byte) (LimitedStock, error) {
	var raw map[string]Stock
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := LimitedStock{}
	for k, v := range raw {
		if id, err := strconv.Atoi(k); err == nil {
			out[id] = v
		}
	}
	return out, nil
}

// SaveJSON writes the table.
func (l LimitedStock) SaveJSON(path string) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
