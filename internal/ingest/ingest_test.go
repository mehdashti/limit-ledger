package ingest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mehdashti/limit-ledger/internal/store"
)

func resp(id, ts, model string, cacheRead, output int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":%q,`+
		`"content":[{"type":"text","text":"x"}],"usage":{"input_tokens":1,"cache_creation_input_tokens":10,`+
		`"cache_read_input_tokens":%d,"output_tokens":%d,`+
		`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":10}}}}`,
		ts, id, model, cacheRead, output)
}

const anchorLine = `{"type":"assistant","timestamp":"2026-10-12T16:23:09.360Z","message":{"model":"<synthetic>",` +
	`"content":[{"type":"text","text":"You've hit your weekly limit · resets 7am (UTC)"}]}}`

func writeFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

type totals struct{ requests, cacheRead, output int64 }

func sum(t *testing.T, st *store.Store) totals {
	t.Helper()
	groups, err := st.Totals(0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	var s totals
	for _, g := range groups {
		s.requests += g.Requests
		s.cacheRead += g.CacheRead
		s.output += g.Output
	}
	return s
}

func TestIngestIncrementalAndDedup(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	main := filepath.Join(root, "proj", "s1.jsonl")
	sub := filepath.Join(root, "proj", "s1", "subagents", "agent-a.jsonl")
	nested := filepath.Join(root, "proj", "s1", "subagents", "agent-b.jsonl") // started by agent-a
	wf := filepath.Join(root, "proj", "s1", "subagents", "workflows", "wf_1", "agent-c.jsonl")
	fork := filepath.Join(root, "proj", "s2.jsonl") // a fork that copied msg_1

	writeFile(t, main,
		`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n",
		// A response split over two lines; the first copy carries zeros.
		strings.Replace(resp("msg_1", "2026-10-10T10:00:00Z", "claude-opus-5-5", 0, 0), `"input_tokens":1`, `"input_tokens":0`, 1)+"\n",
		resp("msg_1", "2026-10-10T10:00:00Z", "claude-opus-5-5", 1000, 50)+"\n",
		anchorLine+"\n",
		// Partial last line: still being written, must not be read yet.
		resp("msg_2", "2026-10-10T10:05:00Z", "claude-opus-5-5", 2000, 60)[:40],
	)
	writeFile(t, sub, resp("msg_3", "2026-10-10T10:01:00Z", "claude-sonnet-5-5", 300, 30)+"\n")
	writeFile(t, nested, resp("msg_4", "2026-10-10T10:02:00Z", "claude-sonnet-5-5", 400, 40)+"\n")
	writeFile(t, wf, resp("msg_5", "2026-10-10T10:03:00Z", "claude-haiku-5-5", 500, 50)+"\n")
	writeFile(t, fork, resp("msg_1", "2026-10-10T10:00:00Z", "claude-opus-5-5", 1000, 50)+"\n", "not json at all\n")

	stats, err := Run(st, Options{Root: root, Workers: 3})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 5 || stats.FilesRead != 5 {
		t.Errorf("files %d read %d, want 5 and 5", stats.Files, stats.FilesRead)
	}
	if stats.ParseErrors != 0 {
		// "not json at all" has no markers, so it is skipped before decoding.
		t.Errorf("parse errors %d", stats.ParseErrors)
	}
	if got, want := sum(t, st), (totals{4, 1000 + 300 + 400 + 500, 50 + 30 + 40 + 50}); got != want {
		t.Errorf("after first run: %+v, want %+v", got, want)
	}
	anchors, err := st.Anchors("weekly")
	if err != nil || len(anchors) != 1 {
		t.Fatalf("anchors %v, %v", anchors, err)
	}

	// Second run with no changes reads nothing.
	stats, err = Run(st, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if stats.FilesRead != 0 {
		t.Errorf("unchanged files re-read: %d", stats.FilesRead)
	}

	// Finish the partial line and add one more response.
	full := resp("msg_2", "2026-10-10T10:05:00Z", "claude-opus-5-5", 2000, 60)
	appendTo(t, main, full[40:]+"\n"+resp("msg_6", "2026-10-10T10:06:00Z", "claude-opus-5-5", 10, 1)+"\n")
	stats, err = Run(st, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if stats.FilesRead != 1 || stats.Responses != 2 {
		t.Errorf("incremental run: read %d files, %d responses; want 1 and 2", stats.FilesRead, stats.Responses)
	}
	if got, want := sum(t, st), (totals{6, 2200 + 2000 + 10, 170 + 60 + 1}); got != want {
		t.Errorf("after append: %+v, want %+v", got, want)
	}

	// A replaced (shorter) file is read again from the start; nothing is double counted.
	writeFile(t, sub, resp("msg_3", "2026-10-10T10:01:00Z", "claude-sonnet-5-5", 300, 30)[:10]+"\n")
	if _, err := Run(st, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	if got, want := sum(t, st), (totals{6, 4210, 231}); got != want {
		t.Errorf("after rewrite: %+v, want %+v", got, want)
	}
}

func TestIngestKinds(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	writeFile(t, filepath.Join(root, "p", "s.jsonl"), resp("m1", "2026-10-10T10:00:00Z", "claude-opus-5-5", 1, 1)+"\n")
	writeFile(t, filepath.Join(root, "p", "s", "subagents", "agent-x.jsonl"), resp("m2", "2026-10-10T10:00:00Z", "claude-opus-5-5", 1, 1)+"\n")
	writeFile(t, filepath.Join(root, "p", "s", "subagents", "workflows", "wf_9", "agent-y.jsonl"), resp("m3", "2026-10-10T10:00:00Z", "claude-opus-5-5", 1, 1)+"\n")
	if _, err := Run(st, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	kinds, err := st.KindCounts()
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]int64{"main": 1, "subagent": 1, "workflow": 1} {
		if kinds[k] != want {
			t.Errorf("kind %s: %d want %d (all: %v)", k, kinds[k], want, kinds)
		}
	}
}

func TestReadLinesLongLine(t *testing.T) {
	long := strings.Repeat("a", 3<<20) // longer than the 1 MiB read buffer
	in := "first\n" + long + "\nunfinished"
	var got []int
	n, err := readLines(bufio.NewReaderSize(strings.NewReader(in), 1<<20), func(b []byte) { got = append(got, len(b)) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 6 || got[1] != len(long)+1 {
		t.Errorf("line lengths %v", got)
	}
	if n != int64(6+len(long)+1) {
		t.Errorf("consumed %d", n)
	}
}

func TestMissingRoot(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := Run(st, Options{Root: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Error("missing projects directory should be an error")
	}
}
