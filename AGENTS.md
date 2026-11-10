# Agent notes

## Specs and design docs live in the website repo

Design docs, specs, and user documentation for revs are not kept in
this repo. They live in the website repo
(https://github.int.exe.xyz/openrevs/website, published at
https://openrevs.org), usually checked out at `../website`:

- `specs/` holds the finished specs (e.g. SJSONL).
- `wip-docs/` holds docs that are still being migrated and revised
  (configuration, publishing, UI, security, formats, ...). At the moment
  these live on the `wip-docs` branch.

When a code change affects documented behavior (CLI flags, config keys,
the upload/publish protocol, the UI), update the matching page in the
website repo. Don't add a `docs/` directory here.
