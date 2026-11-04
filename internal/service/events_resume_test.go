package service

import (
	"context"
	"encoding/json"
	"testing"
)

func TestResumeEventsParameterBoundary(t *testing.T) {
	s := catalogService(t)
	for _, raw := range []string{`{}`, `{"after":null}`, `{"after":0,"limit":1}`, `{"after":9007199254740991,"limit":500}`} {
		if _, err := s.Handle(context.Background(), "events.resume", json.RawMessage(raw)); err != nil {
			t.Errorf("valid %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"after":-1}`, `{"after":1.5}`, `{"after":9007199254740992}`, `{"after":"0"}`, `{"limit":null}`, `{"limit":0}`, `{"limit":501}`, `{"limit":1.5}`, `{"snapshot":true}`} {
		if _, err := s.Handle(context.Background(), "events.resume", json.RawMessage(raw)); err == nil {
			t.Errorf("accepted invalid %s", raw)
		}
	}
}
