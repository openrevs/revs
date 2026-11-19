package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openrevs/revs/collection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var defaults = options{Scale: 5}

func TestParseLine(t *testing.T) {
	tests := []struct {
		in   string
		opts *options
		want entry // hasMeta and strong aren't compared
	}{
		{in: "Pulp Fiction (1994) ★★★★½ - Still great.",
			want: entry{Title: "Pulp Fiction", Year: "1994", Rating: "4.5", Review: "Still great."}},
		{in: "Heat, 1995, 8/10, rewatched on March 3, 2024",
			want: entry{Title: "Heat", Year: "1995", Rating: "4", Date: "2024-03-03"}},
		{in: "Severance S02E03 4/5 tt11280740",
			want: entry{Title: "Severance", Type: "tv", Season: "2", Episode: "3", Rating: "4", IMDB: "tt11280740"}},
		{in: `The Thing 1982 rating: 9/10 "Practical effects hold up"`,
			want: entry{Title: "The Thing", Year: "1982", Rating: "4.5", Review: "Practical effects hold up"}},
		{in: "Blade Runner 2049 (2017) 3.5 stars",
			want: entry{Title: "Blade Runner 2049", Year: "2017", Rating: "3.5"}},
		{in: "1917 (2019) **** — tense",
			want: entry{Title: "1917", Year: "2019", Rating: "4", Review: "tense"}},
		{in: "Apollo 13 1995 4.5",
			want: entry{Title: "Apollo 13", Year: "1995", Rating: "4.5"}},
		{in: "Breaking Bad (2008–2013)",
			want: entry{Title: "Breaking Bad", Year: "2008", Type: "tv"}},
		{in: "***1/2 Alien (1979) - chestburster!",
			want: entry{Title: "Alien", Year: "1979", Rating: "3.5", Review: "chestburster!"}},
		{in: "★★★ 1/2 Aliens",
			want: entry{Title: "Aliens", Rating: "3.5"}},
		{in: "The Wire [TV] 5/5",
			want: entry{Title: "The Wire", Type: "tv", Rating: "5"}},
		{in: "https://www.imdb.com/title/tt0110912/ Pulp Fiction 5/5",
			want: entry{Title: "Pulp Fiction", IMDB: "tt0110912", Rating: "5"}},
		{in: "Arrival https://www.themoviedb.org/movie/329865-arrival",
			want: entry{Title: "Arrival", TMDB: "329865"}},
		{in: "Jaws\t1975\t4\tgreat shark",
			want: entry{Title: "Jaws", Year: "1975", Rating: "4", Review: "great shark"}},
		{in: "Arrival, 3.5",
			want: entry{Title: "Arrival", Rating: "3.5"}},
		{in: "Dune: Part Two (2024) 4/5: Spectacle.",
			want: entry{Title: "Dune: Part Two", Year: "2024", Rating: "4", Review: "Spectacle."}},
		{in: "Up (2009) 4/5. Cried. Watched 2023-04-01 with the kids.",
			want: entry{Title: "Up", Year: "2009", Rating: "4", Date: "2023-04-01", Review: "Cried. Watched 2023-04-01 with the kids."}},
		{in: "Heat 7", opts: &options{Scale: 10},
			want: entry{Title: "Heat 7"}}, // like "Toy Story 3": no separator, so part of the title
		{in: "Heat, 7", opts: &options{Scale: 10},
			want: entry{Title: "Heat", Rating: "3.5"}},
		{in: "Heat 03/04/2023", opts: &options{Scale: 5, DayFirst: true},
			want: entry{Title: "Heat", Date: "2023-04-03"}},
		{in: "Heat 03/04/2023",
			want: entry{Title: "Heat", Date: "2023-03-04"}},
		{in: "Heat 4 April 2023 3 out of 4",
			want: entry{Title: "Heat", Date: "2023-04-04", Rating: "4"}},
		{in: "The Office Season 3, Episode 12 rating: 4",
			want: entry{Title: "The Office", Type: "tv", Season: "3", Episode: "12", Rating: "4"}},
		{in: "Fargo s1 0/10",
			want: entry{Title: "Fargo", Type: "tv", Season: "1", Rating: "0.5"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			o := defaults
			if tt.opts != nil {
				o = *tt.opts
			}
			got, ok := parseLine(tt.in, o)
			if tt.want.Year == "" && tt.want.Rating == "" && tt.want.Date == "" && tt.want.Type == "" && tt.want.IMDB == "" && tt.want.TMDB == "" {
				// No values: a title-only line.
				if ok {
					assert.Equal(t, tt.want.Title, got.Title)
					assert.Empty(t, got.Rating)
				}
				return
			}
			require.True(t, ok, "no values found")
			got.hasMeta, got.strong = false, false
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNoValues(t *testing.T) {
	for _, s := range []string{"Toy Story 3", "Ocean's 11", "Heat", "Blade Runner 2049"} {
		_, ok := parseLine(s, defaults)
		assert.False(t, ok, s)
	}
}

func TestConvert(t *testing.T) {
	text := `# Movies 2023

Pulp Fiction (1994) ★★★★½
  Still great.
  Better every time.

  The diner scene!
Toy Story 3
- Arrival
- Heat (1995) 4/5
Blade Runner 2049 (2017) 3/5
This one took a while to grow on me, I'll admit.
> Quoted note.

## Up (2009) 4/5

Cried.

---

Some closing prose that runs on and on, far too long and wordy to be a title.
`
	entries, skips := convert(text, defaults)
	var got []string
	for _, e := range entries {
		got = append(got, e.Title+"|"+e.Rating+"|"+e.Review)
	}
	assert.Equal(t, []string{
		"Pulp Fiction|4.5|Still great. Better every time.\n\nThe diner scene!",
		"Toy Story 3||",
		"Arrival||",
		"Heat|4|",
		"Blade Runner 2049|3|This one took a while to grow on me, I'll admit. Quoted note.",
		"Up|4|Cried.\n\nSome closing prose that runs on and on, far too long and wordy to be a title.",
	}, got)
	assert.Empty(t, skips)
}

func TestSkipsProse(t *testing.T) {
	entries, skips := convert("Here are some movies I watched this year, in no particular order.\nHeat (1995) 4/5\n", defaults)
	require.Len(t, entries, 1)
	assert.Equal(t, "Heat", entries[0].Title)
	require.Len(t, skips, 1)
	assert.Equal(t, 1, skips[0].line)
}

// TestImports checks that revs imports the output.
func TestImports(t *testing.T) {
	in := strings.Join([]string{
		`Pulp Fiction (1994) ★★★★½ - Still great, "really". Watched 2023-04-01.`,
		"Severance S02E03 4/5",
		"Toy Story 3",
		"Heat 1995",
	}, "\n")
	var out, errs bytes.Buffer
	require.NoError(t, run(context.Background(), nil, strings.NewReader(in), &out, &errs))
	assert.Equal(t, "4 titles converted\n", errs.String())
	assert.Equal(t, `title,year,type,rating,view_date,season,episode,review
Pulp Fiction,1994,,4.5,2023-04-01,,,"Still great, ""really"". Watched 2023-04-01."
Severance,,tv,4,,2,3,
Toy Story 3,,,,,,,
Heat,1995,,,,,,
`, out.String())

	c, err := collection.Open(collection.Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer c.Close()
	stats, err := c.ImportCSV(context.Background(), out.Bytes())
	require.NoError(t, err)
	assert.Equal(t, 4, stats.MoviesAdded)
	assert.Equal(t, 2, stats.ViewingsAdded)
}

func TestNothingFound(t *testing.T) {
	var out, errs bytes.Buffer
	err := run(context.Background(), nil, strings.NewReader("Just a long sentence about nothing in particular, really.\n"), &out, &errs)
	assert.ErrorContains(t, err, "no titles found")
	assert.Empty(t, out.String())
}
