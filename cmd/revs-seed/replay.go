package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/openrevs/revs/internal/movielens"
	"github.com/openrevs/revs/ratingset"
	"github.com/openrevs/revs/server"
)

const replayUsage = `Usage: revs-seed replay [flags] DATASET

Play the selected users' ratings into a server over time, as their
authors would have pushed them, and measure what it costs. The ratings
are taken in steps (-every, from -from to -to): at each step, every
author with new ratings pushes their updated document through the revs
client (as a patch when that's smaller); then the server cuts an epoch,
and a subscriber syncs it.

Without -server, the server runs in this process, its data under -dir,
and cuts an epoch after each step. With -server, the documents go to
that server, which cuts epochs on its own schedule; use a server you
don't mind filling, with -salt to tell runs apart.

Each step prints a row: authors and ratings so far; authors who pushed
(and how many were new); uploads sent as patches and as full documents,
the bytes uploaded and the bytes of the documents they stand for; the
time to push and to cut; and the subscriber's sync: epoch, bytes and
time. -csv writes every measure to a file.

The selected users' ratings are held in memory (about 24 bytes a
rating); use -sample or -users for a large dataset.

The client and in-process server databases sync to disk as they would
in use, which on a slow disk can be most of the time taken. A -dir on
tmpfs (e.g. /dev/shm) leaves just the processing.

Flags:
`

func replay(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, replayUsage)
		fs.PrintDefaults()
	}
	salt := fs.String("salt", "revs-seed", "derive the authors' keys with this salt")
	sample := fs.Float64("sample", 1, "keep about this `fraction` of the users, chosen by user id")
	limit := fs.Int("users", 0, "stop after this many users (after -sample); 0 for all")
	from := fs.String("from", "", "start at this `date` (YYYY-MM-DD, UTC); default the first rating")
	to := fs.String("to", "", "end before this `date`; default after the last rating")
	every := fs.String("every", "1y", "the length of a step: a count and y, m, w or d")
	url := fs.String("server", "", "push to the server at this `URL` instead of one in this process")
	dir := fs.String("dir", "", "keep the client (and in-process server) data in this directory, which must be empty; default a temporary one, removed after")
	snapshotEvery := fs.Int("snapshot-every", 0, "in-process server: publish a snapshot every this many epochs (0 for the default)")
	compactEvery := fs.String("compact-every", "", "in-process server: compacted patch `spans`, comma-separated epochs, or \"none\" (default the server's)")
	workers := fs.Int("j", 0, "authors pushing at once; 0 for one per CPU")
	csvFile := fs.String("csv", "", "also write each step's measures to this CSV `file`")
	verbose := fs.Bool("v", false, "log the in-process server's work (epoch cuts and so on)")
	cpuProfile := fs.String("cpuprofile", "", "write a CPU profile of the replay to this `file`")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("replay needs one DATASET directory")
	}
	if *sample <= 0 || *sample > 1 {
		return fmt.Errorf("-sample %v is not in (0, 1]", *sample)
	}

	o := movielens.ReplayOptions{
		Selection: movielens.Selection{Sample: *sample, Limit: *limit},
		Salt:      *salt,
		Workers:   *workers,
	}
	var err error
	if o.From, err = parseDate("-from", *from); err != nil {
		return err
	}
	if o.To, err = parseDate("-to", *to); err != nil {
		return err
	}
	if !o.From.IsZero() && !o.To.IsZero() && !o.From.Before(o.To) {
		return errors.New("-from must be before -to")
	}
	if o.Every, err = movielens.ParseInterval(*every); err != nil {
		return fmt.Errorf("-every: %w", err)
	}
	var srvOpts server.Options
	srvOpts.SnapshotEvery = *snapshotEvery
	if *compactEvery != "" {
		if *url != "" {
			return errors.New("-compact-every is for the in-process server, not -server")
		}
		if srvOpts.CompactEvery, err = parseSpans(*compactEvery); err != nil {
			return err
		}
	}
	if *snapshotEvery != 0 && *url != "" {
		return errors.New("-snapshot-every is for the in-process server, not -server")
	}

	if *dir == "" {
		d, err := os.MkdirTemp("", "revs-seed-replay-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(d)
		o.Dir = d
	} else {
		if err := os.MkdirAll(*dir, 0o755); err != nil {
			return err
		}
		if ents, err := os.ReadDir(*dir); err != nil {
			return err
		} else if len(ents) > 0 {
			return fmt.Errorf("%s is not empty", *dir)
		}
		o.Dir = *dir
	}

	var csvOut *csv.Writer
	if *csvFile != "" {
		f, err := os.Create(*csvFile)
		if err != nil {
			return err
		}
		defer f.Close()
		csvOut = csv.NewWriter(f)
		defer csvOut.Flush()
		csvOut.Write(csvHeader)
	}

	ds, err := movielens.Open(fs.Arg(0))
	if err != nil {
		return err
	}

	if !*verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	}
	if *url != "" {
		o.URL = *url
		o.HTTPClient = &http.Client{Timeout: time.Minute}
	} else {
		srv, err := server.Open(filepath.Join(o.Dir, "server"), srvOpts)
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		hs := &http.Server{Handler: srv.Handler()}
		go hs.Serve(ln)
		defer hs.Close()
		o.URL = "http://" + ln.Addr().String()
		o.Cut = func(ctx context.Context) error {
			_, err := srv.Cut(ctx)
			return err
		}
	}

	fmt.Fprintf(stderr, "catalog: %d movies; ratings from %s; replaying into %s\n", len(ds.Catalog), ratingsFiles(ds), o.URL)
	// Fixed widths rather than a tabwriter, so rows print as they come.
	const row = "%4v  %-10v  %8v  %10v  %7v  %6v  %7v  %6v  %9v  %9v  %8v  %8v  %6v  %8v  %8v\n"
	fmt.Fprintf(stdout, row, "step", "until", "authors", "ratings", "pushed", "new", "patches", "full",
		"up MB", "docs MB", "push", "cut", "epoch", "sync MB", "sync")
	var total movielens.StepStats
	o.Step = func(s movielens.StepStats) {
		fmt.Fprintf(stdout, row,
			s.Step, s.Until.Format(ratingset.DateDay), s.Authors, s.Ratings, s.Pushed, s.New,
			s.Patches, s.Full, mb(s.UploadBytes), mb(s.DocBytes),
			dur(s.PushTime), dur(s.CutTime), s.Epoch, mb(s.SyncBytes), dur(s.SyncTime))
		if csvOut != nil {
			csvOut.Write(csvRow(s))
			csvOut.Flush()
		}
		total.UploadBytes += s.UploadBytes
		total.DocBytes += s.DocBytes
		total.SyncBytes += s.SyncBytes
		total.PushTime += s.PushTime
		total.CutTime += s.CutTime
		total.SyncTime += s.SyncTime
		total.Patches += s.Patches
		total.Full += s.Full
		total.Corpus = s.Corpus
	}

	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}

	start := time.Now()
	steps, err := movielens.Replay(ctx, ds, o)
	if err != nil {
		return err
	}
	saved := 0.0
	if total.DocBytes > 0 {
		saved = 100 * (1 - float64(total.UploadBytes)/float64(total.DocBytes))
	}
	fmt.Fprintf(stderr, "done: %d steps in %v. Uploads: %d patches and %d documents, %s MB for %s MB of documents (%.0f%% saved). "+
		"Subscriber: %s MB fetched for a corpus of %s MB. Time pushing %v, cutting %v, syncing %v.\n",
		len(steps), time.Since(start).Round(time.Second), total.Patches, total.Full,
		mb(total.UploadBytes), mb(total.DocBytes), saved, mb(total.SyncBytes), mb(total.Corpus),
		total.PushTime.Round(time.Millisecond), total.CutTime.Round(time.Millisecond), total.SyncTime.Round(time.Millisecond))
	if csvOut != nil {
		csvOut.Flush()
		return csvOut.Error()
	}
	return nil
}

var csvHeader = []string{
	"step", "until", "authors", "ratings", "corpus_bytes",
	"pushed", "new", "patches", "full", "upload_bytes", "doc_bytes", "push_ms",
	"cut_ms", "epoch", "sync_requests", "sync_bytes", "sync_ms", "bootstrapped", "changed",
}

func csvRow(s movielens.StepStats) []string {
	i := strconv.Itoa
	i64 := func(n int64) string { return strconv.FormatInt(n, 10) }
	ms := func(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }
	return []string{
		i(s.Step), s.Until.Format(time.RFC3339), i(s.Authors), i(s.Ratings), i64(s.Corpus),
		i(s.Pushed), i(s.New), i(s.Patches), i(s.Full), i64(s.UploadBytes), i64(s.DocBytes), ms(s.PushTime),
		ms(s.CutTime), i(s.Epoch), i(s.SyncRequests), i64(s.SyncBytes), ms(s.SyncTime),
		strconv.FormatBool(s.Bootstrapped), i(s.Changed),
	}
}

func parseDate(flag, s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(ratingset.DateDay, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", flag, err)
	}
	return t, nil
}

// parseSpans reads -compact-every: "none", or comma-separated epoch
// counts.
func parseSpans(s string) ([]int, error) {
	if s == "none" {
		return []int{}, nil
	}
	var out []int
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 2 {
			return nil, fmt.Errorf("-compact-every: %q is not a span of 2 or more epochs", f)
		}
		out = append(out, n)
	}
	return out, nil
}

func mb(n int64) string { return strconv.FormatFloat(float64(n)/(1<<20), 'f', 2, 64) }

func dur(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(10 * time.Millisecond).String()
}
