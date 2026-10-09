# dbtest conventions

Rules for anyone changing this repo, human or agent.

## Go naming

- Export only what another package uses. An unexported type has unexported
  fields unless an encoder (`encoding/json`, struct tags) needs them.
- Names are full words. The only abbreviations are `cfg`, `tel`, `ctx`, `err`,
  and Go-style initialisms (`ID`, `URL`, `ARN`, `DSN`).
- A field holding an SDK client is named after the service: `ecs`, `logs`,
  `client` when there is one.
- A package with more than one constructor names them `New<Thing>`
  (`NewRDS`, `NewAurora`); a package with one uses `New`.
- Per-package config: an unexported `<thing>Config` struct, filled by
  `loadConfig()` from environment variables.

## Code

- Comments state what the code does, then stop. No trailing "why" clauses.
- Inline small fixed strings; no single-use `const` blocks.

## Commits

- Subject line plus at most two short paragraphs. Rationale goes in the PR.
