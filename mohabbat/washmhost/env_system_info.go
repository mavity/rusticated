package main

import (
	"context"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mavity/rusticated/platform"
	"github.com/tetratelabs/wazero/api"
)

const systemInfoPollInterval = 250 * time.Millisecond

const (
	platformInfoSize                       = 1264
	platformInfoSystemContextVersionOffset = 936
	platformInfoLocaleOffset               = 940
	platformInfoTimeZoneOffset             = 1004
	platformInfoKeyboardLayoutIDOffset     = 1068
	platformInfoAppearanceOffset           = 1196
	platformInfoPowerSourceOffset          = 1228
)

type systemContext struct {
	locale           string
	timeZone         string
	keyboardLayoutID string
	appearance       string
	powerSource      string
}

func currentHostSystemContext() systemContext {
	locale := firstNonEmpty(
		platform.SystemLocale(),
		os.Getenv("LC_ALL"),
		os.Getenv("LC_MESSAGES"),
		os.Getenv("LANG"),
	)
	timeZone := firstNonEmpty(platform.SystemTimeZone(), os.Getenv("TZ"))
	if timeZone == "" && time.Local != nil {
		timeZone = time.Local.String()
	}
	return systemContext{
		locale:           locale,
		timeZone:         timeZone,
		keyboardLayoutID: platform.KeyboardLayoutID(),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (h *HostEnv) currentSystemContext() systemContext {
	h.mu.Lock()
	provider := h.systemInfoProvider
	h.mu.Unlock()
	if provider != nil {
		return provider()
	}
	return currentHostSystemContext()
}

func (h *HostEnv) sys_system_info_wait(_ context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	current := h.currentSystemContext()

	h.mu.Lock()
	if h.mod == nil {
		h.mod = m
	}
	delete(h.systemInfoWaiters, ovPtr)
	if len(h.systemInfoWaiters) == 0 {
		h.systemInfoSnapshot = current
	}
	state := h.registerOpLocked(ovPtr, nil)
	h.systemInfoWaiters[ovPtr] = state
	h.armSystemInfoPollLocked()
	h.mu.Unlock()
}

func (h *HostEnv) armSystemInfoPollLocked() {
	if h.systemInfoTimer == nil && len(h.systemInfoWaiters) > 0 {
		h.systemInfoTimerGeneration++
		generation := h.systemInfoTimerGeneration
		h.systemInfoTimer = time.AfterFunc(systemInfoPollInterval, func() {
			h.pollSystemInfo(generation)
		})
	}
}

func (h *HostEnv) pollSystemInfo(generation uint64) {
	current := h.currentSystemContext()

	h.mu.Lock()
	if generation != h.systemInfoTimerGeneration {
		h.mu.Unlock()
		return
	}
	h.systemInfoTimer = nil
	if len(h.systemInfoWaiters) == 0 {
		h.mu.Unlock()
		return
	}
	if current == h.systemInfoSnapshot {
		h.armSystemInfoPollLocked()
		h.mu.Unlock()
		return
	}

	h.systemInfoSnapshot = current
	states := make([]*OpState, 0, len(h.systemInfoWaiters))
	for ovPtr, state := range h.systemInfoWaiters {
		delete(h.systemInfoWaiters, ovPtr)
		states = append(states, state)
	}
	h.mu.Unlock()

	for _, state := range states {
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(state.ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, state.ovPtr)
			h.mu.Unlock()
			if err := writeOverlapped(h.mod, state.ovPtr, wasiSuccess, 0, 0); err != nil {
				debugLog("system_info_wait completion failed: %v", err)
			}
		}
	}
}

func copySystemInfoString(buf []byte, offset, size int, value string) {
	if size <= 1 || offset < 0 || offset+size > len(buf) {
		return
	}
	value = strings.ToValidUTF8(value, "")
	data := []byte(value)
	end := size - 1
	if len(data) > end {
		data = data[:end]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	copy(buf[offset:offset+size], data)
}
