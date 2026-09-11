package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tonobo/stayinformed-go/auth"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stayinformed-token:", err)
		os.Exit(1)
	}
}

func run() error {
	mode := "login"
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "login" || args[0] == "refresh" || args[0] == "serve") {
		mode = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("stayinformed-token "+mode, flag.ContinueOnError)
	format := flags.String("format", "access-token", "output format: access-token or json")
	username := flags.String("username", os.Getenv("STAYINFORMED_USERNAME"), "Stay Informed username")
	usernameCommand := flags.String("username-command", os.Getenv("STAYINFORMED_USERNAME_COMMAND"), "command whose first output line is the username")
	passwordCommand := flags.String("password-command", os.Getenv("STAYINFORMED_PASSWORD_COMMAND"), "command whose first output line is the password")
	refreshTokenCommand := flags.String("refresh-token-command", os.Getenv("STAYINFORMED_REFRESH_TOKEN_COMMAND"), "command whose first output line is the refresh token")
	organization := flags.String("organization", os.Getenv("STAYINFORMED_ORGANIZATION"), "optional Keycloak organization alias")
	locale := flags.String("locale", envOr("STAYINFORMED_LANGUAGE", "de"), "login page locale")
	outputFile := flags.String("output-file", os.Getenv("STAYINFORMED_TOKEN_FILE"), "atomically updated access-token file used in serve mode")
	refreshBefore := flags.Duration("refresh-before", time.Minute, "refresh the token this long before expiry in serve mode")
	if err := flags.Parse(args); err != nil {
		return err
	}

	client, err := auth.NewClient(auth.DefaultConfig())
	if err != nil {
		return err
	}
	if mode == "serve" {
		if *outputFile == "" {
			return errors.New("--output-file is required in serve mode")
		}
		if *refreshBefore < 0 {
			return errors.New("--refresh-before must not be negative")
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return serve(ctx, client, loginInput{
			username: *username, usernameCommand: *usernameCommand, passwordCommand: *passwordCommand,
			organization: *organization, locale: *locale,
		}, *outputFile, *refreshBefore)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var tokens auth.Tokens
	switch mode {
	case "login":
		resolvedUsername := strings.TrimSpace(*username)
		if resolvedUsername == "" && *usernameCommand != "" {
			resolvedUsername, err = commandValue(ctx, *usernameCommand, "username")
			if err != nil {
				return err
			}
		}
		if resolvedUsername == "" {
			return errors.New("--username or --username-command is required")
		}
		if *passwordCommand == "" {
			return errors.New("--password-command is required")
		}
		password, err := commandValue(ctx, *passwordCommand, "password")
		if err != nil {
			return err
		}
		tokens, err = client.Login(ctx, resolvedUsername, password, auth.LoginOptions{Organization: *organization, Locale: *locale})
		password = ""
		if err != nil {
			return err
		}
	case "refresh":
		if *refreshTokenCommand == "" {
			return errors.New("--refresh-token-command is required in refresh mode")
		}
		refreshToken, err := commandValue(ctx, *refreshTokenCommand, "refresh token")
		if err != nil {
			return err
		}
		tokens, err = client.Refresh(ctx, refreshToken)
		refreshToken = ""
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}

	switch *format {
	case "access-token":
		fmt.Fprintln(os.Stdout, tokens.AccessToken)
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(tokens)
	default:
		return fmt.Errorf("unknown output format %q", *format)
	}
	return nil
}

type loginInput struct {
	username        string
	usernameCommand string
	passwordCommand string
	organization    string
	locale          string
}

func serve(ctx context.Context, client *auth.Client, input loginInput, outputFile string, refreshBefore time.Duration) error {
	if err := clearTokenFile(outputFile); err != nil {
		return err
	}
	defer func() { _ = clearTokenFile(outputFile) }()
	loginContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	tokens, err := login(loginContext, client, input)
	cancel()
	if err != nil {
		return err
	}
	if tokens.RefreshToken == "" {
		return errors.New("login did not return a refresh token")
	}
	for {
		if err := writeTokenFile(outputFile, tokens.AccessToken); err != nil {
			return err
		}
		wait := time.Until(tokens.ExpiresAt) - refreshBefore
		if wait <= 0 {
			wait = time.Until(tokens.ExpiresAt) / 2
		}
		if wait <= 0 {
			return errors.New("received an already expired access token")
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		refreshContext, refreshCancel := context.WithTimeout(ctx, 45*time.Second)
		refreshed, refreshErr := client.Refresh(refreshContext, tokens.RefreshToken)
		refreshCancel()
		if refreshErr != nil {
			return fmt.Errorf("refresh access token: %w", refreshErr)
		}
		if refreshed.RefreshToken == "" {
			refreshed.RefreshToken = tokens.RefreshToken
		}
		tokens = refreshed
	}
}

func clearTokenFile(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale token file: %w", err)
	}
	return nil
}

func login(ctx context.Context, client *auth.Client, input loginInput) (auth.Tokens, error) {
	username := strings.TrimSpace(input.username)
	var err error
	if username == "" && input.usernameCommand != "" {
		username, err = commandValue(ctx, input.usernameCommand, "username")
		if err != nil {
			return auth.Tokens{}, err
		}
	}
	if username == "" {
		return auth.Tokens{}, errors.New("--username or --username-command is required")
	}
	if input.passwordCommand == "" {
		return auth.Tokens{}, errors.New("--password-command is required")
	}
	password, err := commandValue(ctx, input.passwordCommand, "password")
	if err != nil {
		return auth.Tokens{}, err
	}
	tokens, err := client.Login(ctx, username, password, auth.LoginOptions{Organization: input.organization, Locale: input.locale})
	password = ""
	return tokens, err
}

func writeTokenFile(path, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("refusing to write an empty access token")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".access-token-*")
	if err != nil {
		return fmt.Errorf("create temporary token file: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set token file permissions: %w", err)
	}
	if _, err := fmt.Fprintln(temporary, token); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync token file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close token file: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace token file: %w", err)
	}
	removeTemporary = false
	return nil
}

func commandValue(ctx context.Context, command, label string) (string, error) {
	process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	output, err := process.Output()
	if err != nil {
		return "", fmt.Errorf("%s command failed", label)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	if !scanner.Scan() || scanner.Text() == "" {
		return "", fmt.Errorf("%s command returned an empty value", label)
	}
	return scanner.Text(), nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
