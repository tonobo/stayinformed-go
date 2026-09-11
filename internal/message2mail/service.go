// Package message2mail forwards Stay Informed news into a mailbox while
// keeping delivery state separate from the upstream read status.
package message2mail

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/tonobo/stayinformed-go/stayinformed"
)

type NewsAPI interface {
	NewsPage(context.Context, stayinformed.NewsOptions) (stayinformed.NewsPage, error)
	NewsItem(context.Context, string) (stayinformed.News, error)
	Attachment(context.Context, string, string) (stayinformed.AttachmentData, error)
}

type OpenSessionFunc func(context.Context, string) (NewsAPI, error)

type Service struct {
	OpenSession     OpenSessionFunc
	State           StateStore
	Mailer          Mailer
	DeliveryIndex   DeliveryIndex
	MessageOptions  MessageOptions
	ForwardExisting bool
	MaxPages        int
	PageSize        int
	MaxAttempts     int
	RetryGrace      time.Duration
	MaxMessageSize  int64
	Now             func() time.Time
}

type SyncResult struct {
	Listed    int
	Seeded    int
	Sent      int
	Confirmed int
	Skipped   int
	Failed    int
	Pending   int
}

func (s *Service) Sync(ctx context.Context, accessToken string) (SyncResult, error) {
	if err := s.setDefaults(); err != nil {
		return SyncResult{}, err
	}
	api, err := s.OpenSession(ctx, accessToken)
	if err != nil {
		return SyncResult{}, fmt.Errorf("open Stay Informed session: %w", err)
	}
	items, err := s.listAll(ctx, api)
	if err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{Listed: len(items)}
	state, err := s.State.Load()
	if err != nil {
		return result, err
	}
	if !state.Initialized {
		state.Initialized = true
		if !s.ForwardExisting {
			for _, item := range items {
				fingerprint := ListFingerprint(item)
				state.Deliveries[fingerprint] = Delivery{
					NewsID: item.Identifier(), ListFingerprint: fingerprint, Status: StatusSeeded,
				}
				result.Seeded++
			}
			if err := s.State.Save(state); err != nil {
				return result, err
			}
			return result, nil
		}
		if err := s.State.Save(state); err != nil {
			return result, err
		}
	}

	if err := s.reconcilePending(ctx, &state, &result); err != nil {
		return result, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Date == items[j].Date {
			return items[i].Identifier() < items[j].Identifier()
		}
		return items[i].Date < items[j].Date
	})
	for _, summary := range items {
		fingerprint := ListFingerprint(summary)
		delivery, exists := state.Deliveries[fingerprint]
		if exists {
			switch delivery.Status {
			case StatusDelivered, StatusSeeded, StatusFailed:
				result.Skipped++
				continue
			case StatusPending, StatusRetryWait:
				if s.Now().Sub(delivery.LastAttempt) < s.RetryGrace {
					result.Pending++
					continue
				}
				if delivery.Attempts >= s.MaxAttempts {
					delivery.Status = StatusFailed
					state.Deliveries[fingerprint] = delivery
					result.Failed++
					if err := s.State.Save(state); err != nil {
						return result, err
					}
					continue
				}
			}
		}
		if err := s.deliver(ctx, api, summary, fingerprint, delivery, &state, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Service) listAll(ctx context.Context, api NewsAPI) ([]stayinformed.News, error) {
	items := make([]stayinformed.News, 0)
	for offset := 0; offset < s.MaxPages; offset++ {
		page, err := api.NewsPage(ctx, stayinformed.NewsOptions{Offset: offset, PageSize: s.PageSize})
		if err != nil {
			return nil, fmt.Errorf("list Stay Informed news page %d: %w", offset, err)
		}
		items = append(items, page.Items...)
		if !page.HasMore(offset, s.PageSize) {
			return items, nil
		}
	}
	return nil, fmt.Errorf("news listing exceeded the configured limit of %d pages", s.MaxPages)
}

func (s *Service) reconcilePending(ctx context.Context, state *State, result *SyncResult) error {
	for key, delivery := range state.Deliveries {
		if delivery.Status != StatusPending && delivery.Status != StatusRetryWait || delivery.Revision == "" {
			continue
		}
		found, err := s.DeliveryIndex.Contains(ctx, delivery.Revision)
		if err != nil {
			return fmt.Errorf("reconcile pending delivery: %w", err)
		}
		if found {
			delivery.Status = StatusDelivered
			delivery.DeliveredAt = s.Now()
			state.Deliveries[key] = delivery
			result.Confirmed++
			if err := s.State.Save(*state); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) deliver(ctx context.Context, api NewsAPI, summary stayinformed.News, fingerprint string, previous Delivery, state *State, result *SyncResult) error {
	item, err := api.NewsItem(ctx, summary.Identifier())
	if err != nil {
		return fmt.Errorf("fetch Stay Informed news item: %w", err)
	}
	revision := Revision(item)
	found, err := s.DeliveryIndex.Contains(ctx, revision)
	if err != nil {
		return fmt.Errorf("check mailbox before delivery: %w", err)
	}
	if found {
		state.Deliveries[fingerprint] = Delivery{
			NewsID: item.Identifier(), ListFingerprint: fingerprint, Revision: revision,
			Status: StatusDelivered, Attempts: previous.Attempts, DeliveredAt: s.Now(),
		}
		result.Confirmed++
		return s.State.Save(*state)
	}

	attachments := make([]MailAttachment, 0, len(item.Attachments))
	var totalSize int64
	for _, metadata := range item.Attachments {
		data, fetchErr := api.Attachment(ctx, item.Identifier(), metadata.ID)
		if fetchErr != nil {
			return fmt.Errorf("fetch attachment %q: %w", metadata.Name, fetchErr)
		}
		totalSize += int64(len(data.Data))
		if totalSize > s.MaxMessageSize {
			return fmt.Errorf("attachments exceed the configured message size limit")
		}
		name := metadata.Name
		if name == "" {
			name = data.Filename
		}
		contentType := data.ContentType
		if contentType == "" {
			contentType = metadata.Type
		}
		attachments = append(attachments, MailAttachment{Name: name, ContentType: contentType, Data: data.Data})
	}
	message, err := BuildMessage(item, attachments, s.MessageOptions)
	if err != nil {
		return fmt.Errorf("build forwarded message: %w", err)
	}
	attempts := previous.Attempts + 1
	if err := s.Mailer.Send(ctx, message); err != nil {
		status := StatusRetryWait
		if attempts >= s.MaxAttempts {
			status = StatusFailed
			result.Failed++
		}
		state.Deliveries[fingerprint] = Delivery{
			NewsID: item.Identifier(), ListFingerprint: fingerprint, Revision: revision,
			Status: status, Attempts: attempts, LastAttempt: s.Now(),
		}
		if saveErr := s.State.Save(*state); saveErr != nil {
			return fmt.Errorf("SMTP delivery failed and state update failed: %v; %w", err, saveErr)
		}
		return fmt.Errorf("deliver message through SMTP: %w", err)
	}
	state.Deliveries[fingerprint] = Delivery{
		NewsID: item.Identifier(), ListFingerprint: fingerprint, Revision: revision,
		Status: StatusPending, Attempts: attempts, LastAttempt: s.Now(),
	}
	if err := s.State.Save(*state); err != nil {
		return fmt.Errorf("persist pending delivery after SMTP accepted the message: %w", err)
	}
	result.Sent++

	found, err = s.DeliveryIndex.Contains(ctx, revision)
	if err != nil {
		result.Pending++
		return fmt.Errorf("SMTP accepted the message but IMAP confirmation failed: %w", err)
	}
	if found {
		delivery := state.Deliveries[fingerprint]
		delivery.Status = StatusDelivered
		delivery.DeliveredAt = s.Now()
		state.Deliveries[fingerprint] = delivery
		result.Confirmed++
		return s.State.Save(*state)
	}
	result.Pending++
	return nil
}

func (s *Service) setDefaults() error {
	if s.OpenSession == nil || s.State == nil || s.Mailer == nil || s.DeliveryIndex == nil {
		return errors.New("session opener, state store, mailer, and delivery index are required")
	}
	if s.MaxPages == 0 {
		s.MaxPages = 100
	}
	if s.PageSize == 0 {
		s.PageSize = 20
	}
	if s.MaxAttempts == 0 {
		s.MaxAttempts = 3
	}
	if s.RetryGrace == 0 {
		s.RetryGrace = 15 * time.Minute
	}
	if s.MaxMessageSize == 0 {
		s.MaxMessageSize = 25 << 20
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.MaxPages < 1 || s.PageSize < 1 || s.MaxAttempts < 1 || s.RetryGrace < 0 || s.MaxMessageSize < 1 {
		return errors.New("message2mail limits must be positive")
	}
	return nil
}
