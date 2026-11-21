# MovieLens seed data

This package and the `revs-seed` command turn the
[MovieLens](https://grouplens.org/datasets/movielens/) ratings datasets
into revs data, so servers and clients can be tested at a realistic
scale (issue #38). There are two ways to use it:

- `revs-seed export` writes each MovieLens user's ratings as a signed
  ratingset document: a corpus to load, inspect or upload.
- `revs-seed replay` plays the ratings into a server over time, as
  their authors would have pushed them, and measures what each step
  costs: uploads, epoch cuts, and a subscriber's syncs.

## Getting the data

Use one of the current MovieLens datasets: `ml-latest`, `ml-25m`,
`ml-32m`, or `ml-latest-small` for something quick. They are in the
project's private datasets repo, or can be downloaded from GroupLens.
A dataset is a directory holding:

| File | Columns | Needed |
|------|---------|--------|
| `movies.csv` | `movieId,title,genres` | yes |
| `ratings-1.csv`, `ratings-2.csv`, ... or `ratings.csv` | `userId,movieId,rating,timestamp` | yes |
| `links.csv` | `movieId,imdbId,tmdbId` | no; gives items IMDb and TMDB refs |

Other files in the directory (`tags.csv`, the README) are ignored.

### Split ratings

`ratings.csv` runs to gigabytes in the larger datasets, so it can be
split into parts named `ratings-1.csv`, `ratings-2.csv`, and so on.
When `ratings-1.csv` exists, the parts are read in order as if they
were one file (and a `ratings.csv` beside them is ignored); otherwise
`ratings.csv` is read.

- Number the parts from 1 with no gaps. The numbers may be
  zero-padded (`ratings-01.csv`). A gap (`ratings-3.csv` missing while
  `ratings-4.csv` exists), or two names for one part (`ratings-1.csv`
  and `ratings-01.csv`), is an error.
- Split by lines, not bytes: each part but the last must end with a
  newline. A user's ratings may run on from one part into the next.
- `ratings-1.csv` must start with the header line; later parts may
  repeat it or not.

GNU `split` makes such parts directly (here, 5 million lines each,
named `ratings-01.csv`, `ratings-02.csv`, ...):

```sh
split -l 5000000 --numeric-suffixes=1 --additional-suffix=.csv ratings.csv ratings-
```

The MovieLens license doesn't allow redistributing the data, so never
commit any of it here; the tests use small synthetic files.

## Building

```sh
make build            # builds bin/revs-seed with the other binaries
# or
go build -o bin/revs-seed ./cmd/revs-seed
```

`bin/revs-seed -h` lists the commands; `bin/revs-seed COMMAND -h`
lists a command's flags.

## How ratings become documents

- **Authors.** Each MovieLens user becomes an author. Their key is
  derived from `-salt` (default `revs-seed`) and their user id, so the
  same dataset and salt always give the same authors and the same
  documents. Use a different salt to get a separate set of authors
  from the same data, for example to replay twice into one server.
- **Items.** Each rated movie is a `movie` item with one entry: the
  rating and the date it was made (UTC). Titles are cleaned up:
  `"Matrix, The (1999)"` becomes title `The Matrix`, year `1999`;
  alternative titles in parentheses are dropped. `links.csv` adds
  `imdb` (`tt0133093`) and `tmdb` refs. Genres are dropped.
- **Scale.** MovieLens rates in half stars from 0.5 to 5.0. Ratingset
  ratings are integers, so documents use the scale 1 to 10 and each
  rating is doubled (3.5 stars is 7).
- **Order.** Items are listed in the order the user rated them, so a
  later revision of a document mostly adds lines at the end, as a real
  client would.
- **Skipped.** Ratings of movies missing from `movies.csv` are left
  out and counted. Users with nothing to include get no document.

Choosing users works the same way for both commands:

- `-sample 0.1` keeps about 10% of the users. Which users are kept
  depends only on their ids, so every run picks the same ones, and a
  10% sample is part of any larger sample.
- `-users 1000` stops after 1000 users (counted after sampling).

## Exporting a corpus

```sh
bin/revs-seed export -o /tmp/corpus ~/datasets/ml-latest
```

This writes, into the output directory (which must be empty or not
exist yet):

- `<userId>.sjsonl`: each user's signed ratingset, ready to upload.
- `index.csv`: one row per document: `userId,authorId,revision,ratings,bytes`,
  plus `privateKey` with `-keys`.
- `export.json`: the settings and totals of the export.

Useful flags:

| Flag | Effect |
|------|--------|
| `-until 2010-12-31` | Only ratings made on or before that day: the corpus as it stood then. |
| `-revision 3` | The revision the documents carry (default 1). |
| `-keys` | Add each author's private key to `index.csv` (base64, as `revs` uses), to push as that author later. |
| `-sample`, `-users` | Choose users, as above. |
| `-salt` | Choose the authors' keys, as above. |
| `-j 4` | How many documents to build at once (default one per CPU). |
| `-q` | No progress output. |

Each document can be uploaded as it is, for example to a local
`revs server`:

```sh
curl --data-binary @/tmp/corpus/1.sjsonl -H 'Content-Type: application/jsonl' \
  http://localhost:1323/upload
```

The export runs at roughly 100k ratings a second, and the documents
take about 140 bytes a rating: a full 32-million-rating dataset makes
roughly 4.5 GB.

## Replaying ratings over time

```sh
bin/revs-seed replay -sample 0.05 -csv /tmp/replay.csv ~/datasets/ml-latest
```

The replay splits the ratings into steps: by default a year at a time
from the first rating to the last. At each step:

1. Every author who rated something during the step pushes their
   updated document through the real revs client, which uploads a
   patch when that's smaller than the document. Each author has their
   own client, as each would have their own app.
2. The server cuts an epoch.
3. A subscriber client syncs, the way any reader of the server would.

It prints a row per step, for example (300 users of a synthetic
dataset):

```
step  until        authors     ratings   pushed     new  patches    full      up MB    docs MB      push       cut   epoch   sync MB      sync
   1  1996-12-27       219         724      219     219        0     219       0.16       0.16     219ms     130ms       1      0.07      97ms
   2  1997-12-27       270        1513      226      51      119     107       0.20       0.26     413ms     209ms       2      0.09     153ms
   7  2002-12-27       300        5256      225       0      222       3       0.20       0.71     509ms     321ms       7      0.10     260ms
```

| Column | Meaning |
|--------|---------|
| `until` | The step covers ratings made before this date. |
| `authors`, `ratings` | Authors with a document so far, and the ratings in them. |
| `pushed`, `new` | Authors who pushed a new revision this step, and how many of them pushed for the first time. |
| `patches`, `full` | Uploads sent as sjsonl patches, and as full documents. |
| `up MB` | Bytes uploaded. |
| `docs MB` | Bytes of the pushed documents in full: what the uploads would have been without patches. |
| `push`, `cut` | Time spent pushing, and cutting the epoch. |
| `epoch` | The server's epoch after the cut. It only advances when something changed. |
| `sync MB`, `sync` | What the subscriber downloaded to catch up, and how long that took. |

A summary follows on standard error: the totals, and how much the
patches saved. `-csv FILE` writes every step's measures to a file,
including a few the table leaves out (the corpus size, the
subscriber's request count, whether it bootstrapped from a snapshot,
and how many documents it saw change).

### Choosing the steps

| Flag | Effect |
|------|--------|
| `-every 6m` | Step length: a count and `y`, `m`, `w` or `d` (default `1y`). |
| `-from 2005-01-01` | Start here instead of at the first rating. Ratings before it all arrive in the first step. |
| `-to 2010-01-01` | Stop before this date instead of after the last rating. |

### The server

By default the server runs inside the replay, with its data in a
temporary directory that is removed afterwards. Its publishing can be
tuned:

| Flag | Effect |
|------|--------|
| `-snapshot-every 10` | Publish a snapshot every 10 epochs (the server's default is 100). |
| `-compact-every 4,16` | Publish compacted patches spanning 4 and 16 epochs (default `24,168`), or `none`. |
| `-v` | Show the server's log (each epoch cut and what it published). |

`-dir DIR` keeps everything in `DIR` instead: the server under
`DIR/server` (a normal server data directory, published files in
`DIR/server/public/v1`), and the clients' databases (`authors.db`,
`subscriber.db`). That's useful for looking at what was published
afterwards, or serving it again with `revs server --data DIR/server`.

`-server URL` pushes to a running server instead. That server cuts
epochs on its own schedule, so the subscriber sees whatever it has
published by the end of each step. Use a server you don't mind
filling, and a fresh `-salt` for each run so the authors don't
collide with an earlier run's.

### Speed

The selected users' ratings are held in memory (about 24 bytes a
rating), so for the larger datasets start with `-sample 0.01` or
`-users`, then increase.

Every push and sync commits to SQLite databases, as a real client and
server would, and on a slow disk most of the time goes on those
commits. Put `-dir` on tmpfs (for example `-dir /dev/shm/replay`) to
measure just the processing. `-cpuprofile FILE` writes a CPU profile
for `go tool pprof`. `-j` sets how many authors push at once (default
one per CPU).

## Tests

`go test ./internal/movielens ./cmd/revs-seed` runs on small synthetic
files and needs no data. To also check a real dataset, point
`REVS_MOVIELENS` at it:

```sh
REVS_MOVIELENS=~/datasets/ml-latest go test ./internal/movielens -run TestDataset -v
```

That builds, signs and verifies every user's document and logs the
totals. Add `-short` to stop after the first 1000 users.

## Using the package

`cmd/revs-seed` is a thin wrapper; Go code in this module can use the
package directly:

```go
ds, err := movielens.Open("/path/to/ml-latest")        // loads the catalog
err = ds.Users(func(u movielens.User) error {            // streams the ratings
	doc, n, skipped, err := ds.Catalog.Document("salt", u, 0, 1) // signed, revision 1
	...
})
```

- `Catalog.Content` gives a user's unsigned document as of a time,
  the input `client.Push` takes; `Key` and `AuthorID` give the user's
  key and author id.
- `Dataset.Export` and `Replay` are the two commands. `Replay` needs
  only a server URL and, optionally, a function to cut epochs, so
  tests can run it against a `revstest` server (see `replay_test.go`).
