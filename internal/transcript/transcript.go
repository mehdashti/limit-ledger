// Package transcript reads Claude Code transcript lines. It extracts token usage from
// assistant responses and limit-hit anchors from synthetic limit messages.
//
// Privacy: only usage fields, model, speed, timestamps and the text of synthetic
// limit messages are decoded. Message content is never decoded or kept.
package transcript

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// Tokens holds the token counts of one response or of a sum of responses.
type Tokens struct {
	Input     int64 // fresh input tokens
	Cache5m   int64 // cache writes with 5-minute TTL
	Cache1h   int64 // cache writes with 1-hour TTL
	CacheRead int64 // cache reads
	Output    int64 // output tokens, thinking included
}

// Add returns the sum of t and o.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{
		Input:     t.Input + o.Input,
		Cache5m:   t.Cache5m + o.Cache5m,
		Cache1h:   t.Cache1h + o.Cache1h,
		CacheRead: t.CacheRead + o.CacheRead,
		Output:    t.Output + o.Output,
	}
}

// Response is one API response as recorded in a transcript.
type Response struct {
	MsgID string
	TS    int64 // Unix milliseconds, UTC
	Model string
	Speed string
	Tokens
	Thinking  int64
	WebSearch int64
	WebFetch  int64
	Sidechain bool
	Kind      string // main, subagent or workflow
}

// Anchor is a moment at which a limit was hit, so utilization was 100%.
type Anchor struct {
	Kind   string // weekly, session, or the limit name as written
	Hit    int64  // Unix milliseconds
	Resets int64  // Unix milliseconds
}

// OrgEvent records which organization (account) a session's credentials belonged to
// at a moment. Claude Code writes these as "credential_org" attachments.
type OrgEvent struct {
	TS        int64  // Unix milliseconds
	Org       string // organization UUID; the store keeps only a salted hash of it
	SessionID string
}

// Result is what one line yielded. All fields may be nil.
type Result struct {
	Response *Response
	Anchor   *Anchor
	Org      *OrgEvent
}

var (
	markAssistant = []byte(`"assistant"`)
	markUsage     = []byte(`"usage"`)
	markSynthetic = []byte(`"<synthetic>"`)
	markCredOrg   = []byte(`"credential_org"`)
)

type rawOrg struct {
	Type       string `json:"type"`
	Timestamp  string `json:"timestamp"`
	SessionID  string `json:"sessionId"`
	Attachment *struct {
		Type             string `json:"type"`
		OrganizationUUID string `json:"organizationUuid"`
	} `json:"attachment"`
}

type rawRecord struct {
	Type        string      `json:"type"`
	Timestamp   string      `json:"timestamp"`
	IsSidechain bool        `json:"isSidechain"`
	RequestID   string      `json:"requestId"`
	Message     *rawMessage `json:"message"`
}

type rawMessage struct {
	ID    string    `json:"id"`
	Model string    `json:"model"`
	Usage *rawUsage `json:"usage"`
}

type rawUsage struct {
	InputTokens              int64  `json:"input_tokens"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	Speed                    string `json:"speed"`
	CacheCreation            *struct {
		Ephemeral5m *int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokensDetails *struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	ServerToolUse *struct {
		WebSearchRequests int64 `json:"web_search_requests"`
		WebFetchRequests  int64 `json:"web_fetch_requests"`
	} `json:"server_tool_use"`
}

type rawSynthetic struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   *struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// KindFromPath classifies a transcript by its path relative to the projects root.
func KindFromPath(rel string) string {
	p := "/" + filepath.ToSlash(rel)
	switch {
	case strings.Contains(p, "/subagents/workflows/"):
		return "workflow"
	case strings.Contains(p, "/subagents/"):
		return "subagent"
	default:
		return "main"
	}
}

// ParseLine extracts a response or an anchor from one JSONL line. Lines that carry
// neither return an empty Result and no error. kind is stored on responses.
func ParseLine(line []byte, kind string) (Result, error) {
	if bytes.Contains(line, markCredOrg) {
		// The marker can also appear inside ordinary text, so only a real attachment
		// record counts; anything else falls through to the normal checks.
		if ev, err := parseOrg(line); err != nil || ev != nil {
			return Result{Org: ev}, err
		}
	}
	if !bytes.Contains(line, markAssistant) {
		return Result{}, nil
	}
	if bytes.Contains(line, markSynthetic) {
		a, err := parseSynthetic(line)
		return Result{Anchor: a}, err
	}
	if !bytes.Contains(line, markUsage) {
		return Result{}, nil
	}
	r, err := parseResponse(line, kind)
	return Result{Response: r}, err
}

func parseResponse(line []byte, kind string) (*Response, error) {
	var rec rawRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, err
	}
	m := rec.Message
	if rec.Type != "assistant" || m == nil || m.Usage == nil || m.Model == "" || m.Model == "<synthetic>" {
		return nil, nil
	}
	id := m.ID
	if id == "" {
		id = rec.RequestID
	}
	if id == "" {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		return nil, err
	}
	u := m.Usage
	r := &Response{
		MsgID:     id,
		TS:        ts.UnixMilli(),
		Model:     m.Model,
		Speed:     u.Speed,
		Sidechain: rec.IsSidechain,
		Kind:      kind,
	}
	r.Input = u.InputTokens
	r.CacheRead = u.CacheReadInputTokens
	r.Output = u.OutputTokens
	if cc := u.CacheCreation; cc != nil && (cc.Ephemeral5m != nil || cc.Ephemeral1h != nil) {
		if cc.Ephemeral5m != nil {
			r.Cache5m = *cc.Ephemeral5m
		}
		if cc.Ephemeral1h != nil {
			r.Cache1h = *cc.Ephemeral1h
		}
	} else {
		r.Cache5m = u.CacheCreationInputTokens
	}
	if d := u.OutputTokensDetails; d != nil {
		r.Thinking = d.ThinkingTokens
	}
	if s := u.ServerToolUse; s != nil {
		r.WebSearch = s.WebSearchRequests
		r.WebFetch = s.WebFetchRequests
	}
	return r, nil
}

func parseOrg(line []byte) (*OrgEvent, error) {
	var rec rawOrg
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, nil // not a clean attachment record; let the other checks decide
	}
	a := rec.Attachment
	if rec.Type != "attachment" || a == nil || a.Type != "credential_org" || a.OrganizationUUID == "" {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		return nil, err
	}
	return &OrgEvent{TS: ts.UnixMilli(), Org: a.OrganizationUUID, SessionID: rec.SessionID}, nil
}

func parseSynthetic(line []byte) (*Anchor, error) {
	var rec rawSynthetic
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, err
	}
	if rec.Type != "assistant" || rec.Message == nil || rec.Message.Model != "<synthetic>" {
		return nil, nil
	}
	hit, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		return nil, err
	}
	for _, text := range contentTexts(rec.Message.Content) {
		if a, ok := ParseLimitMessage(hit, text); ok {
			return a, nil
		}
	}
	return nil, nil
}

// contentTexts returns the text of a message content that is either a string or a
// list of blocks.
func contentTexts(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			out = append(out, b.Text)
		}
	}
	return out
}
