// Package ical renders Stay Informed events as an RFC 5545 calendar.
// It performs no network access and has no authentication concerns.
package ical

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tonobo/stayinformed-go/stayinformed"
)

const defaultProductID = "-//stayinformed-go//stayinformed-go//EN"

var (
	breakPattern = regexp.MustCompile(`(?i)<br\s*/?>`)
	blockPattern = regexp.MustCompile(`(?i)</(?:p|div|li|h[1-6])\s*>`)
	tagPattern   = regexp.MustCompile(`<[^>]+>`)
)

type Options struct {
	ProductID    string
	CalendarName string
	Timezone     string
}

func Render(calendar stayinformed.Calendar, options Options) ([]byte, error) {
	if options.ProductID == "" {
		options.ProductID = defaultProductID
	}
	if options.CalendarName == "" {
		options.CalendarName = calendar.Name
	}
	if options.CalendarName == "" {
		options.CalendarName = "Stay Informed"
	}
	if options.Timezone == "" {
		options.Timezone = calendar.Timezone
	}
	if options.Timezone == "" {
		options.Timezone = "Europe/Berlin"
	}
	location, err := time.LoadLocation(options.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", options.Timezone, err)
	}

	events := deduplicate(calendar.Events)
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Start == events[j].Start {
			return events[i].ID < events[j].ID
		}
		return events[i].Start < events[j].Start
	})

	var output strings.Builder
	writeLine(&output, "BEGIN:VCALENDAR")
	writeLine(&output, "VERSION:2.0")
	writeLine(&output, "PRODID:"+escapeText(options.ProductID))
	writeLine(&output, "CALSCALE:GREGORIAN")
	writeLine(&output, "METHOD:PUBLISH")
	writeLine(&output, "X-WR-CALNAME:"+escapeText(options.CalendarName))
	writeLine(&output, "X-WR-TIMEZONE:"+escapeText(options.Timezone))
	writeLine(&output, "REFRESH-INTERVAL;VALUE=DURATION:PT5M")
	writeLine(&output, "X-PUBLISHED-TTL:PT5M")

	for _, event := range events {
		if err := writeEvent(&output, event, location); err != nil {
			return nil, fmt.Errorf("render event %q: %w", event.ID, err)
		}
	}
	writeLine(&output, "END:VCALENDAR")
	return []byte(output.String()), nil
}

func writeEvent(output *strings.Builder, event stayinformed.Event, location *time.Location) error {
	start, err := parseTime(event.Start, location)
	if err != nil {
		return fmt.Errorf("parse start: %w", err)
	}
	end, err := parseTime(event.End, location)
	if err != nil {
		return fmt.Errorf("parse end: %w", err)
	}
	if event.AllDay {
		start = dateOnly(start, location)
		end = dateOnly(end, location)
		if !end.After(start) {
			end = start.AddDate(0, 0, 1)
		}
	} else if !end.After(start) {
		return errors.New("end must be after start")
	}

	modified := start
	if event.Modified != "" {
		if parsed, parseErr := parseTime(event.Modified, location); parseErr == nil {
			modified = parsed
		}
	}
	uid := event.ID
	if uid == "" {
		hash := sha256.Sum256([]byte(event.Title + "\x00" + event.Start + "\x00" + event.End))
		uid = hex.EncodeToString(hash[:16])
	}

	writeLine(output, "BEGIN:VEVENT")
	writeLine(output, "UID:"+escapeText(uid)+"@stayinformed-go")
	writeLine(output, "DTSTAMP:"+modified.UTC().Format("20060102T150405Z"))
	writeLine(output, "LAST-MODIFIED:"+modified.UTC().Format("20060102T150405Z"))
	if event.AllDay {
		writeLine(output, "DTSTART;VALUE=DATE:"+start.Format("20060102"))
		writeLine(output, "DTEND;VALUE=DATE:"+end.Format("20060102"))
	} else {
		writeLine(output, "DTSTART:"+start.UTC().Format("20060102T150405Z"))
		writeLine(output, "DTEND:"+end.UTC().Format("20060102T150405Z"))
	}
	writeLine(output, "SUMMARY:"+escapeText(event.Title))
	if groups := groupNames(event.Groups); len(groups) > 0 {
		writeLine(output, "CATEGORIES:"+escapeTextList(groups))
	}
	if text := plainText(event.Content); text != "" {
		writeLine(output, "DESCRIPTION:"+escapeText(text))
	}
	if event.Venue != "" {
		writeLine(output, "LOCATION:"+escapeText(event.Venue))
	}
	if event.Type != "" {
		writeLine(output, "X-STAYINFORMED-TYPE:"+escapeText(event.Type))
	}
	if event.Deleted || equalFoldAny(valueOrEmpty(event.Status), "deleted", "cancelled", "canceled") {
		writeLine(output, "STATUS:CANCELLED")
	} else {
		writeLine(output, "STATUS:CONFIRMED")
	}
	writeLine(output, "TRANSP:TRANSPARENT")
	writeLine(output, "END:VEVENT")
	return nil
}

func groupNames(groups []stayinformed.Group) []string {
	seen := make(map[string]struct{}, len(groups))
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func deduplicate(events []stayinformed.Event) []stayinformed.Event {
	byID := make(map[string]stayinformed.Event, len(events))
	withoutID := make([]stayinformed.Event, 0)
	for _, event := range events {
		if event.ID == "" {
			withoutID = append(withoutID, event)
			continue
		}
		current, exists := byID[event.ID]
		if !exists || event.Modified > current.Modified {
			byID[event.ID] = event
		}
	}
	result := make([]stayinformed.Event, 0, len(byID)+len(withoutID))
	for _, event := range byID {
		result = append(result, event)
	}
	return append(result, withoutID...)
}

func parseTime(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

func dateOnly(value time.Time, location *time.Location) time.Time {
	year, month, day := value.In(location).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, location)
}

func plainText(value string) string {
	value = breakPattern.ReplaceAllString(value, "\n")
	value = blockPattern.ReplaceAllString(value, "\n")
	value = tagPattern.ReplaceAllString(value, "")
	value = html.UnescapeString(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = strings.TrimSpace(lines[index])
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func escapeText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, ";", "\\;")
	value = strings.ReplaceAll(value, ",", "\\,")
	return strings.ReplaceAll(value, "\n", "\\n")
}

func escapeTextList(values []string) string {
	escaped := make([]string, len(values))
	for index, value := range values {
		escaped[index] = escapeText(value)
	}
	return strings.Join(escaped, ",")
}

func writeLine(output *strings.Builder, line string) {
	lineBytes := 0
	for len(line) > 0 {
		r, size := utf8.DecodeRuneInString(line)
		limit := 75
		if lineBytes > 0 && lineBytes+size > limit {
			output.WriteString("\r\n ")
			lineBytes = 1
		}
		output.WriteRune(r)
		lineBytes += size
		line = line[size:]
	}
	output.WriteString("\r\n")
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func equalFoldAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
}
