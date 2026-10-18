package assets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

var ensureAssetsBootstrapOnce sync.Once

const browserAssetBootstrapJS = `
(() => {
  if (globalThis.__rustica_assets) return;

  const CACHE_NAME = 'kabibi-litert-models-v1';
  const downloads = new Map();

  async function getOPFSDirectory() {
    if (typeof navigator !== 'undefined' && navigator.storage && typeof navigator.storage.getDirectory === 'function') {
      try {
        return await navigator.storage.getDirectory();
      } catch (e) {
        return null;
      }
    }
    return null;
  }

  function filenameOf(url) {
    try {
      const u = new URL(url);
      const parts = u.pathname.split('/');
      return parts[parts.length - 1] || 'model.litertlm';
    } catch (e) {
      const parts = String(url).split('/');
      return parts[parts.length - 1] || 'model.litertlm';
    }
  }

  globalThis.__rustica_assets = {
    async checkCached(url) {
      const fname = filenameOf(url);
      // 1. Try OPFS
      const root = await getOPFSDirectory();
      if (root) {
        try {
          const fh = await root.getFileHandle(fname);
          const f = await fh.getFile();
          if (f && f.size > 0) {
            return { cached: true, size: f.size, source: 'opfs' };
          }
        } catch (e) {}
      }

      // 2. Try CacheStorage
      if (typeof caches !== 'undefined') {
        try {
          const cache = await caches.open(CACHE_NAME);
          const resp = await cache.match(url);
          if (resp) {
            const cl = resp.headers.get('content-length');
            const size = cl ? parseInt(cl, 10) : 0;
            return { cached: true, size, source: 'cache_storage' };
          }
        } catch (e) {}
      }

      return { cached: false, size: 0 };
    },

    async openModelStream(url) {
      const fname = filenameOf(url);
      const root = await getOPFSDirectory();
      if (root) {
        try {
          const fh = await root.getFileHandle(fname);
          const f = await fh.getFile();
          if (f && f.size > 0) {
            return f.stream();
          }
        } catch (e) {}
      }

      if (typeof caches !== 'undefined') {
        try {
          const cache = await caches.open(CACHE_NAME);
          const resp = await cache.match(url);
          if (resp) {
            const blob = await resp.blob();
            return blob.stream();
          }
        } catch (e) {}
      }

      const res = await fetch(url, { mode: 'cors', credentials: 'omit' });
      if (!res.ok) throw new Error('fetch model failed: HTTP ' + res.status);
      return res.body;
    },

    startDownload(url) {
      if (downloads.has(url)) {
        return JSON.stringify(downloads.get(url).state);
      }

      const state = {
        done: false,
        cached: false,
        received: 0,
        total: 0,
        percent: 0,
        error: null
      };

      const ctrl = new AbortController();
      const rec = { state, ctrl };
      downloads.set(url, rec);

      (async () => {
        try {
          const check = await this.checkCached(url);
          if (check.cached) {
            state.done = true;
            state.cached = true;
            state.received = check.size;
            state.total = check.size;
            state.percent = 100;
            return;
          }

          const fname = filenameOf(url);
          const root = await getOPFSDirectory();
          const resp = await fetch(url, {
            mode: 'cors',
            credentials: 'omit',
            signal: ctrl.signal
          });
          if (!resp.ok) {
            throw new Error('download failed HTTP ' + resp.status);
          }

          const cl = resp.headers.get('content-length');
          if (cl) {
            state.total = parseInt(cl, 10) || 0;
          }

          if (root && typeof root.getFileHandle === 'function') {
            // Stream to OPFS
            const fh = await root.getFileHandle(fname, { create: true });
            const writable = await fh.createWritable();
            const reader = resp.body.getReader();
            while (true) {
              const { done, value } = await reader.read();
              if (done) break;
              if (value) {
                await writable.write(value);
                state.received += value.byteLength;
                if (state.total > 0) {
                  state.percent = Math.floor((state.received / state.total) * 100);
                }
              }
            }
            await writable.close();
          } else if (typeof caches !== 'undefined') {
            // Stream clone to CacheStorage
            const cache = await caches.open(CACHE_NAME);
            const clone = resp.clone();
            const reader = resp.body.getReader();
            while (true) {
              const { done, value } = await reader.read();
              if (done) break;
              if (value) {
                state.received += value.byteLength;
                if (state.total > 0) {
                  state.percent = Math.floor((state.received / state.total) * 100);
                }
              }
            }
            await cache.put(url, clone);
          } else {
            // Memory fallback
            const buf = await resp.arrayBuffer();
            state.received = buf.byteLength;
          }

          state.done = true;
          state.percent = 100;
        } catch (e) {
          state.error = e?.message || String(e);
          state.done = true;
        }
      })();

      return JSON.stringify(state);
    },

    getProgress(url) {
      const rec = downloads.get(url);
      if (!rec) {
        return JSON.stringify({ done: false, error: 'not_found' });
      }
      return JSON.stringify(rec.state);
    },

    cancelDownload(url) {
      const rec = downloads.get(url);
      if (rec) {
        try { rec.ctrl.abort(); } catch (e) {}
        downloads.delete(url);
      }
      return 'ok';
    }
  };
})();
`

type browserDownloadProgress struct {
	Done     bool   `json:"done"`
	Cached   bool   `json:"cached"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
	Percent  int    `json:"percent"`
	Error    string `json:"error"`
}

func ensureBrowserAssetsBootstrap() error {
	var err error
	ensureAssetsBootstrapOnce.Do(func() {
		_, err = EvalJS(browserAssetBootstrapJS)
	})
	return err
}

func ensureModelBrowser(ctx context.Context, url string, expectedSize int64, opts FetchOptions) (string, error) {
	if err := ensureBrowserAssetsBootstrap(); err != nil {
		return "", fmt.Errorf("bootstrap browser assets adapter: %w", err)
	}

	filename := filepath.Base(url)
	opts.progress("download", filename, 0)

	startCode := fmt.Sprintf("globalThis.__rustica_assets.startDownload(%s)", strconv.Quote(url))
	initProgressJSON, err := EvalJS(startCode)
	if err != nil {
		return "", fmt.Errorf("start browser model download: %w", err)
	}

	var prog browserDownloadProgress
	if err := json.Unmarshal([]byte(initProgressJSON), &prog); err == nil && prog.Done {
		if prog.Error != "" {
			return "", fmt.Errorf("download model in browser: %s", prog.Error)
		}
		opts.progress("download", filename, 100)
		return url, nil
	}

	pollCode := fmt.Sprintf("globalThis.__rustica_assets.getProgress(%s)", strconv.Quote(url))
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	lastPct := -1
	for {
		select {
		case <-ctx.Done():
			cancelCode := fmt.Sprintf("globalThis.__rustica_assets.cancelDownload(%s)", strconv.Quote(url))
			_, _ = EvalJS(cancelCode)
			return "", ctx.Err()
		case <-ticker.C:
			pJSON, err := EvalJS(pollCode)
			if err != nil {
				return "", fmt.Errorf("poll browser download progress: %w", err)
			}
			var p browserDownloadProgress
			if err := json.Unmarshal([]byte(pJSON), &p); err != nil {
				continue
			}
			if p.Percent != lastPct {
				lastPct = p.Percent
				opts.progress("download", filename, p.Percent)
			}
			if p.Done {
				if p.Error != "" {
					return "", errors.New(p.Error)
				}
				opts.progress("download", filename, 100)
				return url, nil
			}
		}
	}
}
