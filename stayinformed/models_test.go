package stayinformed

import (
	"encoding/json"
	"testing"
)

func TestNewsPageHasMorePrefersCountOverStaleFlag(t *testing.T) {
	page := NewsPage{
		Items: []News{{ID: "news-1"}},
		Count: 1,
		More:  json.RawMessage(`"1"`),
	}
	if page.HasMore(0, 20) {
		t.Fatal("stale upstream more flag must not override the total count")
	}
}

func TestNewsPageHasMoreStopsOnEmptyPage(t *testing.T) {
	page := NewsPage{More: json.RawMessage(`true`)}
	if page.HasMore(1, 20) {
		t.Fatal("an empty page must terminate pagination")
	}
}

func TestNewsPageHasMoreUsesCountForFullPages(t *testing.T) {
	items := make([]News, 20)
	page := NewsPage{Items: items, Count: 40}
	if !page.HasMore(0, 20) {
		t.Fatal("the first of two full pages must have a successor")
	}
	if page.HasMore(1, 20) {
		t.Fatal("the second of two full pages must be terminal")
	}
}
