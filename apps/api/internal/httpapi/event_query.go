package httpapi

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/rendr17/dioffice/apps/api/internal/events"
)

type eventQuery struct {
	after *int64
	limit int
}

func parseStreamCursor(rawQuery string, headers http.Header) (int64, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, events.ErrInvalidQuery
	}
	for key := range values {
		if key != "after" {
			return 0, events.ErrInvalidQuery
		}
	}
	if lastIDs, supplied := headers[http.CanonicalHeaderKey("Last-Event-ID")]; supplied {
		if len(lastIDs) != 1 {
			return 0, events.ErrInvalidCursor
		}
		return parseEventCursor(lastIDs[0])
	}
	if raw, supplied := values["after"]; supplied {
		if len(raw) != 1 {
			return 0, events.ErrInvalidCursor
		}
		return parseEventCursor(raw[0])
	}
	return 0, nil
}

// Cursors are canonical nonnegative decimal int64 values, never signed/trimmed.
func parseEventCursor(raw string) (int64, error) {
	if raw == "" || len(raw) > 19 || (len(raw) > 1 && raw[0] == '0') {
		return 0, events.ErrInvalidCursor
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, events.ErrInvalidCursor
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, events.ErrInvalidCursor
	}
	return value, nil
}

func parseHistoryQuery(rawQuery string) (eventQuery, error) {
	query := eventQuery{limit: 100}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return query, events.ErrInvalidQuery
	}
	for key := range values {
		if key != "after" && key != "limit" {
			return query, events.ErrInvalidQuery
		}
	}
	if raw, ok := values["after"]; ok {
		if len(raw) != 1 {
			return query, events.ErrInvalidCursor
		}
		after, err := parseEventCursor(raw[0])
		if err != nil {
			return query, err
		}
		query.after = &after
	}
	if raw, ok := values["limit"]; ok {
		if len(raw) != 1 {
			return query, events.ErrInvalidQuery
		}
		limit, err := parseEventCursor(raw[0])
		if err != nil || limit < 1 || limit > 100 {
			return query, events.ErrInvalidQuery
		}
		query.limit = int(limit)
	}
	return query, nil
}
