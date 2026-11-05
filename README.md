# limit-ledger

Measures your Claude Code usage against the subscription limits, locally. It learns from the server's own
utilization figures what one percent of the weekly limit is worth, and it reports when that changes.

**Unofficial.** Not affiliated with or endorsed by Anthropic. All weights and budgets it reports are estimates.

Status: early development. M1 and M2 work:
- reading transcripts and attributing usage to accounts;
- weekly and limit-hit reports;
- recording the weekly percentage through a statusline hook.
Calibration and change detection are next. See [DESIGN.md](DESIGN.md).

## Privacy

- Reads only usage fields from Claude Code's transcripts (`~/.claude/projects`): token counts, model, speed and
  timestamps, plus the text of limit-reached messages.
- Never reads message content into its database. Never reads credentials. Sends nothing anywhere.
- The database lives in your data directory (`~/.local/share/limit-ledger` on Linux,
  `~/Library/Application Support/limit-ledger` on macOS).

## Build

Requires Go 1.27 or newer:

```sh
go build -o limit-ledger ./cmd/limit-ledger
```

## Use

```sh
limit-ledger ingest            # first run reads all history; later runs read what was appended
limit-ledger report weekly     # tokens per kind and API-equivalent dollars per ISO week
limit-ledger report anchors    # usage in each weekly window up to the moment the limit was hit
limit-ledger accounts          # accounts seen on this machine, their stretches and reset schedules
limit-ledger accounts set KEY --label work --plan max   # name an account
limit-ledger report weekly --by-account

limit-ledger statusline install   # record the weekly % that Claude Code shows (see below)
limit-ledger report windows       # weekly % against local usage: API-equivalent $ and output per 1%
limit-ledger statusline uninstall # put the previous statusline back
```

### The statusline hook

On every statusline refresh, Claude Code passes its statusline command the current 5-hour and weekly
utilization. It already received these figures with its last API response. `statusline install` points
`statusLine` in `~/.claude/settings.json` at `limit-ledger statusline`. It keeps a backup of the file, and it
keeps showing any statusline you already had. The hook appends one line to a local spool file and makes no
network request. `ingest` moves the spool into the ledger.

Install from a stable location (for example `go install ./cmd/limit-ledger`), because the settings file stores
the binary's path.

### Claude desktop app

The desktop app does not run the statusline command. Record readings from its usage card by hand:

```sh
limit-ledger sample add --weekly 86 --resets 2026-11-08T22:00:00Z
```

You can also schedule a desktop task that reads the plan usage every few hours and runs that command followed
by `limit-ledger ingest`.

If you switch between several accounts, the ledger works out which one was active at each moment from Claude
Code's `credential_org` records, which exist since about September 2026. It stores only salted hashes of account
ids.

API-equivalent dollars use Anthropic's public list prices as a common unit. The subscription limit weighs
tokens in its own, unpublished way. Learning those weights is the point of this project.

## License

MIT. See [LICENSE](LICENSE).
