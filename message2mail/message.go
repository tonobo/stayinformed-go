package message2mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tonobo/stayinformed-go/stayinformed"
)

var (
	messageBreakPattern = regexp.MustCompile(`(?i)<br\s*/?>`)
	messageBlockPattern = regexp.MustCompile(`(?i)</(?:p|div|li|h[1-6])\s*>`)
	messageTagPattern   = regexp.MustCompile(`<[^>]+>`)
)

type MailAttachment struct {
	Name        string
	ContentType string
	Data        []byte
}

type OutgoingMessage struct {
	EnvelopeFrom string
	Recipients   []string
	Data         []byte
	Revision     string
}

type MessageOptions struct {
	From          string
	To            []string
	SubjectPrefix string
	Timezone      string
}

func ListFingerprint(news stayinformed.News) string {
	return digestStrings(
		news.Identifier(), news.Title, news.Content, news.Date, news.DeadlineDate,
		news.Poster, news.Type, news.ReceiverType, fmt.Sprint(news.HasAttachments),
		fmt.Sprint(news.Important),
	)
}

func Revision(news stayinformed.News) string {
	parts := []string{
		news.Identifier(), news.Title, news.Content, news.Date, news.DeadlineDate,
		news.Poster, news.Type, news.ReceiverType, fmt.Sprint(news.Important),
		fmt.Sprint(news.HTMLContent),
	}
	attachments := append([]stayinformed.Attachment(nil), news.Attachments...)
	sort.Slice(attachments, func(i, j int) bool { return attachments[i].ID < attachments[j].ID })
	for _, attachment := range attachments {
		parts = append(parts, attachment.ID, attachment.Name, attachment.Type, attachment.Modified)
	}
	return digestStrings(parts...)
}

func BuildMessage(news stayinformed.News, attachments []MailAttachment, options MessageOptions) (OutgoingMessage, error) {
	from, err := mail.ParseAddress(options.From)
	if err != nil {
		return OutgoingMessage{}, fmt.Errorf("parse sender: %w", err)
	}
	if len(options.To) == 0 {
		return OutgoingMessage{}, fmt.Errorf("at least one recipient is required")
	}
	recipients := make([]string, 0, len(options.To))
	formattedRecipients := make([]string, 0, len(options.To))
	for _, value := range options.To {
		address, parseErr := mail.ParseAddress(value)
		if parseErr != nil {
			return OutgoingMessage{}, fmt.Errorf("parse recipient: %w", parseErr)
		}
		recipients = append(recipients, address.Address)
		formattedRecipients = append(formattedRecipients, address.String())
	}
	revision := Revision(news)
	domain := "stayinformed-go.local"
	if at := strings.LastIndexByte(from.Address, '@'); at >= 0 && at+1 < len(from.Address) {
		domain = from.Address[at+1:]
	}
	messageID := fmt.Sprintf("<stayinformed.%s.%s@%s>", safeToken(news.Identifier()), revision[:20], domain)
	date := parseMessageDate(news.Date, options.Timezone)
	subject := strings.TrimSpace(options.SubjectPrefix + news.Title)

	var output bytes.Buffer
	writeHeader(&output, "Date", date.Format(time.RFC1123Z))
	writeHeader(&output, "From", from.String())
	writeHeader(&output, "To", strings.Join(formattedRecipients, ", "))
	writeHeader(&output, "Message-ID", messageID)
	writeHeader(&output, "Subject", mime.QEncoding.Encode("utf-8", subject))
	writeHeader(&output, "MIME-Version", "1.0")
	writeHeader(&output, "Auto-Submitted", "auto-generated")
	writeHeader(&output, "X-Auto-Response-Suppress", "All")
	writeHeader(&output, "X-StayInformed-News-ID", safeHeader(news.Identifier()))
	writeHeader(&output, "X-StayInformed-Revision", revision)

	alternativeType, alternativeBody, err := buildAlternative(news)
	if err != nil {
		return OutgoingMessage{}, fmt.Errorf("build alternative body: %w", err)
	}
	if len(attachments) == 0 {
		writeHeader(&output, "Content-Type", alternativeType)
		output.WriteString("\r\n")
		_, _ = output.Write(alternativeBody)
	} else {
		mixed := multipart.NewWriter(&output)
		writeHeader(&output, "Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mixed.Boundary()}))
		output.WriteString("\r\n")
		partHeader := textproto.MIMEHeader{}
		partHeader.Set("Content-Type", alternativeType)
		part, createErr := mixed.CreatePart(partHeader)
		if createErr != nil {
			return OutgoingMessage{}, createErr
		}
		if _, err := part.Write(alternativeBody); err != nil {
			return OutgoingMessage{}, err
		}
		for _, attachment := range attachments {
			if err := writeAttachment(mixed, attachment); err != nil {
				return OutgoingMessage{}, err
			}
		}
		if err := mixed.Close(); err != nil {
			return OutgoingMessage{}, err
		}
	}
	return OutgoingMessage{EnvelopeFrom: from.Address, Recipients: recipients, Data: output.Bytes(), Revision: revision}, nil
}

func buildAlternative(news stayinformed.News) (string, []byte, error) {
	var output bytes.Buffer
	alternative := multipart.NewWriter(&output)
	contentType := mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": alternative.Boundary()})
	plainHeader := textproto.MIMEHeader{}
	plainHeader.Set("Content-Type", "text/plain; charset=utf-8")
	plainHeader.Set("Content-Transfer-Encoding", "quoted-printable")
	plainPart, err := alternative.CreatePart(plainHeader)
	if err != nil {
		return "", nil, err
	}
	plainWriter := quotedprintable.NewWriter(plainPart)
	if _, err := plainWriter.Write([]byte(messagePlainText(news.Content))); err != nil {
		return "", nil, err
	}
	if err := plainWriter.Close(); err != nil {
		return "", nil, err
	}

	htmlHeader := textproto.MIMEHeader{}
	htmlHeader.Set("Content-Type", "text/html; charset=utf-8")
	htmlHeader.Set("Content-Transfer-Encoding", "quoted-printable")
	htmlPart, err := alternative.CreatePart(htmlHeader)
	if err != nil {
		return "", nil, err
	}
	htmlWriter := quotedprintable.NewWriter(htmlPart)
	body := news.Content
	if !news.HTMLContent {
		body = "<pre style=\"white-space:pre-wrap\">" + html.EscapeString(news.Content) + "</pre>"
	}
	if _, err := htmlWriter.Write([]byte("<!doctype html><html><body>" + body + "</body></html>")); err != nil {
		return "", nil, err
	}
	if err := htmlWriter.Close(); err != nil {
		return "", nil, err
	}
	if err := alternative.Close(); err != nil {
		return "", nil, err
	}
	return contentType, output.Bytes(), nil
}

func writeAttachment(writer *multipart.Writer, attachment MailAttachment) error {
	contentType := attachment.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	name := safeFilename(attachment.Name)
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", mime.FormatMediaType(contentType, map[string]string{"name": name}))
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	header.Set("Content-Transfer-Encoding", "base64")
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(attachment.Data)
	for len(encoded) > 76 {
		if _, err := io.WriteString(part, encoded[:76]+"\r\n"); err != nil {
			return err
		}
		encoded = encoded[76:]
	}
	_, err = io.WriteString(part, encoded+"\r\n")
	return err
}

func writeHeader(output *bytes.Buffer, name, value string) {
	fmt.Fprintf(output, "%s: %s\r\n", name, safeHeader(value))
}

func safeHeader(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
}

func safeFilename(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	if slash := strings.LastIndexByte(value, '/'); slash >= 0 {
		value = value[slash+1:]
	}
	value = safeHeader(value)
	if value == "" || value == "." || value == ".." {
		return "attachment.bin"
	}
	return value
}

func safeToken(value string) string {
	var output strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '-' || character == '_' {
			output.WriteRune(character)
		}
	}
	if output.Len() == 0 {
		return "message"
	}
	return output.String()
}

func parseMessageDate(value, timezone string) time.Time {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.UTC
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, parseErr := time.Parse(layout, value); parseErr == nil {
			return parsed
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if parsed, parseErr := time.ParseInLocation(layout, value, location); parseErr == nil {
			return parsed
		}
	}
	return time.Now()
}

func messagePlainText(value string) string {
	value = messageBreakPattern.ReplaceAllString(value, "\n")
	value = messageBlockPattern.ReplaceAllString(value, "\n")
	value = messageTagPattern.ReplaceAllString(value, "")
	return strings.TrimSpace(html.UnescapeString(value))
}

func digestStrings(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = io.WriteString(hash, value)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
