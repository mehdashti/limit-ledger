# limit-ledger

Measures your Claude Code usage against the subscription limits, locally. It learns from the server's own
utilization figures what one percent of the weekly limit is worth, and it reports when that changes.

**Unofficial.** Not affiliated with or endorsed by Anthropic. All weights and budgets it reports are estimates.

Status: early development. First versions of M1–M5 work:
- reading transcripts and attributing usage to accounts;
- weekly and limit-hit reports;
- recording the weekly percentage (statusline hook, desktop task or by hand);
- estimating the weights of each token kind and the cost of 1% of the weekly limit;
- detecting changes in the weekly limit, with desktop notifications;
- exporting a shareable table and pooling shared tables.
See [DESIGN.md](DESIGN.md).

## Privacy

- Reads only usage fields from Claude Code's transcripts (`~/.claude/projects`): token counts, model, speed and
  timestamps, plus the text of limit-reached messages.
- Never reads message content into its database. Never reads credentials. Sends nothing anywhere.
- The database lives in your data directory (`~/.local/share/limit-ledger` on Linux,
  `~/Library/Application Support/limit-ledger` on macOS).

## Install

On Linux or macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/mehdashti/limit-ledger/main/install.sh | sh
```

The script picks the right file for your system from the [latest release](https://github.com/mehdashti/limit-ledger/releases),
checks it against the release's `checksums.txt`, and puts `limit-ledger` in `~/.local/bin`. Set
`LIMIT_LEDGER_VERSION=v0.1.0` for a given version, or `LIMIT_LEDGER_BIN_DIR` for another folder. On Windows, download
the `.zip` from the release page.

Release files are not signed. Files fetched by `curl` are not quarantined on macOS, so Gatekeeper does not stop them.

To build from source (Go 1.27 or newer):

```sh
go install github.com/mehdashti/limit-ledger/cmd/limit-ledger@latest
```

## Use

```sh
limit-ledger ingest            # first run reads all history; later runs read what was appended
limit-ledger report weekly     # tokens per kind and API-equivalent dollars per ISO week
limit-ledger report anchors    # usage in each weekly window up to the moment the limit was hit
limit-ledger accounts          # accounts seen on this machine, their stretches and reset schedules
limit-ledger accounts set KEY --label work --plan max   # name an account
limit-ledger report weekly --by-account

# daily averages of a period against the 4 weeks before it, or against another period
limit-ledger report compare --from 2026-11-02 --to 2026-11-05 --vs-weeks 4
limit-ledger report compare --from 2026-11-02 --vs-from 2026-10-26 --vs-to 2026-10-29 --account B

# estimate how the weekly limit weighs each kind of token
limit-ledger accounts set work --switch-full   # if you switch away from an account only at its limit
limit-ledger calibrate --weeks 2 --save
limit-ledger report fits

# has the weekly limit changed? compares each week with the weeks before it
limit-ledger detect             # table of all weeks and their deviation
limit-ledger detect --notify --quiet   # only new alerts, also as desktop notifications
limit-ledger report alerts

limit-ledger statusline install   # record the weekly % that Claude Code shows (see below)
limit-ledger report windows       # weekly % against local usage: API-equivalent $ and output per 1%
limit-ledger statusline uninstall # put the previous statusline back
```

### More than one machine

If you use an account on several machines, each machine's ledger sees only its own share, but the weekly figure
counts all of them. Pull the other machine's usage into one ledger:

```sh
limit-ledger pull other-machine     # over ssh, no password prompt; "skipped" if it is off or away
limit-ledger machines
```

`pull` runs `limit-ledger ingest` and `usage-export` on the other machine (default path
`~/.local/bin/limit-ledger`, change it with `--remote-bin`) and imports the result. By hand it is
`ssh other 'limit-ledger usage-export' | limit-ledger usage-import -`.

Accounts are matched across machines automatically by a cross-machine id; the transfer holds numbers only.

### Sharing your table

```sh
limit-ledger export --weeks 4     # one JSON file per plan, numbers only; read it before sharing
```

See [community/README.md](community/README.md) for what the file contains and how to add it to the pooled
[community table](community/TABLE.md).

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
