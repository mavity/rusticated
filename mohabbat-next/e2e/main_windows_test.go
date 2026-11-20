//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

func TestPromptMatcherMatchesMarkerAcrossPTYReads(t *testing.T) {
	var matcher promptMatcher
	first := []byte("banner " + promptReadyMarker[:1])
	if _, found := matcher.observe(first, uint64(len(first)), 1, time.Now()); found {
		t.Fatal("matched an incomplete prompt marker")
	}

	second := []byte(promptReadyMarker[1:] + " ")
	observation, found := matcher.observe(second, uint64(len(first)+len(second)), 2, time.Now())
	if !found {
		t.Fatal("did not match the prompt marker split across PTY reads")
	}
	if observation.OutputBytes != uint64(len(first)+len(second)) || observation.ChunkSequence != 2 {
		t.Fatalf("observation = %#v, want end of second output chunk", observation)
	}
	if _, found := matcher.observe([]byte(promptReadyMarker), 99, 3, time.Now()); found {
		t.Fatal("reported the prompt marker more than once")
	}
}

func TestPromptMatcherDoesNotMatchOtherEllipses(t *testing.T) {
	var matcher promptMatcher
	if _, found := matcher.observe([]byte("... …"), 7, 1, time.Now()); found {
		t.Fatal("matched a non-Kabibi ellipsis")
	}
}

func TestWaitForPromptTimesOutWithoutMarker(t *testing.T) {
	state := &outputReader{
		done:        make(chan error, 1),
		promptReady: make(chan promptObservation, 1),
	}
	process := &processWait{done: make(chan struct{})}

	_, err := waitForPrompt(state, process, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), promptReadyMarker) {
		t.Fatalf("waitForPrompt() error = %v, want a prompt-marker timeout", err)
	}
}
