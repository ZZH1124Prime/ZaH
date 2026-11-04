package domain

import "testing"

func TestJobTransitions(t *testing.T) {
	states := []JobState{Queued, Running, WaitingUser, CancelRequested, Succeeded, Failed, Cancelled, Interrupted}
	for _, s := range states {
		if CanTransition(s, s) {
			t.Fatalf("self transition %s", s)
		}
		if s.Terminal() {
			for _, to := range states {
				if CanTransition(s, to) {
					t.Fatalf("terminal transition %s -> %s", s, to)
				}
			}
		}
	}
	if !CanTransition(Running, Interrupted) || !CanTransition(CancelRequested, Succeeded) || CanTransition(Interrupted, Running) {
		t.Fatal("recovery/cancellation boundary")
	}
}
func TestRandomIDs(t *testing.T) {
	a, b := NewID(), NewID()
	if len(a) != 32 || len(b) != 32 || a == b {
		t.Fatal("invalid identities")
	}
}
