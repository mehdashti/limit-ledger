# limit-ledger — design

Status: draft, 2026-11-05. Nothing is built yet.

limit-ledger measures how much Claude Code usage a person spends locally. It learns, from the server's own
utilization figures, how many tokens of each kind one percent of the weekly limit is worth, and reports when that
changes. Each user can export their table of learned weights and share it, so tables can be compared and pooled.

It is an unofficial tool and is not affiliated with Anthropic.

## 1. Problem

Anthropic's subscription plans have a rolling 5-hour limit and a weekly limit. The weekly limit is shown only as a
percentage. Anthropic does not publish how tokens are converted into that percentage. The conversion depends on
the model, the kind of token (fresh input, cache write, cache read, output), and possibly speed. Users regularly
report that "the limit got smaller", but nobody has data that separates three cases:

1. the weekly budget itself changed;
2. the weight of one model or one kind of token changed (for example cache reads started to count more);
3. the user's own mix changed (longer contexts, more Fable, higher effort).

Counting tokens is not enough to tell these apart. The budget and the weights live on the server, so the tool
needs the server's utilization figure next to the local token counts.

## 2. Goals and non-goals

Goals:

- Count every response's tokens on this machine: main sessions, subagents, nested subagents and workflow agents.
  Include past weeks from the transcripts already on disk.
- Record the server's 5-hour and weekly utilization as it arrives, with no extra requests to the server.
- Estimate the hidden weights and the weekly budget, with uncertainty, and keep their history.
- Detect and report a change, and say which of the three cases above it looks like.
- Export a shareable table that contains numbers only, and combine tables from many users.

Non-goals:

- Not another cost tracker. Token and cost dashboards already exist. Our addition is the calibration against
  utilization, the change detection and the shared table.
- No polling of usage endpoints, and no reading of OAuth tokens or `~/.claude/.credentials.json`.
- No message content, prompts, file paths or account identifiers ever leave the machine. They are not stored
  either.

## 3. What the data looks like (verified on Claude Code 2.1.296, 2026-11-05)

### 3.1 Transcripts

- Location: `~/.claude/projects/<project-slug>/`.
  - Main session: `<session-id>.jsonl`.
  - Subagents: `<session-id>/subagents/agent-<agent-id>.jsonl`.
  - Nested subagents (a subagent that starts subagents): same flat `subagents/` folder, one file per child.
  - Workflow agents: `<session-id>/subagents/workflows/wf_<id>/agent-<agent-id>.jsonl`.
- Retention: Claude Code deletes old transcripts after `cleanupPeriodDays` (default 30). History reaches back
  only as far as that setting allowed.
- Each API response appears as one or more `type: "assistant"` records with `message.usage`:
  - `input_tokens`
  - `cache_creation_input_tokens`, split in `cache_creation.ephemeral_5m_input_tokens` and
    `cache_creation.ephemeral_1h_input_tokens`
  - `cache_read_input_tokens`
  - `output_tokens` (includes thinking; `output_tokens_details.thinking_tokens` gives the thinking part)
  - `server_tool_use.web_search_requests`, `server_tool_use.web_fetch_requests`
  - `speed` (`standard` / `fast`), `service_tier`
  - plus `message.model`, `message.id`, `requestId`, `timestamp`, `sessionId`, `isSidechain`, `agentId`.
- Duplicates:
  - One response is split over several lines that repeat the usage.
  - Resumed and forked sessions copy earlier records.
  - Deduplicate by `message.id`, falling back to `requestId`.
  - The copies do not always agree. Some carry zeros or a partial count. In August 2026, 14,621 of 45,506
    responses in one week had copies that differed, almost all in subagent files. Taking the first copy undercounted
    that week's output by 44% (27.7 M against 49.5 M). The ledger therefore keeps the largest value of each count
    per message id.
- Accounts:
  - Since September 2026, transcripts contain `type: "attachment"` records with
    `attachment.type: "credential_org"` and `attachment.organizationUuid`. They name the organization (account)
    the session's credentials belong to.
  - One machine can mix several accounts. Each account's weekly limit resets on its own fixed weekly schedule,
    so limit hits from different accounts fall on different weekdays and times.
  - Most of these records are in subagent files, written as the subagent starts. Few main session files carry
    one.
  - Without account attribution, the windows of different accounts overlap and their sums mix the accounts. See
    §4.1.
- Effort: found in `type: "api-request-blob"` records as `message.output_config.effort` (for example `xhigh`).
  These records also carry request content, so the reader must extract that one field and nothing else. How a
  blob links to its response is not verified yet (open question).
- Limit-hit anchors:
  - When a limit is hit, Claude Code writes a synthetic record with `message.model: "<synthetic>"` and text such
    as `You've hit your weekly limit · resets Aug 10, 1am (Europe/Istanbul)`.
  - At that moment weekly utilization is 100%. This gives calibration points for past weeks, before any
    utilization was recorded.

### 3.2 Utilization

- Every API response carries headers such as `anthropic-ratelimit-unified-5h-utilization`,
  `anthropic-ratelimit-unified-7d-utilization`, `anthropic-ratelimit-unified-5h-reset`,
  `anthropic-ratelimit-unified-7d-reset` and `anthropic-ratelimit-unified-status`. There are also `overage-*`,
  `grace-*` and `slow-*` variants.
- Claude Code passes these figures to the statusline command on stdin:

  ```json
  "rate_limits": {
    "five_hour": { "used_percentage": 0, "resets_at": 0 },
    "seven_day": { "used_percentage": 0, "resets_at": 0 }
  }
  ```

  The real payload has numbers in place of the zeros. A `spend_limit` entry also appears when one applies, and
  the whole object is present only when the server sent the data.
- Claude Code does not save these figures anywhere. **There is no local history of utilization.** It can be
  recorded only from the moment the tool is installed. The limit-hit anchors above are the only exception.

### 3.3 Blind spots

- **Side calls:** Claude Code makes side calls with a small model, for example to summarise a page in WebFetch.
  No small-model records were found in four weeks of transcripts, so these calls are probably not saved. Their
  size is unknown.
- **Usage elsewhere:** cloud sessions, other machines and claude.ai chat draw on the same limit but are not on
  this disk.
- Both show up as utilization that local tokens do not explain. The model keeps an explicit "unexplained" term
  for them (section 5.3).

## 4. Architecture

```
 ~/.claude/projects/**/*.jsonl ──► ingest ──┐
                                            ├──► ledger.db (SQLite, numbers only)
 statusline stdin (rate_limits) ─► sample ──┘        │
                                                     ▼
                                   calibrate ──► weights + budget (with history)
                                                     │
                                                     ▼
                                   detect ─────► change reports / notifications
                                                     │
                                                     ▼
                                   export ─────► shareable table (JSON) ──► community/ aggregate
```

### 4.1 ingest — token collector

- Walks `~/.claude/projects` recursively. On the first run it backfills from all history. After that it tails
  each file by remembered `(inode, offset)`.
- For each new assistant record with usage, it inserts one row keyed by message id into
  `responses(msg_id, ts, model, speed, effort, input, cache_5m, cache_1h, cache_read, output, thinking,
  web_search, web_fetch, is_sidechain, kind)`. `kind` is one of `main`, `subagent` or `workflow`.
- Stores no content, and no file paths or session ids in the clear. A per-install salted hash may be kept if
  grouping is needed.
- Collects limit-hit anchors into `anchors(ts, kind, resets_at)`. `kind` is `weekly` or `session`, and the
  reset time is parsed from the message text with its time zone.
- Account attribution (implemented, schema v2):
  - Every `credential_org` record becomes an `org_events(ts, account, session_key)` row. Only salted hashes of
    the organization and the session are stored.
  - One configuration directory holds one set of credentials, so one account is active at a time on the
    machine. The ledger builds a machine-wide timeline: a new stretch starts at the first record that names a
    different account. On real data, the records fall into clean, non-overlapping stretches.
  - Time before the first record is account `""` (unknown).
  - A limit hit belongs to the account active just before it.
  - A switch really happened somewhere between the last record of the old account and the first record of the
    new one. Most records sit in subagent files, written when a subagent starts, so that gap can be wide. The
    anchor report shows the resulting range ("switch uncertainty").
  - `limit-ledger accounts` lists accounts with their stretches, attributed hits and reset schedule. `accounts
    set KEY --label --plan` names them. The plan is needed for shared tables.
  - The utilization sampler will tag samples by the same timeline.

### 4.2 sample — utilization recorder

**Mode A, statusline (default):**
- The same binary runs as `limit-ledger statusline`, set as `statusLine.command`. It reads the stdin JSON and
  appends one line `(ts, five_hour.used_percentage, five_hour.resets_at, seven_day.used_percentage,
  seven_day.resets_at, claude_version)` to a spool file opened with `O_APPEND`.
- It does not open SQLite. Several sessions may run the statusline at once: appending a short line is safe for
  that, a database lock is not. `ingest` later moves the spool lines into `samples`.
- It then runs the user's previous statusline command, if any, with the same stdin and prints its output. The
  user's statusline therefore stays as it was.
- It must finish within ~50 ms and never print errors into the statusline.

Implemented (schema v3):
- `used_percentage` arrives with one decimal place (`round(utilization × 1000) / 10`), and `resets_at` is a Unix
  time in seconds.
- Samples are grouped into windows by `seven_day.resets_at`, because each account resets on its own schedule. The
  window's account is the one the account timeline names for most of its samples. A sample taken just after a
  switch therefore cannot split a window.
- `ingest` keeps a sample when any figure changed, or after 10 minutes without a change.
- `report windows` shows, per window, the rise of the weekly figure against the account's local usage
  meanwhile: API-equivalent dollars and output tokens per 1%. Usage elsewhere (other machines, claude.ai) also
  raises the figure, so the local cost of 1% is a lower bound.
- The Claude desktop app does not run the statusline command. Its built-in Claude Code (2.1.317) passes
  `rate_limits` only to the statusline, not to hooks, even though hooks do run in the desktop app. Whether the
  VS Code extension runs the statusline is still open.

**Mode C, manual and desktop readings:**
- `limit-ledger sample add --weekly PCT --resets TIME` records a reading by hand, from the figures in the app's
  usage card. Manual readings join the same windows as hook readings.
- In the desktop app, a scheduled task can take readings automatically. Every 2 hours it asks the app for the
  plan usage (the usage card's data: integer percent, reset time) and runs `sample add` and `ingest`. Each run is
  a short session that costs a little quota itself, and it runs only while the app is open.
- Reset times jitter by about a second between readings. Readings whose reset times are within an hour of each
  other belong to one window.

**Mode B, local proxy (optional, later, off by default):**
- A pass-through on `127.0.0.1` (`limit-ledger proxy`, built on `httputil.ReverseProxy`), enabled through
  `ANTHROPIC_BASE_URL`. It reads the `anthropic-ratelimit-*` headers and the usage of every response, including
  the side calls that transcripts miss.
- Rules:
  - stream SSE through without buffering;
  - never log request bodies or `authorization` headers;
  - give a one-command disable;
  - ship only after checking Anthropic's terms.
- Risk: it is a single point of failure. If it stops, every Claude Code session on the machine stops.

### 4.3 calibrate — weights and budget

- **Windows:** samples are grouped by `seven_day.resets_at`, so one window is `[resets_at − 7 d, resets_at]`. A
  jump in `resets_at` that does not match a normal rollover (a manual reset or a plan change) starts a new
  window, which is flagged.
- **Equations:** two consecutive samples in the same window, at least `k` points apart, give one equation:
  `Δ% = Σ_{model, kind} w[model, kind] · tokens[model, kind] + u`. Here `u` is the unexplained term (§3.3) and
  `k` is about 3, depending on the precision of `used_percentage`. Anchors add equations from the window start
  to 100%.
- **Units:** weights are relative to one reference unit. The proposal is one Opus 5.5 output token = 1. The
  budget `B` is the number of units that equal 100% of one window. Relative weights are probably the same for
  everybody, while `B` depends on the plan. Keeping them apart is what makes tables from different users
  comparable.
- **Fit:**
  - The starting assumption is API list-price ratios.
  - Fit with non-negative least squares, regularised toward that prior. Token kinds are strongly correlated
    (cache reads and output grow together), and some models have little data.
  - Report a confidence interval for each weight. A weight with too little data stays near the prior and is
    marked "prior-dominated".
- **History:** every fit is stored with its date range, so the weights over time can be inspected.

**Implemented (v1, schema v4), `limit-ledger calibrate`:**
- One observation per account-week: the account's usage from the start of its window up to its highest
  reading. Readings come from three sources:
  - limit-hit messages (100%);
  - switches away from an account marked `--switch-full` (100%): "I switch only at the limit". A switch
    counts only if the account is not used again before its reset, and only if it was used in that window;
  - samples (hook, manual or the desktop task).
- Windows come from each account's reset schedule. It is known from a limit hit or a reading and repeats
  weekly. Windows that start before account records exist are skipped.
- Tokens become Opus 5.5 equivalents per kind using API price ratios between models. Only the weight of each
  kind is fitted.
- Fit: a grid of multipliers on the API ratios for cache writes and cache reads. Input keeps its API ratio,
  because there is too little of it to fit, and output is the unit. The best fit is the one under which the cost
  of 1% varies least across observations (coefficient of variation). This assumes one budget per plan across
  the chosen weeks (`--weeks`, default 2), so a budget change inside the span shows up as a poor fit.
- `calibrate --save` stores the best fit per plan, and `report fits` lists the history. The observation table
  shows each week's cost of 1% under the best fit and its deviation from the mean.
- First result (author's data): over two weeks, cache writes ≈ 0 and cache reads ≈ 0.25 × their API ratio, with
  a spread of 3.3%. Adding the mid-October weeks gives a much worse fit (11%), which points to a different budget
  then.

### 4.4 detect — change detection

- With the current fit, predict each interval's `Δ%` from its tokens and track `observed / predicted`.
- Use a sequential test (CUSUM or rolling median against the previous two windows) to flag a sustained shift
  beyond normal noise. The starting threshold is 15%, to be tuned.
- Classify the shift:
  - **budget change:** all weights move together, which means `B` changed;
  - **re-weighting:** one weight moves, for example cache reads, or one model;
  - **unexplained usage:** `u` grows while local tokens do not, which means usage happened elsewhere or in side
    calls;
  - **window anomaly:** an irregular reset.
- Output: a report line in the CLI, an optional desktop notification, and an optional HTML page.

### 4.5 export and community tables

- `export` writes one JSON file per user:
  - `schema_version` and `tool_version`
  - `plan` (declared by the user: Pro / Max / Ultra / Team / Enterprise)
  - date range and Claude Code version range
  - model mix shares and sample count
  - relative weights with confidence intervals
  - `B` per window
  - free-text notes
- It never contains content, paths, session ids, account ids or email. An optional random install id is allowed,
  so repeated submissions can be merged.
- Sharing without a server:
  - users open a pull request that adds their file under `community/`, or fill in an issue form;
  - a script in the repository pools the files, weighted by inverse variance;
  - the script publishes the combined table and shows where users disagree, for example by plan.
- If relative weights differ between users or plans, that is itself a finding and is shown, not averaged away.

## 5. Privacy and safety rules

1. Read only usage fields, model, speed, effort, timestamps and the limit-hit text. Never store or send message
   content.
2. Never read credentials or tokens, and never call usage endpoints.
3. The statusline hook keeps the user's own statusline working and fails silently.
4. Proxy mode is off by default, experimental, and documented with its risk.
5. Shared tables hold numbers only. The user reviews the file before it is submitted.
6. The tool is clearly labelled unofficial, its name does not suggest an Anthropic product, and its weights are
   called estimates.

## 6. Stack

Decided 2026-11-05: **Go**. The goals were no extra install for users, memory safety and speed. TypeScript on
Node was the runner-up. Rust was set aside: it brings more complexity for no gain this tool would feel. C was
ruled out: hand-written JSON and TLS handling is too risky.

- **Language:** Go. One static binary and no runtime for the user to install. Every command is a subcommand of
  that binary: `ingest`, `statusline`, `calibrate`, `detect`, `report`, `export`, and later `proxy`.
- **Storage:** SQLite through `modernc.org/sqlite`, which is pure Go with no cgo, so cross-compiling stays a plain
  `GOOS`/`GOARCH` build. Data lives in the user's data directory: XDG on Linux,
  `~/Library/Application Support` on macOS.
- **Parsing:** lines are prefiltered with `bytes.Contains` for `"usage"`, then decoded with `encoding/json`.
  During backfill a bounded worker pool reads files in parallel.
- **Fit:** a small non-negative least-squares solver, regularised toward the prior and written in the project.
  The problem has only tens of weights, so `gonum` is added only if it earns its place.
- **Proxy (M6):** the standard library's `net/http/httputil.ReverseProxy`, with immediate flushing so SSE
  streams pass through unbuffered.
- **Dependencies:** as few as possible. CI runs `go vet` and `govulncheck`.
- **Tests and CI:** `go test`, on GitHub Actions for Linux and macOS.
- **Release:** GoReleaser publishes binaries for Linux, macOS and Windows (amd64 and arm64) on GitHub Releases,
  plus a Homebrew tap.
  - Unsigned binaries trigger Gatekeeper on macOS and SmartScreen on Windows.
  - Signing and notarisation are a release-time decision. A Homebrew formula install usually avoids the macOS
    prompt.
- **Platforms:** Linux and macOS first, Windows paths later.
- **License:** MIT for code. For the community data table: MIT, CC0 or CC-BY-4.0 (to decide).
- **Reference timings, measured on the author's machine on 2026-11-05:**
  - The single-threaded Python prototype read 18 GB of transcripts in about 40 s.
  - Hook start-up was 10–12 ms in Python and about 18 ms in Node.
  - A Go binary is expected to start in a few milliseconds. This is not measured yet.

## 7. Milestones

| # | Deliverable | Done when |
|---|---|---|
| M0 | Repository and this design | Design reviewed |
| M1 | `ingest` + backfill + weekly report (tokens per kind and model, API-equivalent $) + limit-hit anchors | Report matches the first-look numbers |
| M2 | `sample` statusline hook, chaining the existing statusline | A week of samples recorded without breaking the statusline |
| M3 | `calibrate` v1: weights, `B`, confidence intervals, history | Weights for Opus 5.5 with usable intervals |
| M4 | `detect`: change reports and notifications | A simulated budget change is caught and classified |
| M5 | `export` + `community/` aggregation + public GitHub release | First external table merged |
| M6 | Optional proxy mode | Terms checked, fail-safe disable tested |

## 8. Open questions

1. Does the Claude desktop app (Code tab) run the statusline command? If not, samples come only from terminal
   `claude` sessions. That is enough because utilization is account-wide, but it is sparser.
2. Is `used_percentage` an integer or fractional? This decides the minimum interval `k`.
3. How does an `api-request-blob` record (effort) link to its response?
4. Are small-model side calls really absent from transcripts, and how big are they?
5. Does the separate weekly Fable limit appear in `rate_limits`, or only in the headers?
6. Is the weekly window always `resets_at − 7 d` per account? The overlaps seen so far are explained by two
   accounts with fixed weekly schedules. This needs confirming once account attribution exists.
7. Anthropic's terms on local proxies and on publishing inferred weights.
8. Final project name.
