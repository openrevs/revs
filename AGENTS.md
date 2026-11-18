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

## Package layout

The server and client halves are importable on their own, so another
application can build on either without the other (issue #15). The
`revs` binary in `cmd/revs` is just one consumer: it should only use
what the packages export, and anything it needs belongs in a package's
API, not in `cmd/revs`.

- `ratingset`, `keys`: the document format and identities, shared by
  both halves. They depend on nothing else in the module.
- `server`: the publishing server. `server.Open` or `server.New` builds
  one; mount `Handler` and call `Run`, or call `ListenAndServe`.
- `client` (and `client/sqlitestore`, `client/fsstore`): the sync
  protocol client: pulling, verifying, and pushing documents.
- `collection`: the local data layer client apps are built on (movies,
  viewings, identity, servers, push/pull), over `client`.
- `ui`, `tui`: the local web and terminal apps, over `collection`.
- `revstest`: the in-process multi-server test harness.

`internal/layout` has a test that fails if these depend on each other
in the wrong direction (e.g. `collection` reaching `server`). Update its
rules deliberately if the layering changes.

## Web UI interactivity

`revs ui` uses Datastar (https://data-star.dev). `ui/static/datastar.js`
is a vendored client bundle (version on its first line), and
`ui/datastar.go` has the server side and explains the pattern: pages
are server-rendered templates; forms and buttons stay plain HTML that
works without JavaScript, with a `data-on:*` Datastar action added; a
handler checks `isDatastar(r)` and answers with `a.patch` (named
`{{define}}` fragments, morphed into the page by id) or `a.done`,
falling back to the usual redirect. Errors and messages go to the
shared `#flash` area. Prefer this over custom JavaScript, and keep the
no-JavaScript path working. Test Datastar answers with `testUI.ds`.

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
