package shell

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	pathCacheDebounce = 10 * time.Second
	pathCacheMaxAge   = time.Minute
)

type pathCacheKey struct {
	cwd         string
	path        string
	pathExt     string
	windowsLike bool
}

type pathCacheSnapshot struct {
	key       pathCacheKey
	commands  map[string]struct{}
	scripts   map[string]struct{}
	refreshed time.Time
}

type pathCommandCache struct {
	mu               sync.Mutex
	snapshot         *pathCacheSnapshot
	pendingKey       pathCacheKey
	pending          bool
	pendingImmediate bool
	running          bool
	closed           bool
	requests         chan pathCacheRequest
	stop             chan struct{}
}

type pathCacheRequest struct {
	key       pathCacheKey
	immediate bool
}

func newPathCommandCache() *pathCommandCache {
	cache := &pathCommandCache{
		requests: make(chan pathCacheRequest, 1),
		stop:     make(chan struct{}),
	}
	go cache.run()
	return cache
}

func (c *pathCommandCache) close() {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.stop)
	}
	c.mu.Unlock()
}

func (c *pathCommandCache) schedule(key pathCacheKey, immediate bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if c.pending && c.pendingKey == key && c.pendingImmediate {
		c.mu.Unlock()
		return
	}
	c.pending = true
	c.pendingKey = key
	c.pendingImmediate = immediate

	request := pathCacheRequest{key: key, immediate: immediate}
	select {
	case c.requests <- request:
	default:
		select {
		case <-c.requests:
		default:
		}
		select {
		case c.requests <- request:
		case <-c.stop:
		}
	}
	if !c.running {
		c.running = true
		go c.run()
	}
	c.mu.Unlock()
}

func (c *pathCommandCache) contains(key pathCacheKey, name string) bool {
	now := time.Now()
	c.mu.Lock()
	snapshot := c.snapshot
	valid := snapshot != nil && snapshot.key == key && now.Sub(snapshot.refreshed) < pathCacheMaxAge
	c.mu.Unlock()

	if !valid {
		c.schedule(key, false)
		return false
	}
	if _, ok := snapshot.commands[cacheCommandName(name, key.windowsLike)]; ok {
		return true
	}
	_, ok := snapshot.scripts[cacheCommandName(name, key.windowsLike)]
	return ok
}

func (c *pathCommandCache) run() {
	for {
		select {
		case <-c.stop:
			return
		case request := <-c.requests:
			if request.immediate {
				c.refresh(request.key)
			} else {
				c.refreshAfterQuiet(request)
			}
			c.mu.Lock()
			more := len(c.requests) > 0
			if !more {
				c.running = false
			}
			c.mu.Unlock()
			if !more {
				return
			}
		}
	}
}

func (c *pathCommandCache) refreshAfterQuiet(first pathCacheRequest) {
	started := time.Now()
	quiet := time.NewTimer(pathCacheDebounce)
	defer quiet.Stop()

	maxWait := pathCacheMaxAge
	c.mu.Lock()
	if c.snapshot != nil && c.snapshot.key == first.key {
		maxWait -= time.Since(c.snapshot.refreshed)
	}
	c.mu.Unlock()
	if maxWait < 0 {
		maxWait = 0
	}
	maxAge := time.NewTimer(maxWait)
	defer maxAge.Stop()
	key := first.key

	for {
		select {
		case <-c.stop:
			return
		case request := <-c.requests:
			key = request.key
			if request.immediate {
				c.refresh(key)
				return
			}
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(pathCacheDebounce)
		case <-quiet.C:
			c.refresh(key)
			return
		case <-maxAge.C:
			c.refresh(key)
			return
		}

		if time.Since(started) >= pathCacheMaxAge {
			c.refresh(key)
			return
		}
	}
}

func (c *pathCommandCache) refresh(key pathCacheKey) {
	snapshot := buildPathCacheSnapshot(key)
	c.mu.Lock()
	if !c.closed {
		c.snapshot = snapshot
		if c.pendingKey == key {
			c.pending = false
			c.pendingImmediate = false
		}
	}
	c.mu.Unlock()
}

func buildPathCacheSnapshot(key pathCacheKey) *pathCacheSnapshot {
	snapshot := &pathCacheSnapshot{
		key:       key,
		commands:  make(map[string]struct{}),
		scripts:   make(map[string]struct{}),
		refreshed: time.Now(),
	}
	pathDirs := filepath.SplitList(key.path)
	if len(pathDirs) == 0 {
		pathDirs = []string{""}
	}
	for _, dir := range pathDirs {
		if dir == "" || dir == "." {
			dir = key.cwd
		} else if !filepath.IsAbs(dir) {
			dir = filepath.Join(key.cwd, dir)
		}
		indexCommandDirectory(snapshot.commands, dir, key)
	}
	indexScripts(snapshot.scripts, key.cwd, key)
	return snapshot
}

func indexCommandDirectory(commands map[string]struct{}, dir string, key pathCacheKey) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	extensions := pathCacheExtensions(key.pathExt, key.windowsLike)
	for _, entry := range entries {
		name := entry.Name()
		fullPath := filepath.Join(dir, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			info, err = os.Stat(fullPath)
			if err != nil {
				continue
			}
		}
		if info.IsDir() {
			continue
		}

		if key.windowsLike {
			indexWindowsCommand(commands, name, extensions)
		} else if info.Mode()&0o111 != 0 {
			commands[cacheCommandName(name, false)] = struct{}{}
		}
	}
}

func indexWindowsCommand(commands map[string]struct{}, name string, extensions map[string]struct{}) {
	lowerName := strings.ToLower(name)
	commands[lowerName] = struct{}{}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return
	}
	if _, ok := extensions[ext]; ok {
		commands[strings.TrimSuffix(lowerName, ext)] = struct{}{}
	}
}

func indexScripts(scripts map[string]struct{}, dir string, key pathCacheKey) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(name), ".sh") {
			scripts[cacheCommandName(name, key.windowsLike)] = struct{}{}
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		var marker [2]byte
		_, readErr := file.Read(marker[:])
		_ = file.Close()
		if readErr == nil && string(marker[:]) == "#!" {
			scripts[cacheCommandName(name, key.windowsLike)] = struct{}{}
		}
	}
}

func pathCacheExtensions(pathExt string, windowsLike bool) map[string]struct{} {
	extensions := make(map[string]struct{})
	if !windowsLike {
		return extensions
	}
	if pathExt == "" {
		pathExt = ".COM;.EXE;.BAT;.CMD"
	}
	for _, ext := range strings.Split(pathExt, ";") {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		extensions[strings.ToLower(ext)] = struct{}{}
	}
	return extensions
}

func cacheCommandName(name string, windowsLike bool) string {
	if windowsLike {
		return strings.ToLower(name)
	}
	return name
}
