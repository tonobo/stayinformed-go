// Package stayinformed provides a read-only client for the private API used by
// the official Stay Informed parent web application.
//
// The caller owns authentication and supplies a Keycloak access token. The
// client never accepts account credentials, refreshes tokens, or retries API
// requests automatically.
package stayinformed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultAPIBaseURL      = "https://admin-api.stayinformed.de/api/app-user"
	defaultWebAppURL       = "https://app.stayinformed.de/"
	defaultLanguage        = "de"
	defaultTimezone        = "Europe/Berlin"
	defaultUserAgent       = "stayinformed-go/dev"
	defaultPageSize        = 20
	defaultMaxResponseSize = 16 << 20
	defaultMaxAttachment   = 25 << 20
)

var (
	appScriptPattern  = regexp.MustCompile(`(?i)<script[^>]+src=["']([^"']*/js/app\.[^"']+\.js)["']`)
	appVersionPattern = regexp.MustCompile(`VUE_APP_API_VERSION:"([^"]+)"`)
)

type Config struct {
	APIBaseURL        string
	WebAppURL         string
	APIVersion        string
	Language          string
	Timezone          string
	CalendarName      string
	UserAgent         string
	MaxResponseSize   int64
	MaxAttachmentSize int64
	HTTPClient        *http.Client
}

func DefaultConfig() Config {
	return Config{
		APIBaseURL:        defaultAPIBaseURL,
		WebAppURL:         defaultWebAppURL,
		Language:          defaultLanguage,
		Timezone:          defaultTimezone,
		UserAgent:         defaultUserAgent,
		MaxResponseSize:   defaultMaxResponseSize,
		MaxAttachmentSize: defaultMaxAttachment,
		HTTPClient:        &http.Client{Timeout: 30 * time.Second},
	}
}

type Client struct {
	config Config

	versionMu sync.Mutex
	version   string
}

func NewClient(config Config) (*Client, error) {
	defaults := DefaultConfig()
	if config.APIBaseURL == "" {
		config.APIBaseURL = defaults.APIBaseURL
	}
	if config.WebAppURL == "" {
		config.WebAppURL = defaults.WebAppURL
	}
	if config.Language == "" {
		config.Language = defaults.Language
	}
	if config.Timezone == "" {
		config.Timezone = defaults.Timezone
	}
	if config.UserAgent == "" {
		config.UserAgent = defaults.UserAgent
	}
	if config.MaxResponseSize <= 0 {
		config.MaxResponseSize = defaults.MaxResponseSize
	}
	if config.MaxAttachmentSize <= 0 {
		config.MaxAttachmentSize = defaults.MaxAttachmentSize
	}
	if config.HTTPClient == nil {
		config.HTTPClient = defaults.HTTPClient
	}
	for name, value := range map[string]string{"API base URL": config.APIBaseURL, "web app URL": config.WebAppURL} {
		parsed, err := url.ParseRequestURI(value)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("invalid %s", name)
		}
	}
	return &Client{config: config, version: config.APIVersion}, nil
}

// Session is a short-lived, authenticated API view. It retains the caller's
// access token in memory and performs no token refresh or request retry.
type Session struct {
	client      *Client
	accessToken string
	Profile     Profile
}

func (c *Client) OpenSession(ctx context.Context, accessToken string) (*Session, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, errors.New("access token is required")
	}
	var profile Profile
	if err := c.doJSON(ctx, http.MethodPost, c.apiURL("logins/me"), accessToken, nil, map[string]any{}, &profile); err != nil {
		return nil, fmt.Errorf("fetch profile: %w", err)
	}
	if profile.Successful != "1" {
		return nil, errors.New("profile response was not successful")
	}
	if profile.UserID == "" || profile.ApplicationID == "" {
		return nil, errors.New("profile response is missing user or application ID")
	}
	return &Session{client: c, accessToken: accessToken, Profile: profile}, nil
}

func (c *Client) Calendar(ctx context.Context, accessToken string) (Calendar, error) {
	session, err := c.OpenSession(ctx, accessToken)
	if err != nil {
		return Calendar{}, err
	}
	return session.Calendar(ctx)
}

func (s *Session) Calendar(ctx context.Context) (Calendar, error) {
	version, err := s.client.apiVersion(ctx)
	if err != nil {
		return Calendar{}, err
	}
	payload := map[string]any{
		"data": map[string]any{
			"Event": map[string]any{
				"objid":    s.Profile.ApplicationID,
				"userid":   s.Profile.UserID,
				"query":    "",
				"is_web":   true,
				"version":  version,
				"language": s.client.config.Language,
			},
		},
	}
	var response struct {
		Items []struct {
			Date   string  `json:"date"`
			Events []Event `json:"events"`
		} `json:"items"`
		LastCalendarUpdate string `json:"lastCalendarUpdate"`
	}
	if err := s.client.doJSON(ctx, http.MethodPost, s.client.apiURL("events/list"), s.accessToken, &s.Profile, payload, &response); err != nil {
		return Calendar{}, fmt.Errorf("fetch events: %w", err)
	}
	events := make([]Event, 0)
	for _, day := range response.Items {
		events = append(events, day.Events...)
	}
	timezone := s.Profile.Timezone
	if timezone == "" {
		timezone = s.client.config.Timezone
	}
	name := s.client.config.CalendarName
	if name == "" {
		name = s.Profile.ApplicationName
	}
	if name == "" {
		name = "Stay Informed"
	}
	return Calendar{Name: name, Timezone: timezone, LastCalendarUpdate: response.LastCalendarUpdate, Events: events}, nil
}

func (s *Session) NewsPage(ctx context.Context, options NewsOptions) (NewsPage, error) {
	if options.Offset < 0 {
		return NewsPage{}, errors.New("news offset must not be negative")
	}
	if options.PageSize == 0 {
		options.PageSize = defaultPageSize
	}
	if options.PageSize < 1 || options.PageSize > 100 {
		return NewsPage{}, errors.New("news page size must be between 1 and 100")
	}
	if options.Type == "" {
		options.Type = "all"
	}
	if options.ReceiverType == "" {
		options.ReceiverType = "all"
	}
	version, err := s.client.apiVersion(ctx)
	if err != nil {
		return NewsPage{}, err
	}
	news := map[string]any{
		"objid":                   s.Profile.ApplicationID,
		"userid":                  s.Profile.UserID,
		"offset":                  options.Offset,
		"loadsize":                options.PageSize,
		"query":                   options.Query,
		"hidden_only":             options.IncludeHidden,
		"os":                      "",
		"dpi":                     "",
		"xdpi":                    "",
		"density":                 "",
		"platformWidth":           "",
		"logicaldensityfactor":    "",
		"override_answered":       options.OverrideAnswered,
		"is_web":                  true,
		"filter_by_type":          options.Type,
		"filter_by_statuses":      options.Statuses,
		"filter_by_groups_ids":    options.GroupIDs,
		"filter_by_receiver_type": options.ReceiverType,
		"version":                 version,
		"language":                s.client.config.Language,
	}
	var page NewsPage
	payload := map[string]any{"data": map[string]any{"News": news}}
	if err := s.client.doJSON(ctx, http.MethodPost, s.client.apiURL("news/list"), s.accessToken, &s.Profile, payload, &page); err != nil {
		return NewsPage{}, fmt.Errorf("fetch news page: %w", err)
	}
	return page, nil
}

// NewsItem fetches full message details. The upstream application may count a
// detail fetch as viewing the message, so callers should not use it merely to
// enumerate messages.
func (s *Session) NewsItem(ctx context.Context, newsID string) (News, error) {
	if strings.TrimSpace(newsID) == "" {
		return News{}, errors.New("news ID is required")
	}
	version, err := s.client.apiVersion(ctx)
	if err != nil {
		return News{}, err
	}
	news := map[string]any{
		"objid":             s.Profile.ApplicationID,
		"userid":            s.Profile.UserID,
		"override_answered": "false",
		"is_web":            true,
		"version":           version,
		"language":          s.client.config.Language,
	}
	payload := map[string]any{"data": map[string]any{"News": news}}
	var item News
	if err := s.client.doJSON(ctx, http.MethodPost, s.client.apiURL("news/"+url.PathEscape(newsID)), s.accessToken, &s.Profile, payload, &item); err != nil {
		return News{}, fmt.Errorf("fetch news item: %w", err)
	}
	if item.Identifier() == "" {
		item.ObjectID = newsID
	}
	return item, nil
}

func (s *Session) Attachment(ctx context.Context, newsID, attachmentID string) (AttachmentData, error) {
	if strings.TrimSpace(newsID) == "" || strings.TrimSpace(attachmentID) == "" {
		return AttachmentData{}, errors.New("news ID and attachment ID are required")
	}
	endpoint := s.client.apiURL("news/" + url.PathEscape(newsID) + "/attachments/" + url.PathEscape(attachmentID))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return AttachmentData{}, err
	}
	s.client.setHeaders(request, s.accessToken, &s.Profile)
	request.Header.Set("Accept", "application/octet-stream, application/pdf, image/*")
	response, err := s.client.config.HTTPClient.Do(request)
	if err != nil {
		return AttachmentData{}, fmt.Errorf("fetch attachment: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return AttachmentData{}, s.client.responseError(response)
	}
	data, err := readLimited(response.Body, s.client.config.MaxAttachmentSize)
	if err != nil {
		return AttachmentData{}, fmt.Errorf("read attachment: %w", err)
	}
	filename := ""
	if _, params, err := mime.ParseMediaType(response.Header.Get("Content-Disposition")); err == nil {
		filename = filepath.Base(params["filename"])
	}
	return AttachmentData{Data: data, ContentType: response.Header.Get("Content-Type"), Filename: filename}, nil
}

func (c *Client) apiVersion(ctx context.Context) (string, error) {
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	if c.version != "" {
		return c.version, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.WebAppURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", c.config.UserAgent)
	response, err := c.config.HTTPClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("discover API version: fetch web app: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discover API version: web app returned HTTP status %d", response.StatusCode)
	}
	body, err := readLimited(response.Body, c.config.MaxResponseSize)
	if err != nil {
		return "", fmt.Errorf("discover API version: %w", err)
	}
	match := appScriptPattern.FindSubmatch(body)
	if len(match) != 2 {
		return "", errors.New("discover API version: app bundle was not found")
	}
	base, _ := url.Parse(c.config.WebAppURL)
	reference, err := url.Parse(html.UnescapeString(string(match[1])))
	if err != nil {
		return "", errors.New("discover API version: invalid app bundle URL")
	}
	bundleURL := base.ResolveReference(reference)
	if !sameOrigin(bundleURL, base) {
		return "", errors.New("discover API version: refusing cross-origin app bundle")
	}
	bundleRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, bundleURL.String(), nil)
	if err != nil {
		return "", err
	}
	bundleRequest.Header.Set("User-Agent", c.config.UserAgent)
	bundleResponse, err := c.config.HTTPClient.Do(bundleRequest)
	if err != nil {
		return "", fmt.Errorf("discover API version: fetch app bundle: %w", err)
	}
	defer bundleResponse.Body.Close()
	if bundleResponse.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discover API version: app bundle returned HTTP status %d", bundleResponse.StatusCode)
	}
	bundle, err := readLimited(bundleResponse.Body, c.config.MaxResponseSize)
	if err != nil {
		return "", fmt.Errorf("discover API version: %w", err)
	}
	versionMatch := appVersionPattern.FindSubmatch(bundle)
	if len(versionMatch) != 2 {
		return "", errors.New("discover API version: version was not found in app bundle")
	}
	c.version = string(versionMatch[1])
	return c.version, nil
}

func (c *Client) doJSON(ctx context.Context, method, endpoint, accessToken string, profile *Profile, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	c.setHeaders(request, accessToken, profile)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.config.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return c.responseError(response)
	}
	bodyBytes, err := readLimited(response.Body, c.config.MaxResponseSize)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(bodyBytes, output); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) setHeaders(request *http.Request, accessToken string, profile *Profile) {
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("User-Agent", c.config.UserAgent)
	request.Header.Set("Language", c.config.Language)
	request.Header.Set("Accept-Language", c.config.Language)
	request.Header.Set("Is-Web", "true")
	request.Header.Set("Origin", strings.TrimRight(c.config.WebAppURL, "/"))
	timezone := c.config.Timezone
	if profile != nil {
		request.Header.Set("Auth-Association", profile.ApplicationID)
		request.Header.Set("Auth-User", profile.UserID)
		if profile.Timezone != "" {
			timezone = profile.Timezone
		}
	}
	request.Header.Set("X-StayInformed-Timezone", timezone)
}

func (c *Client) responseError(response *http.Response) error {
	body, err := readLimited(response.Body, c.config.MaxResponseSize)
	if err != nil {
		return fmt.Errorf("upstream returned HTTP status %d", response.StatusCode)
	}
	var failure struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(body, &failure) == nil && failure.Message != "" {
		message := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return ' '
			}
			return r
		}, failure.Message)
		if len(message) > 200 {
			message = message[:200]
		}
		return fmt.Errorf("upstream returned HTTP status %d: %s", response.StatusCode, message)
	}
	return fmt.Errorf("upstream returned HTTP status %d", response.StatusCode)
}

func (c *Client) apiURL(path string) string {
	return strings.TrimRight(c.config.APIBaseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	return value, nil
}
