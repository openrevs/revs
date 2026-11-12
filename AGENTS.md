# Agent notes

## Specs and design docs live in the website repo

Design docs, specs, and user documentation for revs are not kept in
this repo. They live in the website repo
(https://github.int.exe.xyz/openrevs/website, published at
https://openrevs.org), usually checked out at `../website`:

- `specs/` holds the specs and design docs (SJSONL, configuration,
  publishing, UI, security, formats, ...). Drafts not yet ready for the
  site carry `published: false` in their front matter, so GitHub Pages
  skips them; the local preview renders them.

When a code change affects documented behavior (CLI flags, config keys,
the upload/publish protocol, the UI), update the matching page in the
website repo. Don't add a `docs/` directory here.
