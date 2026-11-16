package httplog

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, h http.Handler, method, target, body string) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("User-Agent", "test-agent")
	Middleware(logger, h).ServeHTTP(httptest.NewRecorder(), req)

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("decoding log line %q: %v", buf.String(), err)
	}
	return rec
}

func TestLogsRequest(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, "response-secret")
	})
	rec := serve(t, h, "POST", "/upload?x=1", "request-secret")

	want := map[string]any{
		"msg":        "request",
		"method":     "POST",
		"path":       "/upload?x=1",
		"status":     float64(409),
		"bytes":      float64(len("response-secret")),
		"user_agent": "test-agent",
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s = %v, want %v", k, rec[k], v)
		}
	}
	for _, k := range []string{"duration", "remote"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}

	line, _ := json.Marshal(rec)
	for _, secret := range []string{"request-secret", "response-secret"} {
		if strings.Contains(string(line), secret) {
			t.Errorf("log line contains body text %q: %s", secret, line)
		}
	}
}

func TestImplicitStatus(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"write only": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hi") },
		"nothing":    func(w http.ResponseWriter, r *http.Request) {},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := serve(t, h, "GET", "/", ""); rec["status"] != float64(200) {
				t.Errorf("status = %v, want 200", rec["status"])
			}
		})
	}
}
