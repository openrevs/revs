package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/openrevs/revs/collection"
)

// defaultModel is the model -ai uses unless -model says otherwise.
const defaultModel = "claude-haiku-5-5"

// Chunking: the text goes to the model a piece at a time, so that each
// answer stays well within the output limit and pieces can run at once.
const (
	chunkLines  = 120 // lines per request, at most
	parallelism = 4   // requests in flight at once
	maxTokens   = 16000
)

const systemPrompt = `You extract movie and TV ratings from someone's personal notes so they can be imported into a ratings app.

The user message is the text of the notes, each line prefixed with its line number ("L12: ..."). Find every movie or TV series the notes record as watched, rated, or reviewed, and return one entry for each, in order.

For each entry:
- line: the line number where the entry starts.
- title: the title only, as the notes give it, without the year, rating, or other details. Fix obvious capitalization only; don't guess a different title.
- year: the release year (4 digits) if the notes give one, else "". A year that is clearly when it was watched is not the release year.
- type: "tv" for a TV series (or a season or episode of one), else "movie".
- season, episode: numbers, for a TV entry about a particular season or episode, else "".
- rating_value, rating_max: the rating exactly as the notes give it, as a number and the top of its scale: "4/5" is "4" and "5"; "8/10" is "8" and "10"; "★★★½" or "***1/2" is "3.5" and "5"; "B+" is "8.5" and "10" (letter grades: A+ 10, A 9.5, A- 9, B+ 8.5, B 8, B- 7.5, C+ 7, C 6.5, C- 6, D 5, F 2). A bare number with no scale: use rating_max "{{SCALE}}" unless the notes make the scale clear. No rating: both "".
- view_date: the date it was watched, as YYYY-MM-DD, if the notes give a full date, else "". Numeric dates like 03/04/2023 are {{DATEORDER}}. A heading or nearby text may give the date for the entries under it.
- imdb_id: an IMDb id like tt0110912 if the notes give one, else "".
- tmdb_id: a TMDB id (the number in a themoviedb.org URL) if the notes give one, else "".
- review: the person's own comments on it, verbatim (lines that continue the entry included, joined with spaces, paragraphs separated by a blank line), without the title, year, or rating. "" if there are none.

Skip headings, preambles, and anything that isn't an entry. Never invent an entry or a detail the notes don't contain.`

// llmEntry is one entry as the model returns it.
type llmEntry struct {
	Line        int    `json:"line"`
	Title       string `json:"title"`
	Year        string `json:"year"`
	Type        string `json:"type"`
	Season      string `json:"season"`
	Episode     string `json:"episode"`
	RatingValue string `json:"rating_value"`
	RatingMax   string `json:"rating_max"`
	ViewDate    string `json:"view_date"`
	IMDB        string `json:"imdb_id"`
	TMDB        string `json:"tmdb_id"`
	Review      string `json:"review"`
}

// responseSchema is the JSON schema the model's answer must follow.
var responseSchema = func() map[string]any {
	str := map[string]any{"type": "string"}
	props := map[string]any{"line": map[string]any{"type": "integer"}}
	var required []string
	for _, k := range []string{"line", "title", "year", "type", "season", "episode", "rating_value", "rating_max", "view_date", "imdb_id", "tmdb_id", "review"} {
		required = append(required, k)
		if k != "line" {
			props[k] = str
		}
	}
	props["type"] = map[string]any{"type": "string", "enum": []string{"movie", "tv"}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"entries": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"properties":           props,
					"required":             required,
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"entries"},
		"additionalProperties": false,
	}
}()

// extractor finds entries with a Claude model.
type extractor struct {
	client anthropic.Client
	model  string
	opts   options
	log    io.Writer // warnings
}

func newExtractor(model string, o options, log io.Writer, reqOpts ...option.RequestOption) *extractor {
	return &extractor{client: anthropic.NewClient(reqOpts...), model: model, opts: o, log: log}
}

// chunk is a run of lines of the text, first numbering them from 1.
type chunk struct {
	first int
	lines []string
}

// chunks splits text into pieces of at most chunkLines lines, breaking
// at a blank line where it can so an entry and its review stay together.
func chunks(text string) []chunk {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []chunk
	for start := 0; start < len(lines); {
		end := min(start+chunkLines, len(lines))
		if end < len(lines) {
			for i := end - 1; i > start+chunkLines/2; i-- {
				if strings.TrimSpace(lines[i]) == "" {
					end = i + 1
					break
				}
			}
		}
		c := chunk{first: start + 1, lines: lines[start:end]}
		if strings.TrimSpace(strings.Join(c.lines, "")) != "" {
			out = append(out, c)
		}
		start = end
	}
	return out
}

// extract finds the entries in text, sending its chunks to the model a
// few at a time. A chunk the model can't do is converted without it,
// with a warning.
func (x *extractor) extract(ctx context.Context, text string) ([]*entry, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	cs := chunks(text)
	results := make([][]*entry, len(cs))
	errs := make([]error, len(cs))

	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i], errs[i] = x.extractChunk(ctx, c)
		}()
	}
	wg.Wait()

	var all []*entry
	failed := 0
	for i, c := range cs {
		if errs[i] != nil {
			if ctx.Err() != nil || isFatal(errs[i]) {
				return nil, errs[i]
			}
			failed++
			last := c.first + len(c.lines) - 1
			fmt.Fprintf(x.log, "lines %d-%d: %s; converting them without the model\n", c.first, last, errs[i])
			es, _ := convert(strings.Join(c.lines, "\n"), x.opts)
			for _, e := range es {
				e.line += c.first - 1
			}
			results[i] = es
		}
		all = append(all, results[i]...)
	}
	if failed == len(cs) && len(cs) > 1 {
		return nil, fmt.Errorf("the model failed on every part of the text: %w", errs[0])
	}
	return all, nil
}

// isFatal reports whether err will happen for every request, so there
// is no point going on (a bad API key, an unknown model).
func isFatal(err error) bool {
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		switch apierr.StatusCode {
		case 400, 401, 403, 404:
			return true
		}
	}
	return false
}

// extractChunk asks the model for the entries in one chunk. If the
// answer would be too long, it splits the chunk in two and tries again.
func (x *extractor) extractChunk(ctx context.Context, c chunk) ([]*entry, error) {
	var b strings.Builder
	for i, l := range c.lines {
		fmt.Fprintf(&b, "L%d: %s\n", c.first+i, l)
	}

	scale := strconv.FormatFloat(x.opts.Scale, 'f', -1, 64)
	order := "month/day/year (US order)"
	if x.opts.DayFirst {
		order = "day/month/year"
	}
	system := strings.NewReplacer("{{SCALE}}", scale, "{{DATEORDER}}", order).Replace(systemPrompt)

	resp, err := x.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     x.model,
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: system}},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortMedium,
			Format: anthropic.JSONOutputFormatParam{Schema: responseSchema},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(b.String())),
		},
	})
	if err != nil {
		return nil, err
	}
	switch resp.StopReason {
	case anthropic.StopReasonRefusal:
		return nil, errors.New("the model declined to read this part")
	case anthropic.StopReasonMaxTokens:
		if len(c.lines) < 2 {
			return nil, errors.New("the model's answer was too long")
		}
		half := len(c.lines) / 2
		a, err := x.extractChunk(ctx, chunk{c.first, c.lines[:half]})
		if err != nil {
			return nil, err
		}
		z, err := x.extractChunk(ctx, chunk{c.first + half, c.lines[half:]})
		return append(a, z...), err
	}

	var text strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	var out struct {
		Entries []llmEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(text.String()), &out); err != nil {
		return nil, fmt.Errorf("reading the model's answer: %w", err)
	}

	var es []*entry
	for _, le := range out.Entries {
		if e := x.toEntry(le, c); e != nil {
			es = append(es, e)
		}
	}
	sort.SliceStable(es, func(i, j int) bool { return es[i].line < es[j].line })
	return es, nil
}

// toEntry converts the model's entry to a row, putting the rating on
// revs's scale and dropping values it got wrong (the row's check
// reports the rest).
func (x *extractor) toEntry(le llmEntry, c chunk) *entry {
	e := &entry{
		line:    le.Line,
		Title:   strings.TrimSpace(le.Title),
		Year:    strings.TrimSpace(le.Year),
		Date:    strings.TrimSpace(le.ViewDate),
		Season:  trimZeros(strings.TrimSpace(le.Season)),
		Episode: trimZeros(strings.TrimSpace(le.Episode)),
		IMDB:    strings.TrimSpace(le.IMDB),
		TMDB:    strings.TrimSpace(le.TMDB),
		Review:  strings.TrimSpace(le.Review),
		hasMeta: true,
	}
	if e.Title == "" {
		return nil
	}
	if e.line < c.first || e.line >= c.first+len(c.lines) {
		e.line = c.first
	}
	if le.Type == collection.TypeTV {
		e.Type = collection.TypeTV
	}
	if e.Type != collection.TypeTV {
		e.Season, e.Episode = "", ""
	}
	if v, ok := parseNum(strings.TrimSpace(le.RatingValue)); ok {
		max, err := strconv.ParseFloat(strings.TrimSpace(le.RatingMax), 64)
		if err != nil || max <= 0 {
			max = x.opts.Scale
		}
		if v <= max {
			e.Rating = stars(v, max)
		} else {
			fmt.Fprintf(x.log, "line %d: dropped rating %s/%s for %q: out of range\n", e.line, le.RatingValue, le.RatingMax, e.Title)
		}
	}
	return e
}
