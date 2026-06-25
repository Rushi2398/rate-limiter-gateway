package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// captureLogs redirects slog's default logger to a buffer for the duration of one test, using JSON output so we can parse and assert on individual fields rather than just eyeballing log text. Restores the previous default logger via t.Cleanup so tests don't leak state into each other.

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})
	return &buf
}

// parseLastLogLine parses the buffer's content as a single JSON log line. slog's JSON handler writes one JSON object per line; tests in this file only ever trigger one logged request, so we parse the whole trimmed buffer as one object rather than splitting lines.
func parseLastLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse log output as JSON: %v\nraw output: %s", err, buf.String())
	}
	return entry
}

func TestLogger_EmitsRequestFields(t *testing.T) {
	buf := captureLogs(t)
	handler := Logger(passThroughHandler())

	req := httptest.NewRequest(http.MethodPost, "/v1/widgets", nil)
	req.Header.Set("X-API-Key", "client-under-test")
	req.RemoteAddr = "203.0.113.9:5555"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	entry := parseLastLogLine(t, buf)

	if got := entry["method"]; got != "POST" {
		t.Errorf(`method = %v, want "POST"`, got)
	}
	if got := entry["path"]; got != "/v1/widgets" {
		t.Errorf(`path = %v, want "/v1/widgets"`, got)
	}
	if got := entry["client"]; got != "client-under-test" {
		t.Errorf(`client = %v, want "client-under-test"`, got)
	}
	if got := entry["remote_addr"]; got != "203.0.113.9:5555" {
		t.Errorf(`remote_addr = %v, want "203.0.113.9:5555"`, got)
	}
	if _, ok := entry["latency_ms"]; !ok {
		t.Error("expected a latency_ms field in the log entry, found none")
	}
	if _, ok := entry["request_id"]; !ok {
		t.Error("expected a request_id field in the log entry, found none")
	}
}

func TestLogger_RecordsActualStatusCode(t *testing.T) {
	buf := captureLogs(t)

	teapotHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // 418 — distinctive, won't collide with a default
	})
	handler := Logger(teapotHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	entry := parseLastLogLine(t, buf)

	// JSON numbers decode as float64 via encoding/json into map[string]any.
	status, ok := entry["status"].(float64)
	if !ok {
		t.Fatalf("status field = %v (%T), want a number", entry["status"], entry["status"])
	}
	if int(status) != http.StatusTeapot {
		t.Errorf("status = %d, want %d — Logger must record the handler's ACTUAL status, not assume 200", int(status), http.StatusTeapot)
	}
}

func TestLogger_DefaultsStatusTo200WhenHandlerNeverCallsWriteHeader(t *testing.T) {
	buf := captureLogs(t)

	// A handler that writes a body but never explicitly calls WriteHeader — Go's http package implicitly sends 200 in this case, and our statusRecorder must reflect that default, not a zero value.
	implicitOKHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok, implicitly"))
	})
	handler := Logger(implicitOKHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	entry := parseLastLogLine(t, buf)
	status, _ := entry["status"].(float64)
	if int(status) != http.StatusOK {
		t.Errorf("status = %d, want %d (implicit default)", int(status), http.StatusOK)
	}
}

func TestLogger_SetsRequestIDResponseHeader(t *testing.T) {
	_ = captureLogs(t) // suppress log noise; not asserting on it here
	handler := Logger(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get(requestIDHeader); got == "" {
		t.Error("expected X-Request-ID response header to be set, got empty")
	}
}

func TestLogger_RequestIDsAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	const n = 1000
	for i := 0; i < n; i++ {
		id := newRequestID()
		if seen[id] {
			t.Fatalf("duplicate request ID generated after %d iterations: %s", i, id)
		}
		seen[id] = true
	}
}

func TestLogger_MeasuresRealLatency(t *testing.T) {
	buf := captureLogs(t)

	slowHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	handler := Logger(slowHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	entry := parseLastLogLine(t, buf)
	latency, ok := entry["latency_ms"].(float64)
	if !ok {
		t.Fatalf("latency_ms = %v (%T), want a number", entry["latency_ms"], entry["latency_ms"])
	}
	if latency < 40 {
		t.Errorf("latency_ms = %.0f, want >= ~50 given the handler slept 50ms — looks like Logger isn't measuring real elapsed time", latency)
	}
}
