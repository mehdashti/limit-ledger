// Package price converts token counts into API-equivalent dollars at Anthropic's
// first-party list prices. This is a common unit for comparison only. The
// subscription limit uses its own, unpublished weights, which the calibrator learns.
package price

import (
	"sort"
	"strings"

	"github.com/mehdashti/limit-ledger/internal/transcript"
)

// AsOf is the date of the price list below.
const AsOf = "2026-10-06"

// Cache writes cost a multiple of the input price.
const (
	Cache5mFactor = 1.25
	Cache1hFactor = 2.0
	FastFactor    = 2.0 // fast mode costs twice the standard price
)

// Rate is a price in dollars per million tokens.
type Rate struct {
	Input, Output, CacheRead float64
}

var table = []struct {
	prefix string
	rate   Rate
}{
	{"claude-fable-5-1", Rate{10, 50, 0.25}},
	{"claude-fable-5", Rate{10, 50, 1.00}},
	{"claude-mythos-5-1", Rate{10, 50, 0.25}},
	{"claude-opus-5-5", Rate{4, 20, 0.20}},
	{"claude-opus-5", Rate{5, 25, 0.50}},
	{"claude-opus-4-8", Rate{5, 25, 0.50}},
	{"claude-opus-4-7", Rate{5, 25, 0.50}},
	{"claude-opus-4-6", Rate{5, 25, 0.50}},
	{"claude-opus-4-5", Rate{5, 25, 0.50}},
	{"claude-opus-4", Rate{15, 75, 1.50}}, // Opus 4 and 4.1
	{"claude-sonnet-5-5", Rate{2, 10, 0.20}},
	{"claude-sonnet-5", Rate{2, 10, 0.20}},
	{"claude-sonnet-4", Rate{3, 15, 0.30}},
	{"claude-haiku-5-5", Rate{0.10, 0.50, 0.01}},
	{"claude-haiku-4-5", Rate{1, 5, 0.10}},
	{"claude-3-5-haiku", Rate{0.80, 4, 0.08}},
}

func init() {
	sort.SliceStable(table, func(i, j int) bool { return len(table[i].prefix) > len(table[j].prefix) })
}

// Lookup returns the rate for a model id by its longest known prefix.
func Lookup(model string) (Rate, bool) {
	for _, e := range table {
		if strings.HasPrefix(model, e.prefix) {
			return e.rate, true
		}
	}
	return Rate{}, false
}

// Cost returns the API-equivalent dollars of t on model at speed. ok is false for a
// model that is not in the table.
func Cost(model, speed string, t transcript.Tokens) (dollars float64, ok bool) {
	r, ok := Lookup(model)
	if !ok {
		return 0, false
	}
	d := float64(t.Input)*r.Input +
		float64(t.Cache5m)*r.Input*Cache5mFactor +
		float64(t.Cache1h)*r.Input*Cache1hFactor +
		float64(t.CacheRead)*r.CacheRead +
		float64(t.Output)*r.Output
	d /= 1e6
	if speed == "fast" {
		d *= FastFactor
	}
	return d, true
}
