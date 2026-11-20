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

  function modelURLs(url) {
    const urls = [url];
    try {
      const parsed = new URL(url);
      const filename = filenameOf(url);
      if (parsed.hostname.toLowerCase() !== 'huggingface.co' ||
          (url !== '` + DefaultEmbeddingModelURL + `' && !/^gemma-4-.*\.litertlm$/i.test(filename))) {
        return urls;
      }
      const encodedFilename = encodeURIComponent(filename);
      urls.push(
        '` + modelFallbackBase + `/' + encodedFilename,
        '` + modelFallbackBase + `/gemma4/' + encodedFilename
      );
    } catch (e) {}
    return urls;
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
          for (const cacheURL of modelURLs(url)) {
            const resp = await cache.match(cacheURL);
            if (resp) {
              const metadata = responseMetadata(resp);
              return Object.assign({ cached: true, size: metadata.contentLength, source: 'cache_storage' }, metadata);
            }
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
          for (const cacheURL of modelURLs(url)) {
            const resp = await cache.match(cacheURL);
            if (resp) {
              const blob = await resp.blob();
              return {
                stream: blob.stream(),
                provenance: Object.assign({
                  requestedURL: url,
                  readFrom: 'cache_storage',
                  cacheName: CACHE_NAME,
                  cacheKey: cacheURL,
                  size: blob.size
                }, responseMetadata(resp), downloadMetadata(url))
              };
            }
          }
        } catch (e) {}
      }

      const urls = modelURLs(url);
      const failures = [];
      for (const sourceURL of urls) {
        try {
          const res = await fetch(sourceURL, { mode: 'cors', credentials: 'omit' });
          if (!res.ok) {
            throw new Error('HTTP ' + res.status + ' from ' + (res.url || sourceURL));
          }
          if (!res.body) {
            throw new Error('no response body from ' + (res.url || sourceURL));
          }
          return {
            stream: res.body,
            provenance: Object.assign({
              requestedURL: url,
              readFrom: 'network',
              size: 0
            }, responseMetadata(res))
          };
        } catch (e) {
          failures.push(String(e?.message || e));
        }
      }
      throw new Error('fetch model failed from all sources: ' + failures.join('; '));
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
          const fname = filenameOf(url);
          const root = await getOPFSDirectory();
          const urls = modelURLs(url);
          const failures = [];
          for (const sourceURL of urls) {
            let writable = null;
            try {
              const resp = await fetch(sourceURL, {
                mode: 'cors',
                credentials: 'omit',
                signal: ctrl.signal
              });
              if (!resp.ok) {
                throw new Error('HTTP ' + resp.status + ' from ' + (resp.url || sourceURL));
              }

              state.received = 0;
              state.total = 0;
              state.percent = 0;
              state.finalURL = '';
              state.httpStatus = 0;
              state.contentType = '';
              state.storedIn = '';
              const metadata = responseMetadata(resp);
              state.finalURL = metadata.finalURL;
              state.httpStatus = metadata.httpStatus;
              state.contentType = metadata.contentType;

              const cl = resp.headers.get('content-length');
              if (cl) {
                state.total = parseInt(cl, 10) || 0;
              }

              if (root && typeof root.getFileHandle === 'function') {
                state.storedIn = 'opfs';
                const fh = await root.getFileHandle(fname, { create: true });
                writable = await fh.createWritable();
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
                await cache.put(sourceURL, clone);
              } else {
                state.storedIn = 'memory_only';
                const buf = await resp.arrayBuffer();
                state.received = buf.byteLength;
              }

              state.done = true;
              state.percent = 100;
              return;
            } catch (e) {
              failures.push(String(e?.message || e));
              if (writable) {
                try { await writable.abort(); } catch (abortError) {}
              }
              if (root && state.storedIn === 'opfs') {
                try { await root.removeEntry(fname); } catch (removeError) {}
              }
              state.received = 0;
              state.total = 0;
              state.percent = 0;
              state.finalURL = '';
              state.httpStatus = 0;
              state.contentType = '';
              state.storedIn = '';
              if (ctrl.signal.aborted) break;
            }
          }
          throw new Error('download failed from all sources: ' + failures.join('; '));
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

	return ensureModelBrowserWithEval(ctx, url, opts, EvalJS)
}

func ensureModelBrowserWithEval(ctx context.Context, url string, opts FetchOptions, eval func(string) (string, error)) (string, error) {
	filename := filepath.Base(url)

	cache, err := checkBrowserModelCache(eval, url)
	if err != nil {
		return "", err
	}
	if cache.Cached {
		if cache.Size > 0 {
			recordBrowserModelSize(url, cache.Size)
		}
		return url, nil
	}

	startCode := fmt.Sprintf("globalThis.__rustica_assets.startDownload(%s)", strconv.Quote(url))
	initProgressJSON, err := eval(startCode)
	if err != nil {
		return "", fmt.Errorf("start browser model download: %w", err)
	}

	var prog browserDownloadProgress
	if err := json.Unmarshal([]byte(initProgressJSON), &prog); err == nil && prog.Done {
		if prog.Error != "" {
			return "", fmt.Errorf("download model in browser: %s", prog.Error)
		}
		if prog.Total > 0 {
			recordBrowserModelSize(url, prog.Total)
		} else if prog.Received > 0 {
			recordBrowserModelSize(url, prog.Received)
		}
		if !prog.Cached {
			opts.progress("download", filename, 100)
		}
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
			_, _ = eval(cancelCode)
			return "", ctx.Err()
		case <-ticker.C:
			pJSON, err := eval(pollCode)
			if err != nil {
				return "", fmt.Errorf("poll browser download progress: %w", err)
			}
			var p browserDownloadProgress
			if err := json.Unmarshal([]byte(pJSON), &p); err != nil {
				continue
			}
			if !p.Cached && p.Percent != lastPct {
				lastPct = p.Percent
				opts.progress("download", filename, p.Percent)
			}
			if p.Done {
				if p.Error != "" {
					return "", errors.New(p.Error)
				}
				if p.Total > 0 {
					recordBrowserModelSize(url, p.Total)
				} else if p.Received > 0 {
					recordBrowserModelSize(url, p.Received)
				}
				if !p.Cached && lastPct != 100 {
					opts.progress("download", filename, 100)
				}
				return url, nil
			}
		}
	}
}

func browserModelDownloadSize(url string, expectedSize int64) (int64, error) {
	if browserModelSize(url) > 0 {
		return 0, nil
	}
	if err := ensureBrowserAssetsBootstrap(); err != nil {
		return 0, fmt.Errorf("bootstrap browser assets adapter: %w", err)
	}
	return estimateBrowserModelDownload(EvalJS, url, expectedSize)
}

func estimateBrowserModelDownload(eval func(string) (string, error), url string, expectedSize int64) (int64, error) {
	cache, err := checkBrowserModelCache(eval, url)
	if err != nil {
		return 0, err
	}
	if cache.Cached {
		if cache.Size > 0 {
			recordBrowserModelSize(url, cache.Size)
		}
		return 0, nil
	}
	return expectedSize, nil
}

type browserModelCacheStatus struct {
	Cached bool  `json:"cached"`
	Size   int64 `json:"size"`
}

func checkBrowserModelCache(eval func(string) (string, error), url string) (browserModelCacheStatus, error) {
	raw, err := eval(fmt.Sprintf("(async () => JSON.stringify(await globalThis.__rustica_assets.checkCached(%s)))()", strconv.Quote(url)))
	if err != nil {
		return browserModelCacheStatus{}, fmt.Errorf("check browser model cache: %w", err)
	}
	var status browserModelCacheStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return browserModelCacheStatus{}, fmt.Errorf("decode browser model cache status: %w", err)
	}
	return status, nil
}

var (
	browserModelSizesMu sync.Mutex
	browserModelSizes   = make(map[string]int64)
)

func recordBrowserModelSize(url string, size int64) {
	if url == "" || size <= 0 {
		return
	}
	browserModelSizesMu.Lock()
	browserModelSizes[url] = size
	browserModelSizesMu.Unlock()
}

func browserModelSize(url string) int64 {
	browserModelSizesMu.Lock()
	defer browserModelSizesMu.Unlock()
	return browserModelSizes[url]
}
