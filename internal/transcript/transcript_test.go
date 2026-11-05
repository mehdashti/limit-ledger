package transcript

import (
	"testing"
	"time"
)

const assistantLine = `{"type":"assistant","timestamp":"2026-11-03T10:00:00.500Z","isSidechain":true,` +
	`"requestId":"req_1","message":{"id":"msg_1","model":"claude-opus-5-5","role":"assistant",` +
	`"content":[{"type":"text","text":"secret content that must not matter"}],` +
	`"usage":{"input_tokens":2,"cache_creation_input_tokens":300,"cache_read_input_tokens":5000,` +
	`"output_tokens":40,"output_tokens_details":{"thinking_tokens":7},` +
	`"server_tool_use":{"web_search_requests":1,"web_fetch_requests":2},"speed":"standard",` +
	`"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}}`

func TestParseResponse(t *testing.T) {
	res, err := ParseLine([]byte(assistantLine), "subagent")
	if err != nil {
		t.Fatal(err)
	}
	r := res.Response
	if r == nil || res.Anchor != nil {
		t.Fatalf("want a response only, got %+v", res)
	}
	want := Response{
		MsgID: "msg_1", TS: time.Date(2026, 11, 3, 10, 0, 0, 5e8, time.UTC).UnixMilli(),
		Model: "claude-opus-5-5", Speed: "standard",
		Tokens:   Tokens{Input: 2, Cache5m: 100, Cache1h: 200, CacheRead: 5000, Output: 40},
		Thinking: 7, WebSearch: 1, WebFetch: 2, Sidechain: true, Kind: "subagent",
	}
	if *r != want {
		t.Fatalf("got  %+v\nwant %+v", *r, want)
	}
}

func TestParseResponseWithoutSplitOrID(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-11-03T10:00:00Z","requestId":"req_9",` +
		`"message":{"model":"claude-sonnet-5-5","usage":{"input_tokens":1,` +
		`"cache_creation_input_tokens":64,"cache_read_input_tokens":0,"output_tokens":5}}}`
	res, err := ParseLine([]byte(line), "main")
	if err != nil {
		t.Fatal(err)
	}
	r := res.Response
	if r == nil {
		t.Fatal("no response")
	}
	if r.MsgID != "req_9" {
		t.Errorf("id fallback: got %q", r.MsgID)
	}
	if r.Cache5m != 64 || r.Cache1h != 0 {
		t.Errorf("unsplit cache write should count as 5m: %+v", r.Tokens)
	}
}

func TestParseLineIgnores(t *testing.T) {
	for name, line := range map[string]string{
		"user line":    `{"type":"user","message":{"role":"user","content":"hello assistant"}}`,
		"no usage":     `{"type":"assistant","timestamp":"2026-11-03T10:00:00Z","message":{"id":"m","model":"x"}}`,
		"no id at all": `{"type":"assistant","timestamp":"2026-11-03T10:00:00Z","message":{"model":"x","usage":{"output_tokens":1}}}`,
		"user with markers": `{"type":"user","timestamp":"2026-11-03T10:00:00Z","message":{"role":"user",` +
			`"content":"assistant","id":"m","model":"x","usage":{"output_tokens":9}}}`,
	} {
		res, err := ParseLine([]byte(line), "main")
		if err != nil || res.Response != nil || res.Anchor != nil {
			t.Errorf("%s: got %+v, %v", name, res, err)
		}
	}
	if _, err := ParseLine([]byte(`{"type":"assistant","usage":`), "main"); err == nil {
		t.Error("truncated JSON should be an error")
	}
}

func TestParseSyntheticAnchor(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-10-12T16:23:09.360Z","message":{"model":"<synthetic>",` +
		`"content":[{"type":"text","text":"You've hit your weekly limit · resets 7am (UTC)"}],` +
		`"usage":{"input_tokens":0,"output_tokens":0}},"isApiErrorMessage":true}`
	res, err := ParseLine([]byte(line), "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Response != nil {
		t.Fatal("synthetic message must not count as a response")
	}
	a := res.Anchor
	if a == nil {
		t.Fatal("no anchor")
	}
	if a.Kind != "weekly" {
		t.Errorf("kind %q", a.Kind)
	}
	if got, want := time.UnixMilli(a.Resets).UTC(), time.Date(2026, 10, 13, 7, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("resets %v, want %v", got, want)
	}
}

func TestKindFromPath(t *testing.T) {
	for rel, want := range map[string]string{
		"proj/abc.jsonl":                                  "main",
		"proj/abc/subagents/agent-1.jsonl":                "subagent",
		"proj/abc/subagents/workflows/wf_1/agent-2.jsonl": "workflow",
	} {
		if got := KindFromPath(rel); got != want {
			t.Errorf("%s: got %s want %s", rel, got, want)
		}
	}
}
