# limit-ledger — working rules

- One developer and one agent work on this project. Commit directly on `main`. Do not create feature branches
  and do not propose one.
- Commits use `mehdashti <mehdashti@gmail.com>`. It is set in this repository's local git config.
- `notes/` holds the owner's private usage numbers and prototypes. It is git-ignored and is never committed or
  published.
- The design and the privacy rules are in `DESIGN.md` (section 5). The tool reads usage fields only, and never
  message content or credentials.
