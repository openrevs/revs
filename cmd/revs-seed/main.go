// Command revs-seed builds revs data at scale from the MovieLens
// datasets (https://grouplens.org/datasets/movielens/), for testing
// servers and clients with realistic amounts of data.
//
// Each MovieLens user becomes an author, with a key derived from a salt
// and the user's id, so the same dataset and salt always give the same
// authors and documents. See the internal/movielens package for how
// ratings are converted.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/openrevs/revs/internal/movielens"
	"github.com/openrevs/revs/ratingset"
)

const usage = `Usage: revs-seed COMMAND [flags] DATASET

Build revs data from a MovieLens dataset: a directory holding
movies.csv, ratings.csv and (for IMDb and TMDB refs) links.csv, as in
ml-latest, ml-25m or ml-32m.

Each MovieLens user becomes an author whose key is derived from -salt
and the user's id: the same dataset and salt always give the same
authors and documents. Ratings are half stars, doubled onto a 1-10
scale; each is dated by its timestamp.

Commands:
  export   write each user's signed ratingset to a directory

Run "revs-seed COMMAND -h" for a command's flags.
`

const exportUsage = `Usage: revs-seed export -o DIR [flags] DATASET

Write each selected user's signed ratingset to DIR as <userId>.sjsonl,
with index.csv (each user's MovieLens id, author id, revision, ratings
and size) and export.json (the settings used). DIR must be empty or
not exist.

Flags:
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "revs-seed:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return flag.ErrHelp
	}
	switch args[0] {
	case "export":
		return export(ctx, args[1:], stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stderr, usage)
		return flag.ErrHelp
	}
	return fmt.Errorf("unknown command %q (try -h)", args[0])
}

func export(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, exportUsage)
		fs.PrintDefaults()
	}
	out := fs.String("o", "", "the directory to write to (required)")
	salt := fs.String("salt", "revs-seed", "derive the authors' keys with this salt")
	until := fs.String("until", "", "include only ratings made on or before this `date` (YYYY-MM-DD, UTC)")
	rev := fs.Int("revision", 1, "the revision of every document")
	sample := fs.Float64("sample", 1, "keep about this `fraction` of the users, chosen by user id")
	limit := fs.Int("users", 0, "stop after this many users (after -sample); 0 for all")
	withKeys := fs.Bool("keys", false, "add each author's private key to index.csv")
	workers := fs.Int("j", 0, "documents to build at once; 0 for one per CPU")
	quiet := fs.Bool("q", false, "don't report progress")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("export needs one DATASET directory")
	}
	if *out == "" {
		return errors.New("export needs -o DIR")
	}
	if *rev < 1 {
		return fmt.Errorf("-revision %d is less than 1", *rev)
	}
	if *sample <= 0 || *sample > 1 {
		return fmt.Errorf("-sample %v is not in (0, 1]", *sample)
	}

	o := movielens.ExportOptions{
		Selection: movielens.Selection{Sample: *sample, Limit: *limit},
		Salt:      *salt,
		Revision:  *rev,
		Keys:      *withKeys,
		Workers:   *workers,
	}
	if *until != "" {
		t, err := time.Parse(ratingset.DateDay, *until)
		if err != nil {
			return fmt.Errorf("-until: %w", err)
		}
		o.Until = t.Add(24*time.Hour).Unix() - 1 // through the end of the day
	}

	start := time.Now()
	ds, err := movielens.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	if !*quiet {
		fmt.Fprintf(stderr, "catalog: %d movies\n", len(ds.Catalog))
		last := time.Now()
		o.Progress = func(s movielens.ExportStats) {
			if time.Since(last) >= 2*time.Second {
				last = time.Now()
				fmt.Fprintf(stderr, "%s\n", summary(s, time.Since(start)))
			}
		}
	}

	s, err := ds.Export(ctx, *out, o)
	if err != nil {
		return err
	}
	if !*quiet {
		fmt.Fprintf(stderr, "done: %s\n", summary(s, time.Since(start)))
	}
	return nil
}

func summary(s movielens.ExportStats, d time.Duration) string {
	str := fmt.Sprintf("%d users, %d ratings, %.1f MB in %v",
		s.Users, s.Ratings, float64(s.Bytes)/(1<<20), d.Round(time.Second))
	if s.Empty > 0 {
		str += fmt.Sprintf("; %d users with no ratings to include", s.Empty)
	}
	if s.Skipped > 0 {
		str += fmt.Sprintf("; %d ratings of movies not in movies.csv", s.Skipped)
	}
	return str
}
