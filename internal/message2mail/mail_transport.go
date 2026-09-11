package message2mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

type TLSMode string

const (
	TLSImplicit TLSMode = "tls"
	TLSStartTLS TLSMode = "starttls"
)

type Mailer interface {
	Send(context.Context, OutgoingMessage) error
}

type SMTPConfig struct {
	Address  string
	TLSMode  TLSMode
	Username string
	Password string
}

type SMTPMailer struct {
	config SMTPConfig
}

func NewSMTPMailer(config SMTPConfig) (*SMTPMailer, error) {
	if config.Address == "" {
		return nil, errors.New("SMTP address is required")
	}
	if config.TLSMode == "" {
		config.TLSMode = TLSStartTLS
	}
	if config.TLSMode != TLSImplicit && config.TLSMode != TLSStartTLS {
		return nil, fmt.Errorf("unsupported SMTP TLS mode %q", config.TLSMode)
	}
	if (config.Username == "") != (config.Password == "") {
		return nil, errors.New("SMTP username and password must be configured together")
	}
	return &SMTPMailer{config: config}, nil
}

func (m *SMTPMailer) Send(ctx context.Context, message OutgoingMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	host, _, err := net.SplitHostPort(m.config.Address)
	if err != nil {
		return fmt.Errorf("parse SMTP address: %w", err)
	}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	var client *smtp.Client
	switch m.config.TLSMode {
	case TLSImplicit:
		client, err = smtp.DialTLS(m.config.Address, tlsConfig)
	case TLSStartTLS:
		client, err = smtp.DialStartTLS(m.config.Address, tlsConfig)
	}
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	defer client.Close()
	client.CommandTimeout = 30 * time.Second
	client.SubmissionTimeout = 60 * time.Second
	if m.config.Username != "" {
		if err := client.Auth(sasl.NewPlainClient("", m.config.Username, m.config.Password)); err != nil {
			return fmt.Errorf("authenticate to SMTP server: %w", err)
		}
	}
	if err := client.SendMail(message.EnvelopeFrom, message.Recipients, strings.NewReader(string(message.Data))); err != nil {
		return fmt.Errorf("submit message: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}

type DeliveryIndex interface {
	Contains(context.Context, string) (bool, error)
}

type IMAPConfig struct {
	Address  string
	TLSMode  TLSMode
	Username string
	Password string
	Mailbox  string
}

type IMAPMailbox struct {
	config IMAPConfig
}

func NewIMAPMailbox(config IMAPConfig) (*IMAPMailbox, error) {
	if config.Address == "" || config.Username == "" || config.Password == "" {
		return nil, errors.New("IMAP address, username, and password are required")
	}
	if config.TLSMode == "" {
		config.TLSMode = TLSImplicit
	}
	if config.TLSMode != TLSImplicit && config.TLSMode != TLSStartTLS {
		return nil, fmt.Errorf("unsupported IMAP TLS mode %q", config.TLSMode)
	}
	if config.Mailbox == "" {
		config.Mailbox = "INBOX"
	}
	return &IMAPMailbox{config: config}, nil
}

func (m *IMAPMailbox) Contains(ctx context.Context, revision string) (bool, error) {
	client, err := m.dial(nil)
	if err != nil {
		return false, err
	}
	defer client.Close()
	if err := client.Login(m.config.Username, m.config.Password).Wait(); err != nil {
		return false, fmt.Errorf("authenticate to IMAP server: %w", err)
	}
	if _, err := client.Select(m.config.Mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return false, fmt.Errorf("select IMAP mailbox: %w", err)
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	criteria := &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "X-StayInformed-Revision", Value: revision}}}
	data, err := client.Search(criteria, nil).Wait()
	if err != nil {
		return false, fmt.Errorf("search IMAP mailbox: %w", err)
	}
	return len(data.AllSeqNums()) > 0, nil
}

// Watch blocks in IMAP IDLE and coalesces mailbox changes into the trigger
// channel. It owns one connection and returns when that connection fails.
func (m *IMAPMailbox) Watch(ctx context.Context, trigger chan<- struct{}) error {
	updates := make(chan struct{}, 1)
	options := &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					select {
					case updates <- struct{}{}:
					default:
					}
				}
			},
		},
	}
	client, err := m.dial(options)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Login(m.config.Username, m.config.Password).Wait(); err != nil {
		return fmt.Errorf("authenticate to IMAP server: %w", err)
	}
	if _, err := client.Select(m.config.Mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("select IMAP mailbox: %w", err)
	}

	for {
		idle, err := client.Idle()
		if err != nil {
			return fmt.Errorf("start IMAP IDLE: %w", err)
		}
		done := make(chan error, 1)
		go func() { done <- idle.Wait() }()
		select {
		case <-ctx.Done():
			_ = idle.Close()
			<-done
			return nil
		case err := <-done:
			if err != nil {
				return fmt.Errorf("IMAP IDLE failed: %w", err)
			}
			return errors.New("IMAP IDLE ended unexpectedly")
		case <-updates:
			if err := idle.Close(); err != nil {
				return fmt.Errorf("stop IMAP IDLE: %w", err)
			}
			if err := <-done; err != nil {
				return fmt.Errorf("finish IMAP IDLE: %w", err)
			}
			select {
			case trigger <- struct{}{}:
			default:
			}
		}
	}
}

func (m *IMAPMailbox) dial(options *imapclient.Options) (*imapclient.Client, error) {
	host, _, err := net.SplitHostPort(m.config.Address)
	if err != nil {
		return nil, fmt.Errorf("parse IMAP address: %w", err)
	}
	if options == nil {
		options = &imapclient.Options{}
	}
	options.TLSConfig = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	switch m.config.TLSMode {
	case TLSImplicit:
		client, err := imapclient.DialTLS(m.config.Address, options)
		if err != nil {
			return nil, fmt.Errorf("connect to IMAP server: %w", err)
		}
		return client, nil
	case TLSStartTLS:
		client, err := imapclient.DialStartTLS(m.config.Address, options)
		if err != nil {
			return nil, fmt.Errorf("connect to IMAP server: %w", err)
		}
		return client, nil
	default:
		return nil, fmt.Errorf("unsupported IMAP TLS mode %q", m.config.TLSMode)
	}
}
