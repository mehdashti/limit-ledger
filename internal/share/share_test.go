package share

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mehdashti/limit-ledger/internal/calibrate"
	"github.com/mehdashti/limit-ledger/internal/store"
	"github.com/mehdashti/limit-ledger/internal/transcript"
)

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func TestBuildHoldsNumbersOnly(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.PutOrgEvent(&transcript.OrgEvent{TS: ms("2026-10-01T00:00:00Z"), Org: "secret-org-uuid", SessionID: "secret-session"}); err != nil {
		t.Fatal(err)
	}
	for i, out := range []int64{20_123_456, 18_000_400} {
		reset := ms("2026-10-13T07:00:00Z") + int64(i)*7*24*3600*1000
		if err := b.PutResponse(&transcript.Response{MsgID: fmt.Sprint("m", i), TS: reset - 2*24*3600*1000,
			Model: "claude-opus-5-5", Kind: "main", Tokens: transcript.Tokens{Output: out, CacheRead: 5_000_000_000}}); err != nil {
			t.Fatal(err)
		}
		if err := b.PutAnchor(&transcript.Anchor{Kind: "weekly", Hit: reset - 24*3600*1000, Resets: reset}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	plan, label := "Ultra", "my-work-account"
	if _, err := st.SetAccount(st.AccountKey("secret-org-uuid"), &label, &plan); err != nil {
		t.Fatal(err)
	}
	tables, err := Build(st, calibrate.Options{Now: ms("2026-11-05T12:34:56Z")}, "test", "note")
	if err != nil || len(tables) != 1 {
		t.Fatalf("tables %+v %v", tables, err)
	}
	tb := tables[0]
	if err := tb.Validate(); err != nil {
		t.Fatal(err)
	}
	if tb.Created != "2026-11-05" || tb.Plan != "Ultra" || len(tb.Observations) != 2 || tb.Fit == nil {
		t.Fatalf("table %+v", tb)
	}
	o := tb.Observations[0]
	if o.Account != 1 || o.WeekEnding != "2026-10-13" || o.Percent != 100 || o.Source != "limit hit" ||
		o.Units["output"] != 20_123_000 || o.Units["cache_read"] != 5_000_000_000 {
		t.Errorf("observation %+v", o)
	}
	j, _ := json.Marshal(tb)
	for _, secret := range []string{"secret", st.AccountKey("secret-org-uuid"), "my-work-account", "12:34"} {
		if strings.Contains(string(j), secret) {
			t.Errorf("table leaks %q: %s", secret, j)
		}
	}
}

func table(plan string, perPct float64, weeks ...[2]float64) Table {
	t := Table{SchemaVersion: SchemaVersion, Tool: "limit-ledger", ToolVersion: "t", Created: "2026-11-05", Plan: plan}
	for i, w := range weeks {
		out, read := w[0], w[1]
		units := out + 0.0025*read // truth: cache read ×0.25 API, cache writes free
		f := perPct * 100 / units
		t.Observations = append(t.Observations, Observation{Account: 1, WeekEnding: fmt.Sprintf("2026-10-%02d", 10+i),
			Percent: 100, Source: "switch", Units: map[string]float64{"input": 0, "cache_write": 1e8 * f,
				"cache_read": read * f, "output": out * f}})
	}
	return t
}

func TestPool(t *testing.T) {
	tables := map[string]Table{
		"a.json": table("ultra", 350e3, [2]float64{18e6, 6.3e9}, [2]float64{20e6, 6.2e9}),
		"b.json": table("Ultra", 350e3, [2]float64{15e6, 7.0e9}, [2]float64{10e6, 9.0e9}),
		"c.json": table("ultra", 350e3*1.2, [2]float64{16e6, 6.0e9}), // a user whose weeks are worth 20% more
		"p.json": table("pro", 18e3, [2]float64{1e6, 0.4e9}, [2]float64{0.8e6, 0.5e9}),
	}
	pools := Pool(tables)
	if len(pools) != 2 || pools[0].Plan != "pro" || pools[1].Plan != "ultra" {
		t.Fatalf("pools %+v", pools)
	}
	u := pools[1]
	if u.Files != 3 || u.Observations != 5 {
		t.Errorf("ultra pool %+v", u)
	}
	if u.Best.Multiplier[calibrate.CacheRead] != 0.25 || u.Best.Multiplier[calibrate.CacheWrite] != 0 {
		t.Errorf("pooled weights %v", u.Best.Multiplier)
	}
	if d := u.PerFile["c.json"] - u.PerFile["a.json"]; math.Abs(d-0.2*350e3/u.Best.PerPercent) > 0.02 {
		t.Errorf("per-file deviations %v", u.PerFile)
	}
}

func TestLoadRejects(t *testing.T) {
	dir := t.TempDir()
	good, _ := json.Marshal(table("ultra", 350e3, [2]float64{18e6, 6e9}))
	files := map[string]string{
		"good.json":    string(good),
		"extra.json":   strings.Replace(string(good), `"plan"`, `"email":"x@y","plan"`, 1),
		"version.json": strings.Replace(string(good), `"schema_version":1`, `"schema_version":9`, 1),
		"broken.json":  "{",
		"pct.json":     strings.Replace(string(good), `"percent":100`, `"percent":140`, 1),
		"README.md":    "not a table",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tables, bad, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables["good.json"].Plan != "ultra" {
		t.Errorf("tables %v", tables)
	}
	for _, n := range []string{"extra.json", "version.json", "broken.json", "pct.json"} {
		if bad[n] == nil {
			t.Errorf("%s should be rejected", n)
		}
	}
	if _, ok := bad["README.md"]; ok {
		t.Error("non-JSON files are ignored, not rejected")
	}
}
