package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLoginUsesPKCEAndOrganizationScope(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/authorize":
			if request.URL.Query().Get("scope") != defaultScope {
				t.Fatalf("unexpected scope: %q", request.URL.Query().Get("scope"))
			}
			if request.URL.Query().Get("organization") != "example" {
				t.Fatalf("organization was not forwarded")
			}
			if request.URL.Query().Get("code_challenge") == "" {
				t.Fatalf("PKCE challenge is missing")
			}
			fmt.Fprintf(response, `{"loginAction":%q}`, server.URL+"/login")
		case "/login":
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if request.Form.Get("username") != "user@example.invalid" || request.Form.Get("password") != "secret" {
				t.Fatalf("unexpected credentials form")
			}
			// The minimal test form action does not copy the authorization query.
			http.Redirect(response, request, server.URL+"/callback?code=code-1&state="+url.QueryEscape(authorizationState), http.StatusFound)
		case "/token":
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("code_verifier") == "" {
				t.Fatalf("unexpected token request: %v", request.Form)
			}
			response.Header().Set("Content-Type", "application/json")
			fmt.Fprint(response, `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":300}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	authorizationState = ""
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/authorize" {
			authorizationState = request.URL.Query().Get("state")
		}
		return http.DefaultTransport.RoundTrip(request)
	})
	client, err := NewClient(Config{
		AuthorizationURL: server.URL + "/authorize",
		TokenURL:         server.URL + "/token",
		RedirectURL:      server.URL + "/callback",
		ClientID:         "test-client",
		Scope:            defaultScope,
		HTTPClient:       &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := client.Login(context.Background(), "user@example.invalid", "secret", LoginOptions{Organization: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access" || tokens.RefreshToken != "refresh" || tokens.ExpiresAt.IsZero() {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}
}

var authorizationState string

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestParseLoginActionRejectsInvalidPage(t *testing.T) {
	if _, err := parseLoginAction([]byte("missing")); err == nil || !strings.Contains(err.Error(), "login action") {
		t.Fatalf("expected a login action error, got %v", err)
	}
}
