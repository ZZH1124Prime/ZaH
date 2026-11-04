package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

const maxEventCursor int64 = 9007199254740991

// EventPage carries either incremental invalidations or a coherent replacement
// snapshot. Snapshot presence is the reset signal; a cursor advances only past
// events actually returned, or to the snapshot's exact database version.
type EventPage struct {
	Events     []Event                    `json:"events"`
	NextCursor int64                      `json:"next_cursor"`
	HasMore    bool                       `json:"has_more"`
	Snapshot   map[string][]domain.Record `json:"snapshot,omitempty"`
}

func recordChanged(ctx context.Context, q queryer, kind, id string, deleted bool) error {
	data, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		Deleted bool   `json:"deleted,omitempty"`
	}{kind, id, deleted})
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO events(type,data,created_at) VALUES('record.changed',?,?)`, string(data), now())
	return err
}

// ResumeEvents reads cursor, invalidations and (when needed) catalog in one WAL
// read transaction. No retention policy is imposed here; gaps from any removed
// history recover through a replacement snapshot rather than silently skipping.
func (s *Store) ResumeEvents(ctx context.Context, after *int64, limit int) (EventPage, error) {
	page := EventPage{Events: []Event{}}
	if after != nil && (*after < 0 || *after > maxEventCursor) {
		return page, domain.ErrInvalid
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return page, domain.ErrInvalid
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return page, err
	}
	defer conn.Close()
	// Explicit DEFERRED avoids the write reservation configured for mutation
	// transactions. Pinning one connection keeps all reads on the same snapshot.
	if _, err = conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		return page, err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var latest int64
	// sqlite_sequence preserves the high-water mark even if every event was pruned.
	if err = conn.QueryRowContext(ctx, `SELECT MAX(COALESCE((SELECT seq FROM sqlite_sequence WHERE name='events'),0),COALESCE((SELECT MAX(seq) FROM events),0))`).Scan(&latest); err != nil {
		return page, err
	}
	if latest > maxEventCursor {
		return page, fmt.Errorf("%w: event cursor exceeds JSON safe integer range", domain.ErrConflict)
	}
	reset := after == nil || *after > latest
	if !reset {
		page.NextCursor = *after
		rows, e := conn.QueryContext(ctx, `SELECT seq,type,
 CASE WHEN json_type(data,'$.kind')='text' THEN substr(json_extract(data,'$.kind'),1,32) ELSE '' END,
 CASE WHEN json_type(data,'$.id')='text' THEN substr(json_extract(data,'$.id'),1,257) ELSE '' END,
 created_at FROM events WHERE seq>? ORDER BY seq LIMIT ?`, *after, limit+1)
		if e != nil {
			return page, e
		}
		found := []Event{}
		expected := *after + 1
		for rows.Next() {
			var event Event
			var kind, id, created string
			if e = rows.Scan(&event.Seq, &event.Type, &kind, &id, &created); e != nil {
				rows.Close()
				return page, e
			}
			if event.Seq != expected {
				reset = true
			}
			expected = event.Seq + 1
			event.Data = eventNotification(event.Type, kind, id)
			if event.CreatedAt, e = time.Parse(time.RFC3339Nano, created); e != nil {
				rows.Close()
				return page, e
			}
			found = append(found, event)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return page, e
		}
		if len(found) < limit+1 && expected-1 < latest {
			reset = true
		}
		if !reset {
			if len(found) > limit {
				found = found[:limit]
			}
			page.Events = found
			if len(found) > 0 {
				page.NextCursor = found[len(found)-1].Seq
			}
			page.HasMore = page.NextCursor < latest
		}
	}
	if reset {
		kinds := []string{"family", "release", "instance", "environment", "artifact", "binding", "capability", "job"}
		records, e := snapshotRecords(ctx, conn, kinds)
		if e != nil {
			return page, e
		}
		page.Snapshot = make(map[string][]domain.Record, len(kinds))
		for _, kind := range kinds {
			page.Snapshot[kind] = []domain.Record{}
		}
		for _, record := range records {
			page.Snapshot[record.Kind] = append(page.Snapshot[record.Kind], record)
		}
		page.Events = []Event{}
		page.NextCursor = latest
		page.HasMore = false
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return EventPage{}, err
	}
	return page, nil
}

// eventNotification exposes bounded invalidation hints, never stored job results
// or arbitrary caller data. The full event remains available through Events.
func eventNotification(eventType, kind, id string) json.RawMessage {
	switch eventType {
	case "record.changed":
		if _, ok := tables[kind]; !ok {
			kind, id = "unknown", ""
		}
	case "job.queued", "job.changed", "plan.discarded":
		kind = "job"
	case "instance.forgotten":
		kind = "instance"
	default:
		kind, id = "unknown", ""
	}
	// An unusual identity still invalidates its entire kind. Restrict the hint to
	// printable ASCII and 256 bytes so a 500-event page cannot inflate via JSON
	// escaping. Persisted identity and audit evidence are unchanged.
	if len(id) > 256 {
		id = ""
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':') {
			id = ""
			break
		}
	}
	data, _ := json.Marshal(struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{kind, id})
	return data
}
