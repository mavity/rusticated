//go:build wasip1

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

var (
	webAdapterOnce sync.Once
	webAdapterErr  error
	nextWebConvID  uint64
)

func isBrowserHost() bool {
	pi := syscall.GetPlatformInfo()
	if pi != nil && strings.Contains(strings.ToLower(pi.OSName), "browser") {
		return true
	}
	return false
}

const webAdapterBootstrapJS = `
(() => {
  if (globalThis.__rustica_ai) return;

  class RusticaAI {
    constructor() {
      this.corePromise = null;
      this.engines = new Map();
      this.conversations = new Map();
      this.streams = new Map();
      this.nextEngineId = 1;
      this.nextStreamId = 1;
    }

    async loadCore() {
      if (globalThis.LiteRtLmCore) {
        return globalThis.LiteRtLmCore;
      }
      if (!this.corePromise) {
        this.corePromise = (async () => {
          try {
            return await import('https://cdn.jsdelivr.net/npm/@litert-lm/core@0.12.1/+esm');
          } catch (e) {
            console.error('[rustica_ai] Failed to load @litert-lm/core from CDN:', e);
            throw e;
          }
        })();
      }
      return await this.corePromise;
    }

    async createEngine(cfgJson) {
      const cfg = JSON.parse(cfgJson);
      const mod = await this.loadCore();

      let modelSource = cfg.modelUrl;
      if (globalThis.__rustica_assets && typeof globalThis.__rustica_assets.openModelStream === 'function') {
        try {
          modelSource = await globalThis.__rustica_assets.openModelStream(cfg.modelUrl);
        } catch (e) {
          console.warn('[rustica_ai] openModelStream fallback to url:', e);
        }
      }

      let backendEnum = mod.Backend ? mod.Backend.GPU : 'gpu';
      if (cfg.backend === 'cpu') {
        backendEnum = mod.Backend ? mod.Backend.CPU : 'cpu';
      }

      const engineSettings = {
        model: modelSource,
        backend: backendEnum,
      };
      if (cfg.maxTokens > 0) {
        engineSettings.mainExecutorSettings = { maxNumTokens: cfg.maxTokens };
      }

      const engine = await mod.Engine.create(engineSettings);
      const id = this.nextEngineId++;
      this.engines.set(id, engine);
      return String(id);
    }

    async createConversation(cfgJson) {
      const cfg = JSON.parse(cfgJson);
      const engine = this.engines.get(cfg.engineId);
      if (!engine) throw new Error('invalid engine id: ' + cfg.engineId);

      const preface = {
        messages: []
      };

      if (cfg.systemJSON && cfg.systemJSON.trim() !== '') {
        try {
          preface.messages.push({
            role: 'system',
            content: [JSON.parse(cfg.systemJSON)]
          });
        } catch (e) {
          preface.messages.push({
            role: 'system',
            content: [{ type: 'text', text: cfg.systemJSON }]
          });
        }
      }

      if (cfg.historyJSON && cfg.historyJSON.trim() !== '') {
        try {
          const hist = JSON.parse(cfg.historyJSON);
          if (Array.isArray(hist)) {
            preface.messages.push(...hist);
          }
        } catch (e) {
          console.warn('[rustica_ai] parse history error:', e);
        }
      }

      if (cfg.toolsJSON && cfg.toolsJSON.trim() !== '') {
        try {
          preface.tools = JSON.parse(cfg.toolsJSON);
        } catch (e) {
          console.warn('[rustica_ai] parse tools error:', e);
        }
      }

      const conv = await engine.createConversation({ preface });
      const convId = Number(cfg.convId);
      this.conversations.set(convId, { conv, engine });
      return String(convId);
    }

    async cloneConversation(convId, newConvId) {
      const entry = this.conversations.get(Number(convId));
      if (!entry) throw new Error('invalid conv id to clone: ' + convId);

      let cloned;
      if (typeof entry.conv.clone === 'function') {
        cloned = await entry.conv.clone();
      } else {
        throw new Error('conversation clone not supported');
      }

      this.conversations.set(Number(newConvId), { conv: cloned, engine: entry.engine });
      return String(newConvId);
    }

    startStream(convId, messageJSON) {
      const entry = this.conversations.get(Number(convId));
      if (!entry) throw new Error('invalid conv id: ' + convId);

      const streamId = this.nextStreamId++;
      const stream = {
        queue: [],
        waiters: [],
        done: false,
        error: null,
        conv: entry.conv
      };
      this.streams.set(streamId, stream);

      (async () => {
        try {
          const conv = entry.conv;
          const msgObj = typeof messageJSON === 'string' ? JSON.parse(messageJSON) : messageJSON;

          const pushEvent = (ev) => {
            if (stream.waiters.length > 0) {
              const resolve = stream.waiters.shift();
              resolve(JSON.stringify(ev));
            } else {
              stream.queue.push(ev);
            }
          };

          // Check if conv.conversation (WasmConversation) exists
          if (conv.conversation && typeof conv.conversation.sendMessageAsync === 'function' && conv.engine && typeof conv.engine.waitUntilDone === 'function') {
            const wasmConv = conv.conversation;
            const wasmEngine = conv.engine;
            const executor = conv.mutexes?.executor;

            const run = async () => {
              await new Promise((resolve, reject) => {
                wasmConv.sendMessageAsync(JSON.stringify(msgObj), (chunk, isDone, error) => {
                  if (error) {
                    reject(error);
                    return;
                  }
                  if (chunk) {
                    pushEvent({ type: 'chunk', data: chunk });
                  }
                  if (isDone) {
                    resolve();
                  }
                });
              });
              await wasmEngine.waitUntilDone();
            };

            if (executor && typeof executor.acquireAndRun === 'function') {
              await executor.acquireAndRun(run);
            } else {
              await run();
            }
          } else if (typeof conv.sendMessageStreaming === 'function') {
            // High-level fallback
            const streamIter = conv.sendMessageStreaming(msgObj);
            for await (const chunk of streamIter) {
              // Convert chunk to standard LiteRT JSON
              const chunkJson = JSON.stringify(chunk);
              pushEvent({ type: 'chunk', data: chunkJson });
            }
          } else {
            throw new Error('no compatible sendMessage streaming method found');
          }

          stream.done = true;
          pushEvent({ type: 'done' });
        } catch (e) {
          stream.error = e?.message || String(e);
          stream.done = true;
          if (stream.waiters.length > 0) {
            const resolve = stream.waiters.shift();
            resolve(JSON.stringify({ type: 'error', error: stream.error }));
          } else {
            stream.queue.push({ type: 'error', error: stream.error });
          }
        }
      })();

      return String(streamId);
    }

    nextChunk(streamId) {
      const stream = this.streams.get(Number(streamId));
      if (!stream) {
        return JSON.stringify({ type: 'error', error: 'stream not found' });
      }

      if (stream.queue.length > 0) {
        const ev = stream.queue.shift();
        return JSON.stringify(ev);
      }

      if (stream.done) {
        if (stream.error) {
          return JSON.stringify({ type: 'error', error: stream.error });
        }
        return JSON.stringify({ type: 'done' });
      }

      return new Promise((resolve) => {
        stream.waiters.push(resolve);
      });
    }

    cancelStream(streamId) {
      const stream = this.streams.get(Number(streamId));
      if (!stream) return 'ok';

      try {
        if (typeof stream.conv.cancel === 'function') {
          stream.conv.cancel();
        } else if (stream.conv.conversation && typeof stream.conv.conversation.cancelProcess === 'function') {
          stream.conv.conversation.cancelProcess();
        }
      } catch (e) {}

      stream.done = true;
      stream.error = 'context canceled';
      while (stream.waiters.length > 0) {
        const resolve = stream.waiters.shift();
        resolve(JSON.stringify({ type: 'error', error: 'context canceled' }));
      }
      return 'ok';
    }

    deleteConversation(convId) {
      this.conversations.delete(Number(convId));
      return 'ok';
    }

    deleteEngine(engineId) {
      const eng = this.engines.get(Number(engineId));
      if (eng && typeof eng.delete === 'function') {
        try { eng.delete(); } catch (e) {}
      }
      this.engines.delete(Number(engineId));
      return 'ok';
    }
  }

  globalThis.__rustica_ai = new RusticaAI();
})();
`

func ensureWebAdapterLoaded() error {
	webAdapterOnce.Do(func() {
		_, webAdapterErr = EvalJS(webAdapterBootstrapJS)
	})
	return webAdapterErr
}

type webEngineBackend struct {
	engineID uint64
}

func openWebBackend(libPath, modelPath, backend string, maxTokens int) (engineBackend, error) {
	if err := ensureWebAdapterLoaded(); err != nil {
		return nil, fmt.Errorf("load web adapter: %w", err)
	}

	cfg := map[string]any{
		"modelUrl":  modelPath,
		"backend":   backend,
		"maxTokens": maxTokens,
	}
	cfgBytes, _ := json.Marshal(cfg)
	res, err := EvalJS(fmt.Sprintf("globalThis.__rustica_ai.createEngine(%s)", strconv.Quote(string(cfgBytes))))
	if err != nil {
		return nil, fmt.Errorf("web createEngine: %w", err)
	}

	id, err := strconv.ParseUint(strings.TrimSpace(res), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse web engine id %q: %w", res, err)
	}

	return &webEngineBackend{engineID: id}, nil
}

func (e *webEngineBackend) NewConversation(systemJSON, toolsJSON, historyJSON string) (conversationBackend, error) {
	convID := atomic.AddUint64(&nextWebConvID, 1)
	cfg := map[string]any{
		"engineId":    e.engineID,
		"convId":      convID,
		"systemJSON":  systemJSON,
		"toolsJSON":   toolsJSON,
		"historyJSON": historyJSON,
	}
	cfgBytes, _ := json.Marshal(cfg)
	_, err := EvalJS(fmt.Sprintf("globalThis.__rustica_ai.createConversation(%s)", strconv.Quote(string(cfgBytes))))
	if err != nil {
		return nil, fmt.Errorf("web createConversation: %w", err)
	}

	return &webConversationBackend{
		engine: e,
		convID: convID,
	}, nil
}

func (e *webEngineBackend) Close() error {
	if e == nil || e.engineID == 0 {
		return nil
	}
	_, _ = EvalJS(fmt.Sprintf("globalThis.__rustica_ai.deleteEngine(%d)", e.engineID))
	e.engineID = 0
	return nil
}

type webConversationBackend struct {
	engine *webEngineBackend
	convID uint64
	closed atomic.Bool
}

func (c *webConversationBackend) Clone() (conversationBackend, error) {
	if c.closed.Load() {
		return nil, ErrClosed
	}
	newConvID := atomic.AddUint64(&nextWebConvID, 1)
	_, err := EvalJS(fmt.Sprintf("globalThis.__rustica_ai.cloneConversation(%d, %d)", c.convID, newConvID))
	if err != nil {
		return nil, fmt.Errorf("web cloneConversation: %w", err)
	}

	return &webConversationBackend{
		engine: c.engine,
		convID: newConvID,
	}, nil
}

type webStreamEvent struct {
	Type  string `json:"type"`
	Data  string `json:"data"`
	Error string `json:"error"`
}

func (c *webConversationBackend) SendStream(ctx context.Context, messageJSON string, onChunk func(string)) error {
	if c.closed.Load() {
		return ErrClosed
	}

	streamIDStr, err := EvalJS(fmt.Sprintf("globalThis.__rustica_ai.startStream(%d, %s)", c.convID, strconv.Quote(messageJSON)))
	if err != nil {
		return fmt.Errorf("web startStream: %w", err)
	}

	streamID, err := strconv.ParseUint(strings.TrimSpace(streamIDStr), 10, 64)
	if err != nil {
		return fmt.Errorf("parse stream id %q: %w", streamIDStr, err)
	}

	// Cancellation watcher
	doneChan := make(chan struct{})
	defer close(doneChan)

	go func() {
		select {
		case <-ctx.Done():
			_, _ = EvalJS(fmt.Sprintf("globalThis.__rustica_ai.cancelStream(%d)", streamID))
		case <-doneChan:
		}
	}()

	for {
		rawEv, err := EvalJS(fmt.Sprintf("globalThis.__rustica_ai.nextChunk(%d)", streamID))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("web nextChunk: %w", err)
		}

		var ev webStreamEvent
		if err := json.Unmarshal([]byte(rawEv), &ev); err != nil {
			return fmt.Errorf("parse stream event: %w", err)
		}

		switch ev.Type {
		case "chunk":
			if ev.Data != "" {
				onChunk(ev.Data)
			}
		case "done":
			return nil
		case "error":
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New(ev.Error)
		default:
			// Unexpected type
		}
	}
}

func (c *webConversationBackend) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	_, _ = EvalJS(fmt.Sprintf("globalThis.__rustica_ai.deleteConversation(%d)", c.convID))
	return nil
}
