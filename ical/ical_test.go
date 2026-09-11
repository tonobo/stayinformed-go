package ical

import (
	"strings"
	"testing"

	"github.com/tonobo/stayinformed-go/stayinformed"
)

func TestRenderCalendar(t *testing.T) {
	cancelled := "cancelled"
	calendar := stayinformed.Calendar{Name: "Example", Timezone: "Europe/Berlin", Events: []stayinformed.Event{
		{ID: "timed", Title: "Parents, meeting", Content: "<p>First line</p><p>Second line</p>", Venue: "Room; 1", Start: "2026-09-15T13:00:00", End: "2026-09-15T14:00:00", Modified: "2026-09-11T12:00:00"},
		{ID: "all-day", Title: "Closed", Start: "2026-09-16", End: "2026-09-16", AllDay: true, Status: &cancelled},
	}}
	data, err := Render(calendar, Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{
		"DTSTART:20260915T110000Z", "SUMMARY:Parents\\, meeting", "DESCRIPTION:First line\\nSecond line",
		"LOCATION:Room\\; 1", "DTSTART;VALUE=DATE:20260916", "DTEND;VALUE=DATE:20260917", "STATUS:CANCELLED",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("calendar does not contain %q:\n%s", expected, text)
		}
	}
}
