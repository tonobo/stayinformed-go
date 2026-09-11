// Package auth implements the explicit OpenID Connect login used by the
// Stay Informed parent application.
//
// Login and Refresh each perform exactly one authentication attempt. The
// package does not persist credentials or tokens and never refreshes a token
// implicitly.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	defaultAuthorizationURL = "https://login.stayinformed.de/realms/stayinformed/protocol/openid-connect/auth"
	defaultTokenURL         = "https://login.stayinformed.de/realms/stayinformed/protocol/openid-connect/token"
	defaultRedirectURL      = "https://app.stayinformed.de/"
	defaultClientID         = "parent-client"
	defaultScope            = "openid email profile organization"
	defaultUserAgent        = "stayinformed-go/dev"
	maxResponseSize         = 2 << 20
)

var loginActionPattern = regexp.MustCompile(`"loginAction"\s*:\s*("(?:\\.|[^"\\])*")`)

var ErrAuthenticationFailed = errors.New("authentication failed")

type Config struct {
	AuthorizationURL string
	TokenURL         string
	RedirectURL      string
	ClientID         string
	Scope            string
	UserAgent        string
	HTTPClient       *http.Client
}

func DefaultConfig() Config {
	return Config{
		AuthorizationURL: defaultAuthorizationURL,
		TokenURL:         defaultTokenURL,
		RedirectURL:      defaultRedirectURL,
		ClientID:         defaultClientID,
		Scope:            defaultScope,
		UserAgent:        defaultUserAgent,
		HTTPClient:       &http.Client{Timeout: 30 * time.Second},
	}
}

type Client struct {
	config Config
}

func NewClient(config Config) (*Client, error) {
	defaults := DefaultConfig()
	if config.AuthorizationURL == "" {
		config.AuthorizationURL = defaults.AuthorizationURL
	}
	if config.TokenURL == "" {
		config.TokenURL = defaults.TokenURL
	}
	if config.RedirectURL == "" {
		config.RedirectURL = defaults.RedirectURL
	}
	if config.ClientID == "" {
		config.ClientID = defaults.ClientID
	}
	if config.Scope == "" {
		config.Scope = defaults.Scope
	}
	if config.UserAgent == "" {
		config.UserAgent = defaults.UserAgent
	}
	if config.HTTPClient == nil {
		config.HTTPClient = defaults.HTTPClient
	}
	for name, rawURL := range map[string]string{
		"authorization URL": config.AuthorizationURL,
		"token URL":         config.TokenURL,
		"redirect URL":      config.RedirectURL,
	} {
		if _, err := url.ParseRequestURI(rawURL); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", name, err)
		}
	}
	return &Client{config: config}, nil
}

type LoginOptions struct {
	Organization string
	Locale       string
}

type Tokens struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	TokenType        string    `json:"token_type"`
	Scope            string    `json:"scope,omitempty"`
	ExpiresIn        int       `json:"expires_in"`
	RefreshExpiresIn int       `json:"refresh_expires_in,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
}

func (c *Client) Login(ctx context.Context, username, password string, options LoginOptions) (Tokens, error) {
	if strings.TrimSpace(username) == "" || password == "" {
		return Tokens{}, errors.New("username and password are required")
	}

	verifier, err := randomString(48)
	if err != nil {
		return Tokens{}, fmt.Errorf("create PKCE verifier: %w", err)
	}
	state, err := randomString(32)
	if err != nil {
		return Tokens{}, fmt.Errorf("create OAuth state: %w", err)
	}
	challengeSum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeSum[:])

	authorizationURL, err := url.Parse(c.config.AuthorizationURL)
	if err != nil {
		return Tokens{}, fmt.Errorf("parse authorization URL: %w", err)
	}
	query := authorizationURL.Query()
	query.Set("client_id", c.config.ClientID)
	query.Set("redirect_uri", c.config.RedirectURL)
	query.Set("response_type", "code")
	query.Set("response_mode", "query")
	query.Set("scope", c.config.Scope)
	query.Set("code_challenge_method", "S256")
	query.Set("code_challenge", challenge)
	query.Set("state", state)
	if options.Organization != "" {
		query.Set("organization", options.Organization)
	}
	if options.Locale != "" {
		query.Set("kc_locale", options.Locale)
		query.Set("ui_locales", options.Locale)
	}
	authorizationURL.RawQuery = query.Encode()

	session, err := c.newSession()
	if err != nil {
		return Tokens{}, err
	}
	page, err := c.do(ctx, session, http.MethodGet, authorizationURL.String(), "", nil, "")
	if err != nil {
		return Tokens{}, fmt.Errorf("open login page: %w", err)
	}
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK {
		return Tokens{}, fmt.Errorf("open login page: unexpected HTTP status %d", page.StatusCode)
	}
	body, err := readLimited(page.Body, maxResponseSize)
	if err != nil {
		return Tokens{}, fmt.Errorf("read login page: %w", err)
	}
	action, err := parseLoginAction(body)
	if err != nil {
		return Tokens{}, err
	}
	if !sameOrigin(action, c.config.AuthorizationURL) {
		return Tokens{}, errors.New("login page returned an action on a different origin")
	}

	form := url.Values{
		"username":     {username},
		"password":     {password},
		"credentialId": {""},
	}
	loginResponse, err := c.do(ctx, session, http.MethodPost, action, "", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return Tokens{}, fmt.Errorf("submit login form: %w", err)
	}
	defer loginResponse.Body.Close()
	if loginResponse.StatusCode == http.StatusOK {
		return Tokens{}, ErrAuthenticationFailed
	}
	if loginResponse.StatusCode < 300 || loginResponse.StatusCode >= 400 {
		return Tokens{}, fmt.Errorf("submit login form: unexpected HTTP status %d", loginResponse.StatusCode)
	}

	redirect, err := loginResponse.Location()
	if err != nil {
		return Tokens{}, errors.New("login response did not contain a redirect")
	}
	if !sameRedirect(redirect.String(), c.config.RedirectURL) {
		return Tokens{}, errors.New("login response returned an unexpected redirect")
	}
	values := redirect.Query()
	if oauthError := values.Get("error"); oauthError != "" {
		return Tokens{}, fmt.Errorf("OAuth authorization failed: %s", oauthError)
	}
	if values.Get("state") != state {
		return Tokens{}, errors.New("OAuth state mismatch")
	}
	code := values.Get("code")
	if code == "" {
		return Tokens{}, errors.New("login response did not contain an authorization code")
	}

	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {c.config.ClientID},
		"redirect_uri":  {c.config.RedirectURL},
		"code":          {code},
		"code_verifier": {verifier},
	}
	return c.requestTokens(ctx, session, tokenForm)
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Tokens{}, errors.New("refresh token is required")
	}
	session, err := c.newSession()
	if err != nil {
		return Tokens{}, err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.config.ClientID},
		"refresh_token": {refreshToken},
	}
	return c.requestTokens(ctx, session, form)
}

func (c *Client) requestTokens(ctx context.Context, session *http.Client, form url.Values) (Tokens, error) {
	response, err := c.do(ctx, session, http.MethodPost, c.config.TokenURL, "", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return Tokens{}, fmt.Errorf("request token: %w", err)
	}
	defer response.Body.Close()
	body, err := readLimited(response.Body, maxResponseSize)
	if err != nil {
		return Tokens{}, fmt.Errorf("read token response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &failure)
		if failure.Error != "" {
			return Tokens{}, fmt.Errorf("token endpoint rejected request: %s", failure.Error)
		}
		return Tokens{}, fmt.Errorf("token endpoint returned HTTP status %d", response.StatusCode)
	}
	var tokens Tokens
	if err := json.Unmarshal(body, &tokens); err != nil {
		return Tokens{}, fmt.Errorf("decode token response: %w", err)
	}
	if tokens.AccessToken == "" {
		return Tokens{}, errors.New("token response did not contain an access token")
	}
	if tokens.TokenType == "" {
		tokens.TokenType = "Bearer"
	}
	tokens.ExpiresAt = time.Now().UTC().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	return tokens, nil
}

func (c *Client) newSession() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	base := c.config.HTTPClient
	session := *base
	session.Jar = jar
	session.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if sameRedirect(request.URL.String(), c.config.RedirectURL) {
			return http.ErrUseLastResponse
		}
		if sameOrigin(request.URL.String(), c.config.AuthorizationURL) {
			return nil
		}
		return errors.New("refusing cross-origin authentication redirect")
	}
	return &session, nil
}

func (c *Client) do(ctx context.Context, client *http.Client, method, rawURL, bearer string, body io.Reader, contentType string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json, text/html;q=0.9")
	request.Header.Set("User-Agent", c.config.UserAgent)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	return client.Do(request)
}

func parseLoginAction(page []byte) (string, error) {
	match := loginActionPattern.FindSubmatch(page)
	if len(match) != 2 {
		return "", errors.New("login page did not contain a login action")
	}
	var action string
	if err := json.Unmarshal(match[1], &action); err != nil {
		return "", errors.New("login page contained an invalid login action")
	}
	if _, err := url.ParseRequestURI(action); err != nil {
		return "", errors.New("login page contained an invalid login action URL")
	}
	return action, nil
}

func randomString(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func sameOrigin(left, right string) bool {
	a, err := url.Parse(left)
	if err != nil {
		return false
	}
	b, err := url.Parse(right)
	if err != nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func sameRedirect(actual, expected string) bool {
	a, err := url.Parse(actual)
	if err != nil {
		return false
	}
	b, err := url.Parse(expected)
	if err != nil {
		return false
	}
	return sameOrigin(actual, expected) && a.EscapedPath() == b.EscapedPath()
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)
	value, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	return value, nil
}
