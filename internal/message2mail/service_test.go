package message2mail

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tonobo/stayinformed-go/stayinformed"
)

type newsAPIStub struct {
	item stayinformed.News
}

func (api newsAPIStub) NewsPage(context.Context, stayinformed.NewsOptions) (stayinformed.NewsPage, error) {
	return stayinformed.NewsPage{Items: []stayinformed.News{api.item}, Count: 1}, nil
}
func (api newsAPIStub) NewsItem(context.Context, string) (stayinformed.News, error) {
	return api.item, nil
}
func (newsAPIStub) Attachment(context.Context, string, string) (stayinformed.AttachmentData, error) {
	return stayinformed.AttachmentData{}, nil
}

type deliveryIndexStub struct {
	revisions map[string]bool
}

func (index *deliveryIndexStub) Contains(_ context.Context, revision string) (bool, error) {
	return index.revisions[revision], nil
}

type mailerStub struct {
	index *deliveryIndexStub
	sent  int
}

func (mailer *mailerStub) Send(_ context.Context, message OutgoingMessage) error {
	mailer.sent++
	mailer.index.revisions[message.Revision] = true
	return nil
}

func TestServiceDoesNotDeliverRevisionTwice(t *testing.T) {
	item := stayinformed.News{ID: "news-1", Title: "Example", Content: "Body", Date: "2026-09-11T10:00:00Z"}
	index := &deliveryIndexStub{revisions: make(map[string]bool)}
	mailer := &mailerStub{index: index}
	service := Service{
		OpenSession: func(context.Context, string) (NewsAPI, error) { return newsAPIStub{item: item}, nil },
		State:       FileStateStore{Path: filepath.Join(t.TempDir(), "state.json")}, Mailer: mailer, DeliveryIndex: index,
		MessageOptions:  MessageOptions{From: "sender@example.invalid", To: []string{"recipient@example.invalid"}, Timezone: "UTC"},
		ForwardExisting: true,
	}
	first, err := service.Sync(context.Background(), "access-token")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Sync(context.Background(), "access-token")
	if err != nil {
		t.Fatal(err)
	}
	if mailer.sent != 1 || first.Sent != 1 || first.Confirmed != 1 || second.Skipped != 1 {
		t.Fatalf("unexpected results: sent=%d first=%+v second=%+v", mailer.sent, first, second)
	}
}
