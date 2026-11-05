package report

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mehdashti/limit-ledger/internal/store"
)

// unknownAccount is how the time before any credential_org record is shown.
const unknownAccount = "unknown"

type accountNames map[string]string

func loadNames(st *store.Store) (accountNames, []store.Account, error) {
	accts, err := st.Accounts()
	if err != nil {
		return nil, nil, err
	}
	n := accountNames{"": unknownAccount}
	for _, a := range accts {
		n[a.Key] = a.Name()
	}
	return n, accts, nil
}

func (n accountNames) of(key string) string {
	if v, ok := n[key]; ok {
		return v
	}
	return key
}

// usageBy sums usage in [from, to) per account: this machine's usage following the
// timeline, plus each account's usage imported from other machines.
func usageBy(st *store.Store, segs []store.Segment, from, to int64) (map[string]Summary, error) {
	groups := map[string][]store.Group{}
	for _, sg := range segs {
		a, b := max(from, sg.Start), min(to, sg.End)
		if a >= b {
			continue
		}
		g, err := st.LocalTotals(a, b)
		if err != nil {
			return nil, err
		}
		groups[sg.Account] = append(groups[sg.Account], g...)
	}
	remote, err := st.RemoteAccounts(from, to)
	if err != nil {
		return nil, err
	}
	for _, acct := range remote {
		g, err := st.RemoteTotals(acct, from, to)
		if err != nil {
			return nil, err
		}
		groups[acct] = append(groups[acct], g...)
	}
	out := map[string]Summary{}
	for acct, g := range groups {
		out[acct] = Summarize(g)
	}
	return out, nil
}

// switchRange measures how much of acct's usage in [from, to) depends on where an
// account switch is placed. The switch is placed at the first record of the new
// account, but it happened somewhere after the last record of the old one. Usage in
// that gap is counted for the old account. minus is what acct may have been given
// wrongly (gaps after its stretches); plus is what it may have missed (gaps before them).
func switchRange(st *store.Store, segs []store.Segment, acct string, from, to int64) (minus, plus float64, err error) {
	for i := 1; i+1 < len(segs); i++ {
		cur, next := segs[i], segs[i+1]
		if cur.Account != acct && next.Account != acct {
			continue
		}
		a, b := max(from, cur.LastEvent), min(to, next.Start)
		if a >= b {
			continue
		}
		g, err := st.LocalTotals(a, b) // the switch moment is uncertain only on this machine
		if err != nil {
			return 0, 0, err
		}
		d := Summarize(g).Dollars
		if cur.Account == acct {
			minus += d
		} else {
			plus += d
		}
	}
	return minus, plus, nil
}

// schedule names the weekday and UTC time of a weekly reset.
func schedule(resetMs int64) string {
	return time.UnixMilli(resetMs).UTC().Format("Mon 15:04")
}

type hit struct {
	hit, resets int64
	account     string
}

// weeklyHits returns the earliest hit per reset time, with the account that was
// active just before each hit.
func weeklyHits(st *store.Store, segs []store.Segment) ([]hit, error) {
	all, err := st.Anchors("weekly")
	if err != nil {
		return nil, err
	}
	var out []hit
	for _, a := range all {
		if n := len(out); n > 0 && out[n-1].resets == a.Resets {
			continue
		}
		out = append(out, hit{hit: a.Hit, resets: a.Resets, account: store.AccountAt(segs, a.Hit)})
	}
	return out, nil
}

// Accounts prints the accounts seen on this machine and their reset schedules.
func Accounts(st *store.Store, w io.Writer) error {
	accts, err := st.Accounts()
	if err != nil {
		return err
	}
	if len(accts) == 0 {
		fmt.Fprintln(w, "No account records found. Claude Code has written them (credential_org) since about September 2026.")
		return nil
	}
	segs, err := st.Timeline()
	if err != nil {
		return err
	}
	hits, err := weeklyHits(st, segs)
	if err != nil {
		return err
	}
	sched := map[string]map[string]int{}
	nhits := map[string]int{}
	for _, h := range hits {
		if sched[h.account] == nil {
			sched[h.account] = map[string]int{}
		}
		sched[h.account][schedule(h.resets)]++
		nhits[h.account]++
	}
	nseg := map[string]int{}
	for _, sg := range segs {
		nseg[sg.Account]++
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "key\tlabel\tplan\tswitch\tfirst seen\tlast seen\tstretches\tweekly hits\treset schedule (UTC)")
	for _, a := range accts {
		sw := "-"
		if a.SwitchFull {
			sw = "at limit"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\n", a.Key, dash(a.Label), dash(a.Plan), sw,
			stamp(a.First), stamp(a.Last), nseg[a.Key], nhits[a.Key], schedules(sched[a.Key]))
	}
	if n := nhits[""]; n > 0 {
		fmt.Fprintf(tw, "%s\t\t\t\t\t\t\t%d\t%s\n", unknownAccount, n, schedules(sched[""]))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, "\nA stretch is a period during which one account was active on this machine.")
	fmt.Fprintln(w, "Hits marked unknown happened before Claude Code recorded which account was in use.")
	fmt.Fprintln(w, "Label an account: limit-ledger accounts set KEY --label NAME --plan PLAN")
	return nil
}

func schedules(m map[string]int) string {
	type kv struct {
		k string
		v int
	}
	var l []kv
	for k, v := range m {
		l = append(l, kv{k, v})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].v > l[j].v || (l[i].v == l[j].v && l[i].k < l[j].k) })
	var parts []string
	for _, e := range l {
		parts = append(parts, fmt.Sprintf("%s ×%d", e.k, e.v))
	}
	return strings.Join(parts, ", ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
