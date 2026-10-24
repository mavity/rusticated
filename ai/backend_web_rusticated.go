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
      this.coreSource = '';
      this.engines = new Map();
      this.conversations = new Map();
      this.streams = new Map();
      this.nextEngineId = 1;
      this.nextStreamId = 1;
    }

    async loadCore() {
      if (globalThis.LiteRtLmCore) {
        this.coreSource = 'preloaded globalThis.LiteRtLmCore (package/version unknown)';
        return globalThis.LiteRtLmCore;
      }
      if (!this.corePromise) {
        const litertLMURL = 'https://cdn.jsdelivr.net/npm/@litert-lm/core@0.18.0/+esm'
        this.coreSource = litertLMURL;
        this.corePromise = (async () => {
          try {
            return await import(litertLMURL);
          } catch (e) {
            console.error('[rustica_ai] Failed to load @litert-lm/core from CDN:', e);
            throw e;
          }
        })();
      }
      return await this.corePromise;
    }

    initError(error, context) {
      const detail = error?.stack || error?.message || String(error);
      return new Error(
        detail + '\n\nLiteRT-LM initialization context:\n' +
        JSON.stringify(context, null, 2)
      );
    }

    async createEngine(cfgJson) {
      const cfg = JSON.parse(cfgJson);
      const context = {
        model: {
          requestedURL: cfg.modelUrl,
          readFrom: 'model URL directly'
        },
        runtime: {
          package: '@litert-lm/core',
          source: 'not resolved'
        },
        backend: cfg.backend,
        maxTokens: cfg.maxTokens,
        page: globalThis.location ? globalThis.location.origin + globalThis.location.pathname : 'unknown',
        userAgent: globalThis.navigator ? globalThis.navigator.userAgent : 'unknown'
      };

      let mod;
      try {
        mod = await this.loadCore();
        context.runtime.source = this.coreSource || 'unknown';
      } catch (e) {
        context.runtime.source = this.coreSource || 'load failed before source was recorded';
        throw this.initError(e, context);
      }

      let modelSource = cfg.modelUrl;
      if (globalThis.__rustica_assets && typeof globalThis.__rustica_assets.openModelStream === 'function') {
        try {
          const opened = await globalThis.__rustica_assets.openModelStream(cfg.modelUrl);
          if (opened && opened.stream) {
            modelSource = opened.stream;
            context.model = opened.provenance || context.model;
          } else {
            modelSource = opened;
            context.model.readFrom = 'asset adapter (provenance unavailable)';
          }
        } catch (e) {
          context.model.streamOpenError = e?.message || String(e);
          console.warn('[rustica_ai] openModelStream fallback to url:', e);
        }
      }

      let backendEnum;
      if (mod.Backend) {
        backendEnum = mod.Backend.GPU_ARTISAN ?? mod.Backend.GPU ?? 2;
        if (cfg.backend === 'cpu') {
          console.warn('[rustica_ai] CPU backend requested, but web models require GPU_ARTISAN; falling back to GPU_ARTISAN');
          backendEnum = mod.Backend.GPU_ARTISAN ?? mod.Backend.GPU ?? 2;
        } else if (cfg.backend === 'gpu' || cfg.backend === 'gpu_artisan') {
          backendEnum = mod.Backend.GPU_ARTISAN ?? mod.Backend.GPU ?? 2;
        }
      } else {
        backendEnum = 2;
      }
      context.resolvedBackend = String(backendEnum);

      const mainExecutorSettings = {
        maxNumTokens: (cfg.maxTokens && cfg.maxTokens > 0) ? cfg.maxTokens : 4096,
        backendConfig: {
          num_output_candidates: 1,
          wait_for_weight_uploads: true,
          num_decode_steps_per_sync: 1,
          sequence_batch_size: 0,
          supported_lora_ranks: [],
          max_top_k: 64,
          enable_decode_logits: false,
          enable_external_embeddings: false,
          use_submodel: true,
          use_autosized_ringbuffers: true,
        }
      };

      const engineSettings = {
        model: modelSource,
        backend: backendEnum,
        mainExecutorSettings: mainExecutorSettings,
      };

      let engine;
      try {
        engine = await mod.Engine.create(engineSettings);
      } catch (e) {
        throw this.initError(e, context);
      }
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
        conv: entry.conv,
        reader: null
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

          if (typeof conv.sendMessageStreaming === 'function') {
            const responseStream = conv.sendMessageStreaming(msgObj);
            const reader = responseStream.getReader();
            stream.reader = reader;

            while (true) {
              const { done, value } = await reader.read();
              if (done) break;

              let chunk = value;
              if (typeof chunk === 'string') {
                try {
                  chunk = JSON.parse(chunk);
                } catch {
                  chunk = { content: chunk };
                }
              }

              let contentParts = [];

              // Handle thought channel (e.g. Gemma 4 thinking tokens)
              if (chunk && chunk.channels && typeof chunk.channels.thought === 'string' && chunk.channels.thought) {
                contentParts.push({
                  type: 'text',
                  text: chunk.channels.thought
                });
              }

              // Handle content
              if (chunk && chunk.content) {
                if (typeof chunk.content === 'string') {
                  contentParts.push({
                    type: 'text',
                    text: chunk.content
                  });
                } else if (Array.isArray(chunk.content)) {
                  for (const part of chunk.content) {
                    if (typeof part === 'string') {
                      contentParts.push({ type: 'text', text: part });
                    } else if (part && typeof part === 'object') {
                      if (part.type === 'text' && typeof part.text === 'string') {
                        contentParts.push(part);
                      } else if (typeof part.text === 'string') {
                        contentParts.push({ type: 'text', text: part.text });
                      }
                    }
                  }
                }
              }

              const normalizedChunk = {
                role: chunk?.role || 'model',
                content: contentParts,
              };

              const toolCalls = chunk?.tool_calls || chunk?.toolCalls;
              if (Array.isArray(toolCalls) && toolCalls.length > 0) {
                normalizedChunk.tool_calls = toolCalls;
              }

              pushEvent({ type: 'chunk', data: JSON.stringify(normalizedChunk) });
            }
          } else {
            throw new Error('no compatible sendMessage streaming method found (conv.sendMessageStreaming missing)');
          }

          if (!stream.done) {
            stream.done = true;
            pushEvent({ type: 'done' });
          }
        } catch (e) {
          if (!stream.done) {
            stream.error = e?.message || String(e);
            stream.done = true;
            if (stream.waiters.length > 0) {
              const resolve = stream.waiters.shift();
              resolve(JSON.stringify({ type: 'error', error: stream.error }));
            } else {
              stream.queue.push({ type: 'error', error: stream.error });
            }
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
        if (stream.reader && typeof stream.reader.cancel === 'function') {
          stream.reader.cancel().catch(() => {});
        }
      } catch (e) {}

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

func (e *webEngineBackend) NewConversation(cfg ConversationConfig) (conversationBackend, error) {
	systemJSON, toolsJSON, historyJSON, err := buildLiteRTConversationConfig(cfg)
	if err != nil {
		return nil, err
	}
	convID := atomic.AddUint64(&nextWebConvID, 1)
	request := map[string]any{
		"engineId":    e.engineID,
		"convId":      convID,
		"systemJSON":  systemJSON,
		"toolsJSON":   toolsJSON,
		"historyJSON": historyJSON,
	}
	cfgBytes, _ := json.Marshal(request)
	_, err = EvalJS(fmt.Sprintf("globalThis.__rustica_ai.createConversation(%s)", strconv.Quote(string(cfgBytes))))
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

func (c *webConversationBackend) SendStream(ctx context.Context, parts []PromptPart, onEvent func(ModelEvent)) error {
	if c.closed.Load() {
		return ErrClosed
	}

	messageJSON, err := buildMessageJSON(parts)
	if err != nil {
		return err
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
				for _, event := range parseChunkEvents(ev.Data) {
					onEvent(event)
				}
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
