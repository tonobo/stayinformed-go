package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tonobo/stayinformed-go/ical"
	"github.com/tonobo/stayinformed-go/stayinformed"
	"github.com/tonobo/stayinformed-go/webcal"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "webcal:", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", envOr("STAYINFORMED_WEBCAL_LISTEN", "127.0.0.1:8787"), "HTTP listen address")
	tokenFile := flag.String("token-file", os.Getenv("STAYINFORMED_TOKEN_FILE"), "file containing a rotating Stay Informed access token")
	allowCallerToken := flag.Bool("allow-caller-token", false, "accept a Stay Informed bearer token from the caller")
	apiVersion := flag.String("api-version", os.Getenv("STAYINFORMED_API_VERSION"), "override the auto-discovered app API version")
	language := flag.String("language", envOr("STAYINFORMED_LANGUAGE", "de"), "upstream response language")
	timezone := flag.String("timezone", envOr("STAYINFORMED_TIMEZONE", "Europe/Berlin"), "fallback IANA timezone")
	calendarName := flag.String("calendar-name", os.Getenv("STAYINFORMED_CALENDAR_NAME"), "override the calendar name")
	flag.Parse()

	config := stayinformed.DefaultConfig()
	config.APIVersion = *apiVersion
	config.Language = *language
	config.Timezone = *timezone
	config.CalendarName = *calendarName
	client, err := stayinformed.NewClient(config)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	var tokenSource webcal.TokenSource
	switch {
	case *tokenFile != "" && !*allowCallerToken:
		tokenSource = webcal.FileTokenSource(*tokenFile)
	case *tokenFile == "" && *allowCallerToken:
		tokenSource = webcal.RequestBearerToken
	default:
		return errors.New("configure exactly one of --token-file or --allow-caller-token")
	}
	handler, err := webcal.NewHandler(client, tokenSource, ical.Options{CalendarName: *calendarName, Timezone: *timezone}, logger)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	logger.Info("webcal listening", "address", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
