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

## Testing

`go test ./...` runs everything; no test needs extra processes or a
running server. For anything involving servers and clients talking to
each other (pushing, pulling, multi-server sync), use the `revstest`
package: a `revstest.Network` creates any number of in-memory servers
(`NewServer`), raw clients (`NewClient`), and collections
(`NewCollection`, `NewAuthor`) connected by an in-process HTTP
transport, and can take servers down, bring them back, or restart
them. New server or client dependencies should be injectable
(`revs.ServerConfig`, `collection.Options`, `client.Option`) so the
harness can supply test versions.
