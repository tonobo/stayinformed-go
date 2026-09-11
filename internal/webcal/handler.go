// Package webcal exposes a stateless HTTP handler that turns a caller-supplied
// Stay Informed access token into a live iCalendar feed.
package webcal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tonobo/stayinformed-go/ical"
	"github.com/tonobo/stayinformed-go/stayinformed"
)

type CalendarClient interface {
	Calendar(context.Context, string) (stayinformed.Calendar, error)
}

// TokenSource returns the Stay Informed token to use for one live request.
// Authentication of the incoming HTTP request is intentionally independent.
type TokenSource func(context.Context, *http.Request) (string, error)

type Handler struct {
	client  CalendarClient
	token   TokenSource
	options ical.Options
	logger  *slog.Logger
	mux     *http.ServeMux
}

func NewHandler(client CalendarClient, token TokenSource, options ical.Options, logger *slog.Logger) (*Handler, error) {
	if client == nil {
		return nil, errors.New("calendar client is required")
	}
	if token == nil {
		return nil, errors.New("token source is required")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	handler := &Handler{client: client, token: token, options: options, logger: logger, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /calendar.ics", handler.calendar)
	handler.mux.HandleFunc("GET /healthz", handler.health)
	handler.mux.HandleFunc("GET /readyz", handler.ready)
	return handler, nil
}

func (h *Handler) ready(response http.ResponseWriter, request *http.Request) {
	token, err := h.token(request.Context(), request)
	if err != nil || strings.TrimSpace(token) == "" {
		http.Error(response, "not ready", http.StatusServiceUnavailable)
		return
	}
	token = ""
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(map[string]string{"status": "ready"})
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Referrer-Policy", "no-referrer")
	h.mux.ServeHTTP(response, request)
}

func (h *Handler) health(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) calendar(response http.ResponseWriter, request *http.Request) {
	token, err := h.token(request.Context(), request)
	if err != nil || strings.TrimSpace(token) == "" {
		h.logger.Error("access token lookup failed", "error", err)
		http.Error(response, "the upstream access token is unavailable", http.StatusServiceUnavailable)
		return
	}
	calendar, err := h.client.Calendar(request.Context(), strings.TrimSpace(token))
	token = ""
	if err != nil {
		h.logger.Error("live calendar fetch failed", "error", err)
		http.Error(response, "failed to fetch the live calendar", http.StatusBadGateway)
		return
	}
	data, err := ical.Render(calendar, h.options)
	if err != nil {
		h.logger.Error("calendar rendering failed", "error", err)
		http.Error(response, "failed to render the calendar", http.StatusBadGateway)
		return
	}
	digest := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	response.Header().Set("Cache-Control", "private, no-cache")
	response.Header().Set("Content-Disposition", `inline; filename="stayinformed.ics"`)
	response.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	response.Header().Set("ETag", etag)
	if request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = response.Write(data)
}

// RequestBearerToken is intended for local or private deployments where the
// caller directly supplies the Stay Informed access token. It must not be used
// behind an authentication proxy which owns the Authorization header.
func RequestBearerToken(_ context.Context, request *http.Request) (string, error) {
	value := request.Header.Get("Authorization")
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", errors.New("a Stay Informed bearer token is required")
	}
	return parts[1], nil
}
