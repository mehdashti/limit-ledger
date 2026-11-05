// Package share builds the table a user can publish, and pools tables from many
// users. A table holds numbers only: per account-week, the weekly reading and the
// token totals per kind (in Opus 5.5 equivalents, rounded), plus the user's own fit.
// It holds no content, paths, session or account ids, e-mail, or times finer than a
// day. Accounts are numbered 1, 2, … in the order they appear.
package share

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mehdashti/limit-ledger/internal/calibrate"
	"github.com/mehdashti/limit-ledger/internal/store"
)

// SchemaVersion is the version of the table format.
const SchemaVersion = 1

// Table is one user's shared table for one plan.
type Table struct {
	SchemaVersion int           `json:"schema_version"`
	Tool          string        `json:"tool"`
	ToolVersion   string        `json:"tool_version"`
	Created       string        `json:"created"` // YYYY-MM-DD
	Plan          string        `json:"plan"`
	Observations  []Observation `json:"observations"`
	Fit           *Fit          `json:"fit,omitempty"`
	Note          string        `json:"note,omitempty"`
}

// Observation is one account-week.
type Observation struct {
	Account    int                `json:"account"`
	WeekEnding string             `json:"week_ending"` // UTC date of the weekly reset
	Percent    float64            `json:"percent"`
	Source     string             `json:"source"`
	Units      map[string]float64 `json:"units"` // Opus 5.5 equivalents, rounded to thousands
}

// Fit is the user's own best fit.
type Fit struct {
	Weights      map[string]float64 `json:"weights"` // relative to one Opus 5.5 output token
	PerPercent   float64            `json:"per_percent"`
	Spread       float64            `json:"spread"`
	Observations int                `json:"observations"`
}

var kindKeys = [...]string{calibrate.Input: "input", calibrate.CacheWrite: "cache_write",
	calibrate.CacheRead: "cache_read", calibrate.Output: "output"}

var sources = map[string]bool{"limit hit": true, "switch": true, "reading": true}

// Build makes one table per plan from the user's observations.
func Build(st *store.Store, opt calibrate.Options, toolVersion, note string) ([]Table, error) {
	obs, _, err := calibrate.Collect(st, opt)
	if err != nil {
		return nil, err
	}
	created := time.UnixMilli(opt.Now).UTC().Format("2006-01-02")
	number := map[string]int{}
	byPlan := map[string][]calibrate.Observation{}
	var plans []string
	for _, o := range obs {
		if _, ok := number[o.Account]; !ok {
			number[o.Account] = len(number) + 1
		}
		if _, ok := byPlan[o.Plan]; !ok {
			plans = append(plans, o.Plan)
		}
		byPlan[o.Plan] = append(byPlan[o.Plan], o)
	}
	sort.Strings(plans)
	var out []Table
	for _, plan := range plans {
		po := byPlan[plan]
		t := Table{SchemaVersion: SchemaVersion, Tool: "limit-ledger", ToolVersion: toolVersion, Created: created,
			Plan: plan, Note: note}
		for _, o := range po {
			u := map[string]float64{}
			for k, name := range kindKeys {
				u[name] = math.Round(o.Units[k]/1000) * 1000
			}
			t.Observations = append(t.Observations, Observation{Account: number[o.Account],
				WeekEnding: time.UnixMilli(o.Reset).UTC().Format("2006-01-02"), Percent: o.Percent,
				Source: o.Source, Units: u})
		}
		if len(po) >= 2 {
			best := calibrate.Fit(po)[0]
			w := map[string]float64{}
			for k, name := range kindKeys {
				w[name] = best.Weights[k]
			}
			t.Fit = &Fit{Weights: w, PerPercent: math.Round(best.PerPercent), Spread: round(best.Spread, 4),
				Observations: len(po)}
		}
		out = append(out, t)
	}
	return out, nil
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

// Validate checks a table read from someone else.
func (t Table) Validate() error {
	switch {
	case t.SchemaVersion != SchemaVersion:
		return fmt.Errorf("schema_version %d, want %d", t.SchemaVersion, SchemaVersion)
	case t.Tool != "limit-ledger":
		return fmt.Errorf("tool %q", t.Tool)
	case strings.TrimSpace(t.Plan) == "":
		return errors.New("plan is empty")
	case len(t.Observations) == 0:
		return errors.New("no observations")
	}
	if _, err := time.Parse("2006-01-02", t.Created); err != nil {
		return fmt.Errorf("created: %v", err)
	}
	for i, o := range t.Observations {
		if o.Percent <= 0 || o.Percent > 100 {
			return fmt.Errorf("observation %d: percent %v out of range", i+1, o.Percent)
		}
		if !sources[o.Source] {
			return fmt.Errorf("observation %d: unknown source %q", i+1, o.Source)
		}
		if _, err := time.Parse("2006-01-02", o.WeekEnding); err != nil {
			return fmt.Errorf("observation %d: week_ending: %v", i+1, err)
		}
		if o.Account < 1 {
			return fmt.Errorf("observation %d: account must be 1, 2, …", i+1)
		}
		for k, v := range o.Units {
			if !isKind(k) {
				return fmt.Errorf("observation %d: unknown unit kind %q", i+1, k)
			}
			if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("observation %d: bad %s units %v", i+1, k, v)
			}
		}
		if o.Units["output"] <= 0 {
			return fmt.Errorf("observation %d: no output units", i+1)
		}
	}
	return nil
}

func isKind(k string) bool {
	for _, n := range kindKeys {
		if n == k {
			return true
		}
	}
	return false
}

// toCalibrate converts a shared observation back for fitting.
func (o Observation) toCalibrate(plan string) calibrate.Observation {
	var u calibrate.Units
	for k, name := range kindKeys {
		u[k] = o.Units[name]
	}
	return calibrate.Observation{Plan: plan, Percent: o.Percent, Source: o.Source, Units: u}
}

// Load reads every *.json table in dir. Files that fail validation are reported in bad.
func Load(dir string) (tables map[string]Table, bad map[string]error, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	tables, bad = map[string]Table{}, map[string]error{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			bad[e.Name()] = err
			continue
		}
		var t Table
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&t); err != nil {
			bad[e.Name()] = err
			continue
		}
		if err := t.Validate(); err != nil {
			bad[e.Name()] = err
			continue
		}
		tables[e.Name()] = t
	}
	return tables, bad, nil
}

// PlanPool is the pooled result for one plan.
type PlanPool struct {
	Plan         string
	Files        int
	Observations int
	Best         calibrate.Hypothesis
	References   map[string]calibrate.Hypothesis
	// PerFile is each file's mean cost of 1% under the pooled weights, relative to the
	// pooled mean (0.1 = 10% more tokens per 1% than the pool).
	PerFile map[string]float64
}

// Pool fits the weights on all observations of each plan together.
func Pool(tables map[string]Table) []PlanPool {
	byPlan := map[string][]string{}
	for name, t := range tables {
		p := strings.ToLower(strings.TrimSpace(t.Plan))
		byPlan[p] = append(byPlan[p], name)
	}
	var out []PlanPool
	for plan, files := range byPlan {
		sort.Strings(files)
		var all []calibrate.Observation
		for _, f := range files {
			for _, o := range tables[f].Observations {
				all = append(all, o.toCalibrate(plan))
			}
		}
		pp := PlanPool{Plan: plan, Files: len(files), Observations: len(all), Best: calibrate.Fit(all)[0],
			References: map[string]calibrate.Hypothesis{}, PerFile: map[string]float64{}}
		for _, r := range calibrate.References {
			pp.References[r.Name] = calibrate.Evaluate(all, r.Multiplier)
		}
		for _, f := range files {
			var sum float64
			obs := tables[f].Observations
			for _, o := range obs {
				sum += calibrate.PerPercent(o.toCalibrate(plan), pp.Best.Weights)
			}
			if pp.Best.PerPercent > 0 && len(obs) > 0 {
				pp.PerFile[f] = sum/float64(len(obs))/pp.Best.PerPercent - 1
			}
		}
		out = append(out, pp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plan < out[j].Plan })
	return out
}
