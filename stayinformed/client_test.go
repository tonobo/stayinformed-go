package stayinformed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCalendarFetchesProfileAndEvents(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("Authorization") != "Bearer access-token" {
			t.Fatalf("missing access token")
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/logins/me":
			fmt.Fprint(response, `{"id":"user-1","appid":"app-1","appname":"Example Calendar","timezone":"Europe/Berlin","success":"1"}`)
		case "/events/list":
			if request.Header.Get("Auth-Association") != "app-1" || request.Header.Get("Auth-User") != "user-1" {
				t.Fatalf("profile headers are missing")
			}
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			fmt.Fprint(response, `{"items":[{"date":"2026-09-15","events":[{"id":"event-1","title":"Example","start":"2026-09-15T13:00:00","end":"2026-09-15T14:00:00","groups":[{"id":"group-1","name":"Class A","hidden":false}]}]}]}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{APIBaseURL: server.URL, WebAppURL: server.URL, APIVersion: "1.2.3", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	calendar, err := client.Calendar(context.Background(), "access-token")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || calendar.Name != "Example Calendar" || len(calendar.Events) != 1 || calendar.Events[0].ID != "event-1" {
		t.Fatalf("unexpected calendar: %+v", calendar)
	}
	if len(calendar.Events[0].Groups) != 1 || calendar.Events[0].Groups[0].Name != "Class A" {
		t.Fatalf("unexpected event groups: %+v", calendar.Events[0].Groups)
	}
}
