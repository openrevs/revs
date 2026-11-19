// Command revs-txt2csv converts free-form text notes about movies and
// TV series into a CSV file that revs can import.
//
// Each line that names a title becomes a row. The year, rating, viewing
// date, season and episode, and IMDb or TMDB ids are picked out of the
// line wherever they appear, in most common notations; the text before
// them is the title and the text after them the review. Lines that
// follow an entry without values of their own (or that are indented or
// quoted under it) continue its review.
//
// With -ai, a Claude model (Claude Haiku 5.5 by default) reads the notes
// instead, which copes with much freer writing. The notes are sent to
// the Anthropic API, using the key in ANTHROPIC_API_KEY. Its answers
// are checked the same way, and any part of the text it fails on is
// converted without it.
//
// The conversion is a best guess: read the result before importing it.
// Lines that couldn't be read are reported on standard error.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

const usage = `Usage: revs-txt2csv [flags] [FILE...]

Convert free-form text notes about movies and TV series into a CSV
file for revs's import. Reads the files named, or standard input, and
writes CSV to standard output (or -o).

Each line that names a title becomes a row, for example:

  Pulp Fiction (1994) ★★★★½ - Still great. Watched 2023-04-01.
  - Heat, 1995, 8/10, rewatched on March 3, 2024
  Severance S02E03 4/5 tt11280740
  The Thing 1982 rating: 9 "Practical effects hold up"

Recognized: years ("(1994)", "1994"), ratings ("4/5", "8/10", "3.5
stars", "★★★½", "***1/2", "rating: 4", or a bare number on -scale),
dates ("2023-04-01", "April 1, 2023", "4/1/2023"), TV seasons and
episodes ("S02E03", "Season 2"), "(TV)", and IMDb and TMDB ids or
URLs. Lines below an entry that have no values of their own are added
to its review; markdown headings, lists, and quotes are understood.

With -ai, a Claude model reads the notes instead, which handles much
freer writing (prose, letter grades, dates given in headings). This
sends the notes to the Anthropic API and needs an API key in
ANTHROPIC_API_KEY.

Flags:
`

// apiOptions are extra options for the API client; tests point it at a
// fake server.
var apiOptions []option.RequestOption

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "revs-txt2csv:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("revs-txt2csv", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	var (
		o       options
		out     = fs.String("o", "", "write the CSV to `FILE` instead of standard output")
		verbose = fs.Bool("v", false, "list every line skipped, and every row written")
		strict  = fs.Bool("strict", false, "fail if any line can't be converted, instead of skipping it")
		ai      = fs.Bool("ai", false, "have a Claude model read the notes (sends them to the Anthropic API; needs ANTHROPIC_API_KEY)")
		model   = fs.String("model", defaultModel, "the Claude `MODEL` -ai uses")
	)
	fs.Float64Var(&o.Scale, "scale", 5, "the top of the scale for ratings given as a bare number, such as \"7\" (10 for out-of-10 notes)")
	fs.BoolVar(&o.DayFirst, "day-first", false, "read numeric dates like 03/04/2023 as day/month/year (default month/day/year)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.Scale <= 0 {
		return fmt.Errorf("-scale must be positive")
	}

	var text strings.Builder
	if fs.NArg() == 0 {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		text.Write(b)
	}
	for _, name := range fs.Args() {
		b, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		text.Write(b)
		text.WriteString("\n\n")
	}

	var (
		entries []*entry
		skips   []skipped
	)
	if *ai {
		x := newExtractor(*model, o, stderr, apiOptions...)
		var err error
		if entries, err = x.extract(ctx, text.String()); err != nil {
			return fmt.Errorf("-ai: %w", err)
		}
	} else {
		entries, skips = convert(text.String(), o)
	}
	var good []*entry
	var bad int
	for _, e := range entries {
		if err := e.check(); err != nil {
			bad++
			fmt.Fprintf(stderr, "line %d: skipped %q: %s\n", e.line, e.Title, strings.ReplaceAll(err.Error(), "\n", "; "))
			continue
		}
		if *verbose {
			fmt.Fprintf(stderr, "line %d: %s\n", e.line, e)
		}
		good = append(good, e)
	}
	if *verbose {
		for _, s := range skips {
			fmt.Fprintf(stderr, "line %d: no title found: %s\n", s.line, s.text)
		}
	}
	if *strict && (bad > 0 || len(skips) > 0) {
		return fmt.Errorf("%d lines couldn't be converted (-v lists them)", bad+len(skips))
	}
	if len(good) == 0 {
		return fmt.Errorf("no titles found")
	}

	var buf bytes.Buffer
	if err := writeCSV(&buf, good); err != nil {
		return err
	}
	if *out != "" {
		if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
			return err
		}
	} else if _, err := stdout.Write(buf.Bytes()); err != nil {
		return err
	}

	msg := fmt.Sprintf("%d titles converted", len(good))
	if n := bad + len(skips); n > 0 {
		msg += fmt.Sprintf(", %d lines skipped", n)
		if !*verbose && len(skips) > 0 {
			msg += " (-v lists them)"
		}
	}
	fmt.Fprintln(stderr, msg)
	return nil
}
