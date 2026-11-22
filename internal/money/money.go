// Package money formats WoW copper amounts.
package money

import (
	"fmt"
	"math"
	"strings"
)

// Format renders copper as "12g 34s 56c". Negative values get a leading "-".
func Format(copper float64) string {
	neg := copper < 0
	c := int64(math.Round(math.Abs(copper)))
	g, s, cc := c/10000, (c/100)%100, c%100
	var parts []string
	if g > 0 {
		parts = append(parts, fmt.Sprintf("%dg", g))
	}
	if s > 0 || g > 0 {
		parts = append(parts, fmt.Sprintf("%ds", s))
	}
	parts = append(parts, fmt.Sprintf("%dc", cc))
	out := strings.Join(parts, " ")
	if neg && c != 0 {
		out = "-" + out
	}
	return out
}
