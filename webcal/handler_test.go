package webcal

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tonobo/stayinformed-go/ical"
	"github.com/tonobo/stayinformed-go/stayinformed"
)

type calendarClientStub struct {
	token string
	calls int
}

func (client *calendarClientStub) Calendar(_ context.Context, token string) (stayinformed.Calendar, error) {
	client.token = token
	client.calls++
	return stayinformed.Calendar{Name: "Example", Events: []stayinformed.Event{{ID: "event", Title: "Example", Start: "2026-09-15T13:00:00Z", End: "2026-09-15T14:00:00Z"}}}, nil
}

func TestHandlerUsesIndependentTokenSource(t *testing.T) {
	client := &calendarClientStub{}
	handler, err := NewHandler(client, func(context.Context, *http.Request) (string, error) {
		return "upstream-token", nil
	}, ical.Options{Timezone: "UTC"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/calendar.ics", nil)
	request.Header.Set("Authorization", "Basic inbound-authentication")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || client.token != "upstream-token" {
		t.Fatalf("unexpected response %d and token %q", response.Code, client.token)
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag is missing")
	}
	request = httptest.NewRequest(http.MethodGet, "/calendar.ics", nil)
	request.Header.Set("If-None-Match", etag)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotModified || client.calls != 2 {
		t.Fatalf("expected a live fetch followed by 304, got %d and %d calls", response.Code, client.calls)
	}
}

func TestHandlerReturnsUnavailableWithoutToken(t *testing.T) {
	handler, err := NewHandler(&calendarClientStub{}, func(context.Context, *http.Request) (string, error) {
		return "", errors.New("not ready")
	}, ical.Options{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/calendar.ics", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status %d", response.Code)
	}
}
