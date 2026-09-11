package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tonobo/stayinformed-go/internal/message2mail"
	"github.com/tonobo/stayinformed-go/stayinformed"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "message2mail:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultState, err := defaultStatePath()
	if err != nil {
		return err
	}
	var recipients stringList
	flags := flag.NewFlagSet("message2mail", flag.ContinueOnError)
	tokenCommand := flags.String("token-command", os.Getenv("STAYINFORMED_TOKEN_COMMAND"), "command whose first output line is a Stay Informed access token")
	mailUsername := flags.String("mail-username", os.Getenv("MESSAGE2MAIL_MAIL_USERNAME"), "IMAP and SMTP username")
	mailPasswordCommand := flags.String("mail-password-command", os.Getenv("MESSAGE2MAIL_MAIL_PASSWORD_COMMAND"), "command whose first output line is the IMAP and SMTP password")
	imapAddress := flags.String("imap-address", os.Getenv("MESSAGE2MAIL_IMAP_ADDRESS"), "IMAP host and port")
	imapTLS := flags.String("imap-tls", envOr("MESSAGE2MAIL_IMAP_TLS", "tls"), "IMAP TLS mode: tls or starttls")
	imapMailbox := flags.String("imap-mailbox", envOr("MESSAGE2MAIL_IMAP_MAILBOX", "INBOX"), "mailbox to watch and search")
	smtpAddress := flags.String("smtp-address", os.Getenv("MESSAGE2MAIL_SMTP_ADDRESS"), "SMTP host and port")
	smtpTLS := flags.String("smtp-tls", envOr("MESSAGE2MAIL_SMTP_TLS", "starttls"), "SMTP TLS mode: tls or starttls")
	from := flags.String("from", os.Getenv("MESSAGE2MAIL_FROM"), "RFC 5322 sender address")
	flags.Var(&recipients, "to", "RFC 5322 recipient address; repeat for multiple recipients")
	subjectPrefix := flags.String("subject-prefix", envOr("MESSAGE2MAIL_SUBJECT_PREFIX", "[Stay Informed] "), "forwarded message subject prefix")
	statePath := flags.String("state-file", envOr("MESSAGE2MAIL_STATE_FILE", defaultState), "durable delivery state path")
	forwardExisting := flags.Bool("forward-existing", false, "forward existing messages when creating state instead of seeding them")
	once := flags.Bool("once", false, "run one synchronization without IMAP IDLE")
	syncInterval := flags.Duration("sync-interval", 30*time.Minute, "fallback synchronization interval")
	debounce := flags.Duration("debounce", 5*time.Second, "delay used to coalesce IMAP changes")
	retryGrace := flags.Duration("retry-grace", 15*time.Minute, "minimum wait before retrying an unconfirmed delivery")
	maxAttempts := flags.Int("max-attempts", 3, "maximum SMTP attempts per message revision")
	maxPages := flags.Int("max-pages", 100, "maximum Stay Informed news pages per synchronization")
	apiVersion := flags.String("api-version", os.Getenv("STAYINFORMED_API_VERSION"), "override the auto-discovered app API version")
	language := flags.String("language", envOr("STAYINFORMED_LANGUAGE", "de"), "upstream response language")
	timezone := flags.String("timezone", envOr("STAYINFORMED_TIMEZONE", "Europe/Berlin"), "IANA timezone")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if len(recipients) == 0 {
		if value := os.Getenv("MESSAGE2MAIL_TO"); value != "" {
			for _, recipient := range strings.Split(value, ",") {
				if strings.TrimSpace(recipient) != "" {
					recipients = append(recipients, strings.TrimSpace(recipient))
				}
			}
		}
	}
	if *tokenCommand == "" || *mailUsername == "" || *mailPasswordCommand == "" || *imapAddress == "" || *smtpAddress == "" || *from == "" || len(recipients) == 0 {
		return errors.New("token command, mail credentials, IMAP, SMTP, sender, and at least one recipient are required")
	}
	mailPassword, err := commandValue(context.Background(), *mailPasswordCommand, "mail password")
	if err != nil {
		return err
	}
	defer func() { mailPassword = "" }()

	mailbox, err := message2mail.NewIMAPMailbox(message2mail.IMAPConfig{
		Address: *imapAddress, TLSMode: message2mail.TLSMode(*imapTLS), Username: *mailUsername, Password: mailPassword, Mailbox: *imapMailbox,
	})
	if err != nil {
		return err
	}
	mailer, err := message2mail.NewSMTPMailer(message2mail.SMTPConfig{
		Address: *smtpAddress, TLSMode: message2mail.TLSMode(*smtpTLS), Username: *mailUsername, Password: mailPassword,
	})
	if err != nil {
		return err
	}
	clientConfig := stayinformed.DefaultConfig()
	clientConfig.APIVersion = *apiVersion
	clientConfig.Language = *language
	clientConfig.Timezone = *timezone
	client, err := stayinformed.NewClient(clientConfig)
	if err != nil {
		return err
	}
	service := &message2mail.Service{
		OpenSession: func(ctx context.Context, token string) (message2mail.NewsAPI, error) {
			return client.OpenSession(ctx, token)
		},
		State:           message2mail.FileStateStore{Path: *statePath},
		Mailer:          mailer,
		DeliveryIndex:   mailbox,
		MessageOptions:  message2mail.MessageOptions{From: *from, To: recipients, SubjectPrefix: *subjectPrefix, Timezone: *timezone},
		ForwardExisting: *forwardExisting,
		MaxPages:        *maxPages,
		MaxAttempts:     *maxAttempts,
		RetryGrace:      *retryGrace,
	}
	lock, err := acquireLock(*statePath + ".lock")
	if err != nil {
		return err
	}
	defer releaseLock(lock)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	syncOnce := func(parent context.Context) error {
		ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
		defer cancel()
		token, err := commandValue(ctx, *tokenCommand, "token")
		if err != nil {
			return err
		}
		result, err := service.Sync(ctx, token)
		token = ""
		logger.Info("synchronization finished",
			"listed", result.Listed, "seeded", result.Seeded, "sent", result.Sent,
			"confirmed", result.Confirmed, "pending", result.Pending, "skipped", result.Skipped, "failed", result.Failed,
		)
		return err
	}
	if *once {
		return syncOnce(context.Background())
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	triggers := make(chan struct{}, 1)
	go watchWithReconnect(ctx, mailbox, triggers, logger)
	ticker := time.NewTicker(*syncInterval)
	defer ticker.Stop()
	triggers <- struct{}{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := syncOnce(ctx); err != nil {
				logger.Error("periodic synchronization failed", "error", err)
			}
		case <-triggers:
			if *debounce > 0 {
				timer := time.NewTimer(*debounce)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil
				case <-timer.C:
				}
			}
			for len(triggers) > 0 {
				<-triggers
			}
			if err := syncOnce(ctx); err != nil {
				logger.Error("IMAP-triggered synchronization failed", "error", err)
			}
		}
	}
}

func watchWithReconnect(ctx context.Context, mailbox *message2mail.IMAPMailbox, triggers chan<- struct{}, logger *slog.Logger) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := mailbox.Watch(ctx, triggers)
		if ctx.Err() != nil {
			return
		}
		logger.Error("IMAP IDLE disconnected", "error", err, "reconnect_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > time.Minute {
			backoff = time.Minute
		}
	}
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

func acquireLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("another message2mail process is using this state")
	}
	return file, nil
}

func releaseLock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func defaultStatePath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		directory = stateHome
	} else if home, err := os.UserHomeDir(); err == nil {
		directory = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(directory, "stayinformed-go", "message2mail.json"), nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}
