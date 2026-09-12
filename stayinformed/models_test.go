package stayinformed

import (
	"encoding/json"
	"reflect"
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

func TestNewsPreservesAvailableMetadata(t *testing.T) {
	var news News
	err := json.Unmarshal([]byte(`{
		"id":"news-1",
		"groups":[{"id":"group-b","name":"Class B","hidden":true},{"id":"group-a","name":"Class A","hidden":false}],
		"favourite":true,
		"mode":"archive",
		"answeredMemberIds":["member-1"],
		"answeredMemberOtherIds":["member-2"],
		"options":[{"id":"option-1"}],
		"responses":[{"id":"response-1"}],
		"pdfs":[{"id":"document-1","name":"example.pdf"}],
		"show_child_name":true,
		"show_class":true,
		"show_comment":true,
		"show_signature":true,
		"videoId":"video-1"
	}`), &news)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(news.GroupNames(), []string{"Class A", "Class B"}) {
		t.Fatalf("group names = %v", news.GroupNames())
	}
	if !news.Groups[0].Hidden || !news.Favourite || news.Mode != "archive" || len(news.AnsweredMemberIDs) != 1 || len(news.AnsweredMemberOtherIDs) != 1 {
		t.Fatalf("scalar metadata was not preserved: %+v", news)
	}
	if len(news.Options) != 1 || len(news.Responses) != 1 || len(news.PDFs) != 1 || news.PDFs[0].Name != "example.pdf" {
		t.Fatalf("collection metadata was not preserved: %+v", news)
	}
	if !news.ShowChildName || !news.ShowClass || !news.ShowComment || !news.ShowSignature || news.VideoID == nil || *news.VideoID != "video-1" {
		t.Fatalf("response or video metadata was not preserved: %+v", news)
	}
}
