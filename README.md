# limit-ledger

Measures your Claude Code usage against the subscription limits, locally. It learns from the server's own
utilization figures what one percent of the weekly limit is worth, and it reports when that changes.

**Unofficial.** Not affiliated with or endorsed by Anthropic. All weights and budgets it reports are estimates.

Status: early development. Milestone M1 works: it reads transcripts and prints weekly and limit-hit reports.
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
```

API-equivalent dollars use Anthropic's public list prices as a common unit. The subscription limit weighs
tokens in its own, unpublished way. Learning those weights is the point of this project.

## License

MIT. See [LICENSE](LICENSE).
