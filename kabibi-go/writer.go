package main

import (
	"io"
	"sync"
)

type SwitchableWriter struct {
	mu     sync.Mutex
	target io.Writer
}

func (s *SwitchableWriter) SetTarget(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.target = w
}

func (s *SwitchableWriter) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.target == nil {
		return len(p), nil
	}
	return s.target.Write(p)
}
