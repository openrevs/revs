package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAPI is a Messages API that answers each request with reply(the
// user message text), and records the requests.
type fakeAPI struct {
	reply func(userText string) (status int, stopReason, text string)

	mu   sync.Mutex
	reqs []map[string]any
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()

	msgs := req["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	userText := content[0].(map[string]any)["text"].(string)

	status, stop, text := f.reply(userText)
	w.Header().Set("Content-Type", "application/json")
	if status != 200 {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"type": "authentication_error", "message": "invalid x-api-key"}})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_test", "type": "message", "role": "assistant", "model": req["model"],
		"content":     []any{map[string]any{"type": "text", "text": text}},
		"stop_reason": stop,
		"usage":       map[string]any{"input_tokens": 10, "output_tokens": 10},
	})
}

func withFakeAPI(t *testing.T, f *fakeAPI) {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	old := apiOptions
	apiOptions = []option.RequestOption{option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0)}
	t.Cleanup(func() { apiOptions = old })
}

func answer(es ...llmEntry) string {
	for i := range es {
		if es[i].Type == "" {
			es[i].Type = "movie"
		}
	}
	b, _ := json.Marshal(map[string]any{"entries": es})
	return string(b)
}

func TestAI(t *testing.T) {
	f := &fakeAPI{reply: func(string) (int, string, string) {
		return 200, "end_turn", answer(
			llmEntry{Line: 2, Title: "Heat", Year: "1995", RatingValue: "B+", RatingMax: "10"}, // not a number: dropped
			llmEntry{Line: 1, Title: "Pulp Fiction", Year: "1994", RatingValue: "9", RatingMax: "10", ViewDate: "2023-04-01", Review: "Still great."},
			llmEntry{Line: 3, Title: "Severance", Type: "tv", Season: "02", Episode: "3", RatingValue: "4", RatingMax: "5"},
			llmEntry{Line: 4, Title: "Up", Season: "1", RatingValue: "7", RatingMax: "5"}, // season on a movie, rating off the scale
			llmEntry{Line: 5, Title: "Jaws", Year: "1066"},                                // fails the import's checks
		)
	}}
	withFakeAPI(t, f)

	var out, errs bytes.Buffer
	in := "I saw Pulp Fiction (94) on 4/1/23, a 9/10. Still great.\nHeat: B+\nSeverance s2e3 4/5\nUp\nJaws\n"
	require.NoError(t, run(context.Background(), []string{"-ai", "-scale", "10", "-day-first"}, strings.NewReader(in), &out, &errs))
	assert.Equal(t, `title,year,type,rating,view_date,season,episode,review
Pulp Fiction,1994,,4.5,2023-04-01,,,Still great.
Heat,1995,,,,,,
Severance,,tv,4,,2,3,
Up,,,,,,,
`, out.String())
	assert.Contains(t, errs.String(), `line 4: dropped rating 7/5 for "Up"`)
	assert.Contains(t, errs.String(), `line 5: skipped "Jaws"`)

	require.Len(t, f.reqs, 1)
	req := f.reqs[0]
	assert.Equal(t, "claude-haiku-5-5", req["model"])
	oc := req["output_config"].(map[string]any)
	assert.Equal(t, "json_schema", oc["format"].(map[string]any)["type"])
	system := req["system"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, system, `rating_max "10"`)
	assert.Contains(t, system, "day/month/year")
	user := req["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.True(t, strings.HasPrefix(user, "L1: I saw Pulp Fiction"), user)
}

func TestAIChunks(t *testing.T) {
	// 300 one-line entries, in chunks; one chunk is refused and
	// converted without the model.
	var lines []string
	for i := range 300 {
		lines = append(lines, "Movie "+itoa(i)+" (2001) 4/5")
	}
	f := &fakeAPI{reply: func(user string) (int, string, string) {
		if strings.Contains(user, "Movie 150 ") {
			return 200, "refusal", ""
		}
		var es []llmEntry
		for _, l := range strings.Split(strings.TrimSpace(user), "\n") {
			num, text, _ := strings.Cut(strings.TrimPrefix(l, "L"), ": ")
			title, _, _ := strings.Cut(text, " (")
			es = append(es, llmEntry{Line: atoi(num), Title: "AI " + title, RatingValue: "4", RatingMax: "5"})
		}
		return 200, "end_turn", answer(es...)
	}}
	withFakeAPI(t, f)

	var out, errs bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"-ai"}, strings.NewReader(strings.Join(lines, "\n")), &out, &errs))
	rows := strings.Split(strings.TrimSpace(out.String()), "\n")[1:]
	require.Len(t, rows, 300)
	assert.Equal(t, "AI Movie 0,,4", rows[0])
	assert.Equal(t, "Movie 150,2001,4", rows[150]) // the refused chunk, without the model
	assert.Equal(t, "AI Movie 299,,4", rows[299])
	assert.Contains(t, errs.String(), "the model declined to read this part; converting them without the model")
	assert.Greater(t, len(f.reqs), 2)
}

func TestAIBadKey(t *testing.T) {
	withFakeAPI(t, &fakeAPI{reply: func(string) (int, string, string) { return 401, "", "" }})
	var out, errs bytes.Buffer
	err := run(context.Background(), []string{"-ai"}, strings.NewReader("Heat (1995) 4/5\n"), &out, &errs)
	assert.ErrorContains(t, err, "401")
	assert.Empty(t, out.String())
}

func TestChunks(t *testing.T) {
	var lines []string
	for i := range 250 {
		if i%10 == 9 {
			lines = append(lines, "")
		} else {
			lines = append(lines, "x")
		}
	}
	cs := chunks(strings.Join(lines, "\n"))
	n := 0
	for _, c := range cs {
		assert.Equal(t, n+1, c.first)
		assert.LessOrEqual(t, len(c.lines), chunkLines)
		if n+len(c.lines) < len(lines) {
			assert.Equal(t, "", c.lines[len(c.lines)-1], "chunk should end at a blank line")
		}
		n += len(c.lines)
	}
	assert.Equal(t, len(lines), n)
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func atoi(s string) int {
	var n int
	json.Unmarshal([]byte(s), &n)
	return n
}
