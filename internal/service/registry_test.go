package service

import (
	"errors"
	"testing"

	"github.com/XSpring/minipsi/internal/psi"
)

func TestMemStore(t *testing.T) {
	s := newMemStore(2)
	a, b := &record{id: "a"}, &record{id: "b"}
	if err := s.add(a); err != nil {
		t.Fatal(err)
	}
	if err := s.add(&record{id: "a"}); !errors.Is(err, psi.ErrSessionExists) {
		t.Errorf("same ID: %v, want ErrSessionExists", err)
	}
	if err := s.add(b); err != nil {
		t.Fatal(err)
	}
	if err := s.add(&record{id: "c"}); !errors.Is(err, psi.ErrBusy) {
		t.Errorf("full: %v, want ErrBusy", err)
	}
	if s.get("a") != a || s.get("x") != nil {
		t.Error("get")
	}
	// remove takes the record, not the ID: an old record does not remove a new one.
	s.remove(a)
	newA := &record{id: "a"}
	if err := s.add(newA); err != nil {
		t.Fatal(err)
	}
	s.remove(a)
	if s.get("a") != newA {
		t.Error("an old record removed the new one")
	}
}

func TestStateEnded(t *testing.T) {
	for st, want := range map[state]bool{
		stateStarting: false, stateReady: false, stateRunning: false,
		stateRejected: true, stateDone: true, stateFailed: true, stateAborted: true,
	} {
		if st.ended() != want {
			t.Errorf("%s: ended %v", st, st.ended())
		}
	}
}
