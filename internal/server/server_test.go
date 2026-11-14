package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func do(t *testing.T, deps map[string]Pinger, path string) *httptest.ResponseRecorder {
	t.Helper()
	r, err := NewBase(nil, nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)), deps)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestHealthz(t *testing.T) {
	if w := do(t, nil, "/healthz"); w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
}

func TestReadyzUpAndDown(t *testing.T) {
	up := map[string]Pinger{"postgres": fakePinger{}}
	if w := do(t, up, "/readyz"); w.Code != http.StatusOK {
		t.Fatalf("up: code = %d", w.Code)
	}
	down := map[string]Pinger{"postgres": fakePinger{err: errors.New("mati")}}
	if w := do(t, down, "/readyz"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("down: code = %d", w.Code)
	}
}

func TestMetrics(t *testing.T) {
	if w := do(t, nil, "/metrics"); w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
}
