package main

import (
	"bytes"
	"sync"
	"testing"
)

func TestSwitchableWriterWithNilTarget(t *testing.T) {
	sw := &SwitchableWriter{}

	n, err := sw.Write([]byte("hello"))
	if err != nil {
		t.Errorf("Write with nil target error = %v, want nil", err)
	}
	if n != 5 {
		t.Errorf("Write with nil target returned n=%d, want 5", n)
	}
}

func TestSwitchableWriterWithTarget(t *testing.T) {
	var buf bytes.Buffer
	sw := &SwitchableWriter{}
	sw.SetTarget(&buf)

	n, err := sw.Write([]byte("hello"))
	if err != nil {
		t.Errorf("Write error = %v, want nil", err)
	}
	if n != 5 {
		t.Errorf("Write returned n=%d, want 5", n)
	}
	if buf.String() != "hello" {
		t.Errorf("buffer contains %q, want %q", buf.String(), "hello")
	}
}

func TestSwitchableWriterMultipleWrites(t *testing.T) {
	var buf bytes.Buffer
	sw := &SwitchableWriter{}
	sw.SetTarget(&buf)

	sw.Write([]byte("a"))
	sw.Write([]byte("b"))
	sw.Write([]byte("c"))

	if buf.String() != "abc" {
		t.Errorf("buffer contains %q, want %q", buf.String(), "abc")
	}
}

func TestSwitchableWriterSetTargetSwitch(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	sw := &SwitchableWriter{}

	sw.SetTarget(&buf1)
	sw.Write([]byte("first"))

	sw.SetTarget(&buf2)
	sw.Write([]byte("second"))

	if buf1.String() != "first" {
		t.Errorf("buf1 contains %q, want %q", buf1.String(), "first")
	}
	if buf2.String() != "second" {
		t.Errorf("buf2 contains %q, want %q", buf2.String(), "second")
	}
}

func TestSwitchableWriterConcurrentAccess(t *testing.T) {
	var buf bytes.Buffer
	sw := &SwitchableWriter{}
	sw.SetTarget(&buf)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				sw.Write([]byte{byte('0' + rune(id%10))})
			}
		}(i)
	}
	wg.Wait()

	if buf.Len() != 50 {
		t.Errorf("buffer contains %d bytes, want 50", buf.Len())
	}
}

func TestSwitchableWriterConcurrentSetTarget(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	sw := &SwitchableWriter{}
	sw.SetTarget(&buf1)

	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				sw.Write([]byte{byte('a' + rune(id))})
			}
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			sw.SetTarget(&buf2)
			sw.SetTarget(&buf1)
		}
	}()

	wg.Wait()

	total := buf1.Len() + buf2.Len()
	if total != 15 {
		t.Errorf("total bytes written = %d, want 15", total)
	}
}
