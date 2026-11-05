# limit-ledger — design

Status: first versions of M0–M5 built and released as v0.1.0 (2026-11-05). M6 (proxy) not started. Sections
marked "Implemented" describe what the code does; the rest is the original plan.

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
- First result (author's data, one machine only): over two weeks, cache writes ≈ 0 and cache reads ≈ 0.25 × their
  API ratio, with a spread of 3.3%. This was before the second machine's usage was merged (§4.5), so it is
  superseded. With both machines the two accounts differ steadily (see §4.4), and a fit that pools accounts is
  worse (≈ 11%).

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

**Implemented (v1, schema v5), `limit-ledger detect`:**
- Every account-week (the calibrate observations, default: last 6 weeks) is measured with one fixed set of
  weights: the newest saved fit, or else a fit on the span. Each week's cost of 1% is compared with the median
  of up to 4 earlier weeks of the same plan.
- Two earlier versions were tried and dropped:
  - comparing each week with all other weeks: one odd week distorted the verdict on its neighbours;
  - refitting the weights for each comparison: with 2–4 weeks the fitted weights swing to the grid edges.
- A deviation of at least `--threshold` (default 15%) is a change:
  - **budget:** the deviation keeps its sign and at least half its size under every reference weighting (API
    prices, output only, cache reads free). The whole limit moved, or, if 1% got cheaper, the account was also
    used elsewhere;
  - **weighting:** otherwise. The token mix or the weights changed.
- **Usage elsewhere:** a rise of the weekly figure of at least 1 point between two readings of a window, while
  local usage of that account was at most $2 API-equivalent per point, proves usage on another machine or in
  claude.ai.
- Alerts are raised for the newest week of each account and for every rise elsewhere. Each alert is raised once
  (`alerts` table); `--notify` shows a desktop notification (`notify-send`, `osascript`), and `--quiet` prints
  only new alerts. `report alerts` lists them. The desktop scheduled task runs `detect --notify --quiet` after
  each reading.
- Each account is compared with its own earlier weeks, not with other accounts. After merging the second machine,
  the author's two accounts (same plan) reached 100% at steadily different usage: about $2,050 and about $2,750
  API-equivalent per week. A comparison across accounts read that steady gap as a change. The likely cause is
  usage the ledger cannot see on one account (claude.ai chat).
- Result (author's data, both machines): one account is stable from mid-October to early November (−7%, +2%) and
  slightly larger this week (+16%). The other grew clearly from mid-October (+61%, then +47%). There is no sign
  of a cut.

### 4.5 merging machines

Implemented (schema v6):
- An account used on several machines has its usage split between their ledgers, but its weekly figure is
  account-wide. Without merging, every account sum misses the other machines' part. In the author's data that was
  up to 40% of a week.
- Each account gets a cross-machine id at ingest: `xid`, a SHA-256 of the organization UUID with a fixed salt. It
  is the same on every machine without storing the UUID. Local keys stay salted per machine.
- `usage-export` (on the other machine) writes every response it can attribute with its own timeline: time,
  account `xid`, model, speed, token counts, and a hash of the message id. `usage-import` (here) stores them in
  `remote_responses`, keyed by (machine, message hash), so a repeated import replaces rather than adds. A ledger
  refuses its own bundle.
- Every account sum adds the account's imported usage (`AccountTotals`, `usageBy`, `Totals`, `Daily`). The
  timeline and the switch-uncertainty range use this machine's data only, because they describe this machine.
- `limit-ledger pull HOST` does it over ssh in one step: BatchMode (no password prompt), a 5-second connect
  timeout, and "skipped" rather than an error when the host is unreachable (ssh exit code 255). The desktop task
  pulls the second machine every 2 hours.
- Not yet merged: the other machine's utilization samples and limit-hit anchors.

### 4.6 export and community tables

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

**Implemented (v1):**
- `limit-ledger export [--weeks N] [--out DIR] [--note TEXT]` writes `limit-ledger-<plan>-<date>.json` per plan
  (schema version 1). It holds:
  - the plan and the creation date;
  - per account-week: account number (1, 2, …), reset date, highest reading and its source, and token totals
    per kind in Opus 5.5 equivalents, rounded to thousands;
  - the user's own best fit.
  Tests check that no key, label, session, organization or clock time leaks into it.
- `limit-ledger community pool DIR [--markdown] [--check]` validates every `*.json` in the folder. Unknown
  fields, an unknown schema version or out-of-range values reject a file. It fits each plan on all shared
  account-weeks together and shows each file's cost of 1% against the pool.
- `community/README.md` explains how to share. `.github/ISSUE_TEMPLATE/share-table.yml` is the issue form. CI
  rejects bad files and checks that `community/TABLE.md` is regenerated.
- Open: pooling assumes one budget per plan across all shared weeks. Once tables span a change in the limit,
  the pool should be split by period.

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

| # | Deliverable | Status (2026-11-05) |
|---|---|---|
| M0 | Repository and this design | Done |
| M1 | `ingest` + backfill + weekly report + limit-hit anchors; account attribution | Done: 17 GB of history read in about 20 s |
| M2 | Utilization readings: statusline hook, readings by hand, desktop scheduled task | Done. The desktop app does not run the statusline, so readings there come from the scheduled task |
| M3 | `calibrate`: weights, cost of 1%, saved fits | v1: grid fit over cache weights; few observations yet |
| M4 | `detect`: change reports and notifications | v1: each account against its own earlier weeks; alerts raised once |
| M5 | `export` + `community/` pooling + public release | v1: export, pooling, issue form and CI check; release v0.1.0 published; no shared table yet |
| — | Merging machines (`usage-export`, `usage-import`, `pull`) | Done: responses only; readings and anchors of other machines not merged yet |
| M6 | Optional proxy mode | Not started; Anthropic's terms to be checked first |

Next steps, in order:
1. Let readings accumulate for a few weeks, then `calibrate --save` again. The saved fit of 2026-11-05 predates the
   merge.
2. Merge the other machines' utilization readings and limit-hit anchors as well (§4.5).
3. Calibrate per account with a shared set of weights (a budget factor per account), since accounts on one plan can
   differ steadily.
4. A Homebrew tap; a first table in `community/`.
5. M6 only if readings stay too sparse.

## 8. Open questions

Answered:
- The desktop app does not run the statusline command, and its hooks get no `rate_limits`. Readings there come
  from the app's own plan usage (scheduled task, integer percent).
- `used_percentage` in the statusline has one decimal place. The desktop app's usage figure is an integer.
- Each account's weekly window resets on its own fixed weekly schedule. The overlapping windows seen early on were
  two accounts.
- Name: limit-ledger.

Open:
1. How does an `api-request-blob` record (effort) link to its response?
2. Are small-model side calls really absent from transcripts, and how big are they?
3. The desktop app reports a separate weekly Fable window. Does it ever bind, and how does it relate to the
   all-models window?
4. Is the steady gap between two accounts on one plan explained by claude.ai chat use (not in transcripts), or is
   it something else?
5. Does the VS Code extension run the statusline command?
6. Anthropic's terms on local proxies and on publishing inferred weights.
