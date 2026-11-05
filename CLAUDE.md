# limit-ledger — working rules

## Workflow
- One developer and one agent work on this project. Commit directly on `main`. Do not create feature branches
  and do not propose one.
- Commits use `mehdashti <mehdashti@gmail.com>`. It is set in this repository's local git config.
- At the start of a session, read `notes/HANDOFF.md` if it exists. It describes the owner's machines, accounts,
  the desktop scheduled task and the current findings.
- This project runs no containers, so the global close rule (stop the project's containers) has nothing to stop.

## Privacy
- `notes/` holds the owner's private usage numbers, prototypes and the handoff note. It is git-ignored and is
  never committed or published.
- Tracked files never contain personal details: account or organization ids, e-mail addresses, machine
  addresses, or one person's per-account usage. Those go to `notes/`.
- The tool reads usage fields only, and never message content or credentials. The design and the privacy rules
  are in `DESIGN.md` (sections 2 and 5).

## Building and testing
- Test with `gofmt -l .`, `go vet ./...` and `go test -race ./...`. CI also checks `community/TABLE.md` and the
  GoReleaser config.
- `go install ./cmd/limit-ledger` updates `~/go/bin/limit-ledger`, which the desktop scheduled task and the
  statusline hook run. Rebuild after every change that should reach them.
- `detect` records each alert as raised. When experimenting on the real ledger, run it on a copy
  (`--db COPY`, made with SQLite's backup), so real alerts still reach the notifications.
- A schema change is a new entry in `migrations` (`internal/store/store.go`). If the migration clears `files`,
  the next `ingest` reads all history again, which takes about 20 seconds here.

## Releasing
1. Push to `main` and wait until CI is green (tests on Linux and macOS, and the release-config job).
2. Tag `vX.Y.Z` with an annotated tag and push the tag. `release.yml` runs GoReleaser and publishes the GitHub
   Release.
3. Check the release files. Other machines update with `install.sh` (`curl … | sh`).
