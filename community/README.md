# Shared weights tables

People who run limit-ledger can share their table here. The combined table, [TABLE.md](TABLE.md), shows:
- how the weekly limit of each plan seems to weigh each kind of token;
- how much 1% of the week is worth;
- where users disagree, for example between plans or over time.

Every figure is an estimate from volunteers' own usage. None of it comes from Anthropic.

## What a table contains

`limit-ledger export` writes one JSON file per plan. It contains numbers only.

**What it contains:**
- the plan you set with `limit-ledger accounts set KEY --plan PLAN`;
- for each account-week:
  - the date the weekly limit reset;
  - the highest reading of the weekly figure (100% for a limit hit, or a switch at the limit);
  - the token totals per kind, in Opus 5.5 equivalents, rounded to thousands;
- your own best fit, if you have at least two weeks;
- the date the file was made, and an optional note you write.

**What it does not contain:** message content, prompts, file or project paths, session ids, account ids or
e-mail addresses, or times finer than a day. Accounts are numbered 1, 2, … in the order they appear.

## How to share

1. Run:

   ```sh
   limit-ledger export --weeks 4 --note "mostly long agent sessions, Opus"
   ```

2. Read the file. Share it only if you are fine with everything in it.
3. Share it either way:
   - **as a pull request:** add the file to this folder under any name ending in `.json`;
   - **as an issue:** open a "Share a weights table" issue and paste the file.

## How the pool works

```sh
limit-ledger community pool community             # text summary
limit-ledger community pool community --markdown  # regenerates TABLE.md
```

- **Validation:** every file is checked (`--check` fails the CI on a bad file).
- **Fitting:** all account-weeks of a plan are fitted together, with the same method as `limit-ledger
  calibrate`.
- **Comparison:** each file's cost of 1% under the pooled weights is shown against the pool.
- **Disagreement:** a file far from the pool may come from a different usage pattern, from use on other
  machines, or from a different budget in those weeks. That is a finding in itself, not something to average
  away.
