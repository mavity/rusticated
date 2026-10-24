package credstore

import (
	"fmt"
	"os"
	"sync"
)

var storeMu sync.Mutex

func entryKey(provider, account string) string {
	return provider + "\x00" + account
}

func Set(provider, account, secret string) error {
	if provider == "" || account == "" || secret == "" {
		return fmt.Errorf("provider, account, and secret must be non-empty")
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	entries, err := load()
	if err != nil {
		return err
	}
	entries[entryKey(provider, account)] = secret
	return save(entries)
}

func Get(provider, account string) (string, error) {
	if provider == "" || account == "" {
		return "", fmt.Errorf("provider and account must be non-empty")
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	entries, err := load()
	if err != nil {
		return "", err
	}
	secret, ok := entries[entryKey(provider, account)]
	if !ok {
		return "", os.ErrNotExist
	}
	return secret, nil
}

func Delete(provider, account string) error {
	if provider == "" || account == "" {
		return fmt.Errorf("provider and account must be non-empty")
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	entries, err := load()
	if err != nil {
		return err
	}
	delete(entries, entryKey(provider, account))
	return save(entries)
}
