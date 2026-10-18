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

  function responseMetadata(resp) {
    return {
      finalURL: resp.url || '',
      httpStatus: resp.status || 0,
      contentType: resp.headers.get('content-type') || '',
      contentLength: parseInt(resp.headers.get('content-length') || '0', 10) || 0
    };
  }

  function downloadMetadata(url) {
    const rec = downloads.get(url);
    if (!rec || !rec.state.done || rec.state.error) return {};
    const state = rec.state;
    return {
      download: {
        requestedURL: url,
        finalURL: state.finalURL || '',
        httpStatus: state.httpStatus || 0,
        contentType: state.contentType || '',
        contentLength: state.total || 0,
        bytesReceived: state.received || 0,
        storedIn: state.storedIn || '',
        cachedBeforeDownload: !!state.cached
      }
    };
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
            return { cached: true, size: f.size, source: 'opfs', filename: fname };
          }
        } catch (e) {}
      }

      // 2. Try CacheStorage
      if (typeof caches !== 'undefined') {
        try {
          const cache = await caches.open(CACHE_NAME);
          const resp = await cache.match(url);
          if (resp) {
            const metadata = responseMetadata(resp);
            return Object.assign({ cached: true, size: metadata.contentLength, source: 'cache_storage' }, metadata);
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
            return {
              stream: f.stream(),
              provenance: Object.assign({
                requestedURL: url,
                readFrom: 'opfs',
                cacheName: 'OPFS',
                filename: fname,
                size: f.size
              }, downloadMetadata(url))
            };
          }
        } catch (e) {}
      }

      if (typeof caches !== 'undefined') {
        try {
          const cache = await caches.open(CACHE_NAME);
          const resp = await cache.match(url);
          if (resp) {
            const blob = await resp.blob();
            return {
              stream: blob.stream(),
              provenance: Object.assign({
                requestedURL: url,
                readFrom: 'cache_storage',
                cacheName: CACHE_NAME,
                cacheKey: url,
                size: blob.size
              }, responseMetadata(resp), downloadMetadata(url))
            };
          }
        } catch (e) {}
      }

      const res = await fetch(url, { mode: 'cors', credentials: 'omit' });
      if (!res.ok) {
        throw new Error('fetch model failed: HTTP ' + res.status + ' from ' + (res.url || url));
      }
      if (!res.body) {
        throw new Error('fetch model returned no response body from ' + (res.url || url));
      }
      return {
        stream: res.body,
        provenance: Object.assign({
          requestedURL: url,
          readFrom: 'network',
          size: 0
        }, responseMetadata(res))
      };
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
        finalURL: '',
        httpStatus: 0,
        contentType: '',
        storedIn: '',
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
            state.finalURL = check.finalURL || '';
            state.httpStatus = check.httpStatus || 0;
            state.contentType = check.contentType || '';
            state.storedIn = check.source || '';
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
            throw new Error('download failed HTTP ' + resp.status + ' from ' + (resp.url || url));
          }

          const metadata = responseMetadata(resp);
          state.finalURL = metadata.finalURL;
          state.httpStatus = metadata.httpStatus;
          state.contentType = metadata.contentType;

          const cl = resp.headers.get('content-length');
          if (cl) {
            state.total = parseInt(cl, 10) || 0;
          }

          if (root && typeof root.getFileHandle === 'function') {
            // Stream to OPFS
            state.storedIn = 'opfs';
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
            state.storedIn = 'cache_storage';
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
            state.storedIn = 'memory_only';
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
