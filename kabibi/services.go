package main

import (
	"context"
	"os"
)

// FSEntry is a single item returned by FSService.ReadDir.
type FSEntry struct {
	Name  string
	IsDir bool
}

// FSService abstracts filesystem access for widgets that display directory contents.
type FSService interface {
	ReadDir(path string) ([]FSEntry, error)
}

// AIService abstracts the AI conversation backend for ChatWidget.
type AIService interface {
	SendMessage(ctx context.Context, text string, onToken func(string)) error
}

// OsFSService reads directory listings from the local filesystem.
type OsFSService struct{}

func (s *OsFSService) ReadDir(path string) ([]FSEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]FSEntry, len(entries))
	for i, e := range entries {
		result[i] = FSEntry{Name: e.Name(), IsDir: e.IsDir()}
	}
	return result, nil
}
