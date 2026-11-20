//go:build windows

package main

import (
	"math/rand"
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

func TestHumanKeyDelayUsesDocumentedCadenceRanges(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	tests := []struct {
		name     string
		previous byte
		current  byte
		minimum  time.Duration
		maximum  time.Duration
	}{
		{name: "alternating hands", previous: 'f', current: 'j', minimum: 45 * time.Millisecond, maximum: 95 * time.Millisecond},
		{name: "same hand", previous: 'f', current: 'd', minimum: 110 * time.Millisecond, maximum: 210 * time.Millisecond},
		{name: "after a space", previous: ' ', current: 'w', minimum: 80 * time.Millisecond, maximum: 150 * time.Millisecond},
		{name: "after punctuation", previous: '.', current: 'w', minimum: 220 * time.Millisecond, maximum: 550 * time.Millisecond},
		{name: "backspace repetition", previous: 0x7f, current: 0x7f, minimum: 75 * time.Millisecond, maximum: 140 * time.Millisecond},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := humanKeyDelay(test.previous, test.current, rng)
			if got < test.minimum || got > test.maximum {
				t.Fatalf("humanKeyDelay() = %s, want [%s, %s]", got, test.minimum, test.maximum)
			}
		})
	}
}

func TestHumanKeyDelayIsReproducibleFromSeed(t *testing.T) {
	first := rand.New(rand.NewSource(42))
	second := rand.New(rand.NewSource(42))
	for _, pair := range [][2]byte{{'f', 'j'}, {' ', 'w'}, {'.', 'a'}, {'d', 'f'}} {
		if got, want := humanKeyDelay(pair[0], pair[1], first), humanKeyDelay(pair[0], pair[1], second); got != want {
			t.Fatalf("same-seed delays differ: got %s, want %s", got, want)
		}
	}
}

func TestPhaseCounterDeltasSkipPhaseBoundaries(t *testing.T) {
	sampler := processSampler{samples: []processSample{
		{Phase: "idle", User100NS: 10000, Kernel100NS: 20000, PageFaults: 2},
		{Phase: "idle", User100NS: 30000, Kernel100NS: 40000, PageFaults: 4},
		{Phase: "typing", User100NS: 50000, Kernel100NS: 60000, PageFaults: 7},
		{Phase: "typing", User100NS: 90000, Kernel100NS: 100000, PageFaults: 9},
	}}

	got := sampler.phaseCounterDeltas()
	if got["idle"].SampleIntervals != 1 || got["typing"].SampleIntervals != 1 {
		t.Fatalf("phase sample intervals = %#v, want one within-phase interval per phase", got)
	}
	if got["idle"].UserCPUMS != 2 || got["typing"].UserCPUMS != 4 {
		t.Fatalf("phase user CPU deltas = %#v, want 2 ms idle and 4 ms typing", got)
	}
	if got["idle"].PageFaults != 2 || got["typing"].PageFaults != 2 {
		t.Fatalf("phase page-fault deltas = %#v, want 2 per phase", got)
	}
}
