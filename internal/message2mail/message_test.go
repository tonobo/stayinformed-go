package message2mail

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"

	"github.com/tonobo/stayinformed-go/stayinformed"
)

func TestBuildMessageCreatesNestedMIME(t *testing.T) {
	message, err := BuildMessage(stayinformed.News{
		ID: "news-1", Title: "Example update", Content: "<p>Hello</p>", Date: "2026-09-11T10:00:00Z", HTMLContent: true,
		Poster: "Example Author", Type: "announcement", ReceiverType: "group", Important: true,
		Groups:      []stayinformed.Group{{ID: "group-b", Name: "Class B"}, {ID: "group-a", Name: "Class A"}},
		Attachments: []stayinformed.Attachment{{ID: "attachment-1", Name: "example.txt"}},
	}, []MailAttachment{{Name: "example.txt", ContentType: "text/plain", Data: []byte("attachment")}}, MessageOptions{
		From: "sender@example.invalid", To: []string{"recipient@example.invalid"}, SubjectPrefix: "[Example] ", Timezone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(message.Data))
	if err != nil {
		t.Fatal(err)
	}
	mediaType, parameters, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("unexpected outer media type %q: %v", mediaType, err)
	}
	mixed := multipart.NewReader(parsed.Body, parameters["boundary"])
	alternativePart, err := mixed.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	alternativeType, alternativeParameters, err := mime.ParseMediaType(alternativePart.Header.Get("Content-Type"))
	if err != nil || alternativeType != "multipart/alternative" {
		t.Fatalf("unexpected alternative type %q: %v", alternativeType, err)
	}
	alternative := multipart.NewReader(alternativePart, alternativeParameters["boundary"])
	plain, err := alternative.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	plainBody, err := io.ReadAll(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plainBody), "Hello") {
		t.Fatalf("plain body is missing content: %q", plainBody)
	}
	if !strings.Contains(string(plainBody), "Published by: Example Author") || !strings.Contains(string(plainBody), "Groups: Class A, Class B") {
		t.Fatalf("plain body is missing metadata: %q", plainBody)
	}
	htmlPart, err := alternative.NextPart()
	if err != nil {
		t.Fatalf("HTML alternative is missing: %v", err)
	}
	htmlBody, err := io.ReadAll(htmlPart)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlBody), "<strong>Groups</strong>") || !strings.Contains(string(htmlBody), "Class A, Class B") {
		t.Fatalf("HTML body is missing metadata: %q", htmlBody)
	}
	attachment, err := mixed.NextPart()
	if err != nil || attachment.FileName() != "example.txt" {
		t.Fatalf("attachment is missing: %v", err)
	}
	if message.Revision == "" || !strings.Contains(parsed.Header.Get("Message-ID"), message.Revision[:20]) {
		t.Fatalf("stable revision headers are missing")
	}
	if parsed.Header.Get("Keywords") == "" || parsed.Header.Get("X-Stayinformed-Groups") == "" || parsed.Header.Get("Importance") != "high" {
		t.Fatalf("metadata headers are missing: %+v", parsed.Header)
	}
}
