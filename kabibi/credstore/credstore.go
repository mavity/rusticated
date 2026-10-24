package credstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"ai"
)

type CredentialType = ai.CredentialType
type Credential = ai.Credential

const (
	CredentialAPIKey = ai.CredentialAPIKey
	CredentialOAuth  = ai.CredentialOAuth
)

// CredentialInfo summarizes a stored credential without exposing secret values.
type CredentialInfo struct {
	ProviderID string         `json:"provider_id"`
	Type       CredentialType `json:"type"`
}

var storeMu sync.Mutex

func entryKey(provider, account string) string {
	return provider + "\x00" + account
}

func cloneCredential(c *Credential) *Credential {
	if c == nil {
		return nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		cp := *c
		return &cp
	}
	var out Credential
	if err := json.Unmarshal(data, &out); err != nil {
		cp := *c
		return &cp
	}
	return &out
}

func decodeStoredCredential(raw string) (*Credential, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") {
		var cred Credential
		if err := json.Unmarshal([]byte(trimmed), &cred); err == nil && (cred.Type == CredentialAPIKey || cred.Type == CredentialOAuth) {
			return &cred, nil
		}
	}
	return &Credential{
		Type: CredentialAPIKey,
		Key:  raw,
	}, nil
}

func encodeStoredCredential(cred *Credential) (string, error) {
	if cred == nil {
		return "", fmt.Errorf("credential must not be nil")
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return "", fmt.Errorf("encode credential: %w", err)
	}
	return string(data), nil
}

// Read returns the stored credential for providerID, or (nil, nil) if none is stored.
func Read(ctx context.Context, providerID string) (*Credential, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if providerID == "" {
		return nil, fmt.Errorf("provider ID must be non-empty")
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	entries, err := load()
	if err != nil {
		return nil, err
	}
	if raw, ok := entries[providerID]; ok {
		return decodeStoredCredential(raw)
	}
	if raw, ok := entries[entryKey(providerID, "api-key")]; ok {
		return decodeStoredCredential(raw)
	}
	if raw, ok := entries[entryKey(providerID, "default")]; ok {
		return decodeStoredCredential(raw)
	}
	return nil, nil
}

// Modify performs a serialized read-modify-write on providerID's credential.
// If fn returns a non-nil *Credential, it is persisted before releasing the lock.
// If fn returns (nil, nil), the existing stored entry is left unchanged and returned.
func Modify(ctx context.Context, providerID string, fn func(current *Credential) (*Credential, error)) (*Credential, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if providerID == "" {
		return nil, fmt.Errorf("provider ID must be non-empty")
	}
	if fn == nil {
		return nil, fmt.Errorf("modify callback must not be nil")
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	entries, err := load()
	if err != nil {
		return nil, err
	}
	var current *Credential
	if raw, ok := entries[providerID]; ok {
		current, err = decodeStoredCredential(raw)
		if err != nil {
			return nil, err
		}
	} else if raw, ok := entries[entryKey(providerID, "api-key")]; ok {
		current, err = decodeStoredCredential(raw)
		if err != nil {
			return nil, err
		}
	} else if raw, ok := entries[entryKey(providerID, "default")]; ok {
		current, err = decodeStoredCredential(raw)
		if err != nil {
			return nil, err
		}
	}
	next, err := fn(cloneCredential(current))
	if err != nil {
		return nil, err
	}
	if next == nil {
		return cloneCredential(current), nil
	}
	encoded, err := encodeStoredCredential(next)
	if err != nil {
		return nil, err
	}
	entries[providerID] = encoded
	delete(entries, entryKey(providerID, "api-key"))
	delete(entries, entryKey(providerID, "default"))
	if err := save(entries); err != nil {
		return nil, err
	}
	return cloneCredential(next), nil
}

// Remove deletes any stored credential for providerID.
func Remove(ctx context.Context, providerID string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if providerID == "" {
		return fmt.Errorf("provider ID must be non-empty")
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	entries, err := load()
	if err != nil {
		return err
	}
	delete(entries, providerID)
	delete(entries, entryKey(providerID, "api-key"))
	delete(entries, entryKey(providerID, "default"))
	return save(entries)
}

// List returns non-secret metadata for all stored provider credentials.
func List(ctx context.Context) ([]CredentialInfo, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	entries, err := load()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]CredentialType, len(entries))
	for k, raw := range entries {
		providerID := k
		if idx := strings.IndexByte(k, 0); idx >= 0 {
			providerID = k[:idx]
		}
		if providerID == "" {
			continue
		}
		cred, decErr := decodeStoredCredential(raw)
		if decErr != nil || cred == nil {
			continue
		}
		if _, exists := seen[providerID]; !exists || !strings.Contains(k, "\x00") {
			seen[providerID] = cred.Type
		}
	}
	infos := make([]CredentialInfo, 0, len(seen))
	for providerID, credType := range seen {
		infos = append(infos, CredentialInfo{ProviderID: providerID, Type: credType})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].ProviderID < infos[j].ProviderID
	})
	return infos, nil
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
	if account == "api-key" || account == "default" {
		if encoded, encErr := encodeStoredCredential(&Credential{Type: CredentialAPIKey, Key: secret}); encErr == nil {
			entries[provider] = encoded
		}
	}
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
	if secret, ok := entries[entryKey(provider, account)]; ok {
		if cred, decErr := decodeStoredCredential(secret); decErr == nil && cred != nil {
			if cred.Type == CredentialAPIKey && cred.Key != "" {
				return cred.Key, nil
			}
			if cred.Type == CredentialOAuth && cred.Access != "" {
				return cred.Access, nil
			}
		}
		return secret, nil
	}
	if raw, ok := entries[provider]; ok && (account == "api-key" || account == "default") {
		cred, decErr := decodeStoredCredential(raw)
		if decErr == nil && cred != nil {
			if cred.Type == CredentialAPIKey && cred.Key != "" {
				return cred.Key, nil
			}
			if cred.Type == CredentialOAuth && cred.Access != "" {
				return cred.Access, nil
			}
		}
	}
	return "", os.ErrNotExist
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
	if account == "api-key" || account == "default" {
		delete(entries, provider)
	}
	return save(entries)
}

// Store implements ai.CredentialStore using kabibi/credstore.
type Store struct{}

func NewStore() *Store {
	return &Store{}
}

func (*Store) Read(ctx context.Context, providerID string) (*Credential, error) {
	return Read(ctx, providerID)
}

func (*Store) Modify(ctx context.Context, providerID string, fn func(current *Credential) (*Credential, error)) (*Credential, error) {
	return Modify(ctx, providerID, fn)
}

func (*Store) Delete(ctx context.Context, providerID string) error {
	return Remove(ctx, providerID)
}

