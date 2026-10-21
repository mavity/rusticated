// ─────────────────────────────────────────────────────────────────────────────
// washmhost.js — Unified Rusticated WASM Browser Host
// Implements the rusticated WASM ABI ("env" import object) for the browser,
// backed directly by asynchronous IndexedDB storage and a cooperative,
// event-driven scheduler.
// ─────────────────────────────────────────────────────────────────────────────

export const FLAG_COMPLETED = 1;

// ── WASI (wasip1) errno mapping ───────────────────────────────────────────────
export const wasiSuccess          = 0;
export const wasiE2BIG            = 1;
export const wasiEACCES           = 2;
export const wasiEADDRINUSE       = 3;
export const wasiEADDRNOTAVAIL    = 4;
export const wasiEAFNOSUPPORT     = 5;
export const wasiEAGAIN           = 6;
export const wasiEALREADY         = 7;
export const wasiEBADF            = 8;
export const wasiEBADMSG          = 9;
export const wasiEBUSY            = 10;
export const wasiECANCELED        = 11;
export const wasiECHILD           = 12;
export const wasiECONNABORTED     = 13;
export const wasiECONNREFUSED     = 14;
export const wasiECONNRESET       = 15;
export const wasiEDEADLK          = 16;
export const wasiEDESTADDRREQ     = 17;
export const wasiEDOM             = 18;
export const wasiEDQUOT           = 19;
export const wasiEEXIST           = 20;
export const wasiEFAULT           = 21;
export const wasiEFBIG            = 22;
export const wasiEHOSTUNREACH     = 23;
export const wasiEIDRM            = 24;
export const wasiEILSEQ           = 25;
export const wasiEINPROGRESS      = 26;
export const wasiEINTR            = 27;
export const wasiEINVAL           = 28;
export const wasiEIO              = 29;
export const wasiEISCONN          = 30;
export const wasiEISDIR           = 31;
export const wasiELOOP            = 32;
export const wasiEMFILE           = 33;
export const wasiEMLINK           = 34;
export const wasiEMSGSIZE         = 35;
export const wasiEMULTIHOP        = 36;
export const wasiENAMETOOLONG     = 37;
export const wasiENETDOWN         = 38;
export const wasiENETRESET        = 39;
export const wasiENETUNREACH      = 40;
export const wasiENFILE           = 41;
export const wasiENOBUFS          = 42;
export const wasiENODEV           = 43;
export const wasiENOENT           = 44;
export const wasiENOEXEC          = 45;
export const wasiENOLCK           = 46;
export const wasiENOLINK          = 47;
export const wasiENOMEM           = 48;
export const wasiENOMSG           = 49;
export const wasiENOPROTOOPT      = 50;
export const wasiENOSPC           = 51;
export const wasiENOSYS           = 52;
export const wasiENOTCONN         = 53;
export const wasiENOTDIR          = 54;
export const wasiENOTEMPTY        = 55;
export const wasiENOTRECOVERABLE  = 56;
export const wasiENOTSOCK         = 57;
export const wasiENOTSUP          = 58;
export const wasiENOTTY           = 59;
export const wasiENXIO            = 60;
export const wasiEOVERFLOW        = 61;
export const wasiEOWNERDEAD       = 62;
export const wasiEPERM            = 63;
export const wasiEPIPE            = 64;
export const wasiEPROTO           = 65;
export const wasiEPROTONOSUPPORT  = 66;
export const wasiEPROTOTYPE       = 67;
export const wasiERANGE           = 68;
export const wasiEROFS            = 69;
export const wasiESPIPE           = 70;
export const wasiESRCH            = 71;
export const wasiESTALE           = 72;
export const wasiETIMEDOUT        = 73;
export const wasiETXTBSY          = 74;
export const wasiEXDEV            = 75;
export const wasiENOTCAPABLE      = 76;

export function mapErrno(e) {
  if (!e) return wasiSuccess;
  switch (e.code || e.name) {
    case 'ENOENT':
    case 'NotFoundError':     return wasiENOENT;
    case 'ESRCH':             return wasiESRCH;
    case 'EINTR':             return wasiEINTR;
    case 'EIO':               return wasiEIO;
    case 'ENXIO':             return wasiENXIO;
    case 'EBADF':             return wasiEBADF;
    case 'ECHILD':            return wasiECHILD;
    case 'EAGAIN':            return wasiEAGAIN;
    case 'ENOMEM':
    case 'QuotaExceededError':return wasiENOMEM;
    case 'EACCES':            return wasiEACCES;
    case 'EFAULT':            return wasiEFAULT;
    case 'EBUSY':             return wasiEBUSY;
    case 'EEXIST':            return wasiEEXIST;
    case 'EXDEV':             return wasiEXDEV;
    case 'ENODEV':            return wasiENODEV;
    case 'ENOTDIR':           return wasiENOTDIR;
    case 'EISDIR':            return wasiEISDIR;
    case 'EINVAL':            return wasiEINVAL;
    case 'EMFILE':            return wasiEMFILE;
    case 'ENOSPC':            return wasiENOSPC;
    case 'ENOSYS':            return wasiENOSYS;
    case 'ENOTSUP':           return wasiENOTSUP;
    case 'EPIPE':             return wasiEPIPE;
    case 'ERANGE':            return wasiERANGE;
    case 'ENOTEMPTY':         return wasiENOTEMPTY;
    case 'ECONNREFUSED':      return wasiECONNREFUSED;
    case 'EADDRINUSE':        return wasiEADDRINUSE;
    case 'ECANCELED':
    case 'AbortError':        return wasiECANCELED;
    default:
      return wasiEIO;
  }
}

// ── Path resolution ──────────────────────────────────────────────────────────
export function resolvePath(baseCwd, target) {
  let full;
  const t = (target || '').trim().replace(/\\/g, '/');
  if (t.startsWith('/')) {
    full = t;
  } else {
    const cwd = (baseCwd || '/').trim().replace(/\\/g, '/');
    full = cwd.endsWith('/') ? cwd + t : cwd + '/' + t;
  }
  const parts = [];
  for (const seg of full.split('/')) {
    if (!seg || seg === '.') continue;
    if (seg === '..') {
      if (parts.length > 0) parts.pop();
    } else {
      parts.push(seg);
    }
  }
  return '/' + parts.join('/');
}

// ── Virtual Eval Dylib Handles ──────────────────────────────────────────────
export const EVAL_LIB_HANDLE = 0xEEEE0001n;
export const EVAL_SYM_HANDLE = 0xEEEE0002n;

// ── Stat Marshalling (64 bytes little-endian) ────────────────────────────────
export const STAT_KIND_UNKNOWN = 0;
export const STAT_KIND_FILE    = 1;
export const STAT_KIND_DIR     = 2;
export const STAT_KIND_SYMLINK = 3;

export function marshalStat(entry) {
  const isDir = entry.type === 'dir';
  const kind = isDir ? STAT_KIND_DIR : STAT_KIND_FILE;
  const modeBase = isDir ? 0o040000 : 0o100000;
  const mode = modeBase | ((entry.mode ?? (isDir ? 0o755 : 0o644)) & 0o7777);
  const size = BigInt(entry.size ?? (entry.content ? entry.content.length : 0));
  const mtimeMs = entry.mtime || Date.now();
  const mtimeNs = BigInt(mtimeMs) * 1000000n;

  const ab = new ArrayBuffer(64);
  const view = new DataView(ab);
  view.setUint32(0, kind, true);
  view.setUint32(4, mode, true);
  view.setUint32(8, 0, true);     // uid
  view.setUint32(12, 0, true);    // gid
  view.setBigUint64(16, size, true);
  view.setBigUint64(24, mtimeNs, true); // modifiedNs
  view.setBigUint64(32, mtimeNs, true); // accessedNs
  view.setBigUint64(40, mtimeNs, true); // createdNs
  view.setBigUint64(48, 1n, true);      // nlink
  view.setBigUint64(56, 0n, true);      // inode
  return new Uint8Array(ab);
}

// ── Overlapped struct write helper ───────────────────────────────────────────
// Layout: u32 FLAG_COMPLETED | u32 errorCode | u64 continued | u64 resultExt
export function writeOverlapped(view, ovPtr, errorCode, continued, resultExt) {
  view.setUint32(ovPtr,      FLAG_COMPLETED, true);
  view.setUint32(ovPtr + 4,  errorCode,      true);
  view.setBigUint64(ovPtr + 8,  BigInt(continued), true);
  view.setBigUint64(ovPtr + 16, BigInt(resultExt), true);
}

// ── Asynchronous IndexedDB Storage ───────────────────────────────────────────
export class AsyncIndexedDBFS {
  constructor(dbName = 'RusticatedFS') {
    this.dbName = dbName;
    this.db = null;
  }

  async init() {
    if (typeof indexedDB === 'undefined') {
      // Fallback in-memory map for headless/non-browser testing environments
      this.memFallback = new Map();
      return;
    }
    this.db = await new Promise((resolve, reject) => {
      const req = indexedDB.open(this.dbName, 1);
      req.onupgradeneeded = (e) => {
        const db = e.target.result;
        if (!db.objectStoreNames.contains('files')) {
          db.createObjectStore('files', { keyPath: 'path' });
        }
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  }

  async getEntry(path) {
    if (this.memFallback) return this.memFallback.get(path) || null;
    return new Promise((resolve, reject) => {
      const tx = this.db.transaction('files', 'readonly');
      const store = tx.objectStore('files');
      const req = store.get(path);
      req.onsuccess = () => resolve(req.result || null);
      req.onerror = () => reject(req.error);
    });
  }

  async putEntry(entry) {
    if (this.memFallback) {
      this.memFallback.set(entry.path, entry);
      return;
    }
    return new Promise((resolve, reject) => {
      const tx = this.db.transaction('files', 'readwrite');
      const store = tx.objectStore('files');
      const req = store.put(entry);
      req.onsuccess = () => resolve();
      req.onerror = () => reject(req.error);
    });
  }

  async removeEntry(path) {
    if (this.memFallback) {
      return this.memFallback.delete(path);
    }
    return new Promise((resolve, reject) => {
      const tx = this.db.transaction('files', 'readwrite');
      const store = tx.objectStore('files');
      const req = store.delete(path);
      req.onsuccess = () => resolve(true);
      req.onerror = () => reject(req.error);
    });
  }

  async readdir(dirPath) {
    const normDir = dirPath.endsWith('/') ? dirPath : dirPath + '/';
    let entries = [];
    if (this.memFallback) {
      for (const [p, e] of this.memFallback) {
        if (p !== dirPath && p.startsWith(normDir)) {
          const rest = p.slice(normDir.length);
          if (!rest.includes('/')) entries.push(e);
        }
      }
      return entries;
    }
    return new Promise((resolve, reject) => {
      const tx = this.db.transaction('files', 'readonly');
      const store = tx.objectStore('files');
      const req = store.getAll();
      req.onsuccess = () => {
        const all = req.result || [];
        const res = all.filter(e => {
          if (e.path === dirPath) return false;
          if (e.path.startsWith(normDir)) {
            const rest = e.path.slice(normDir.length);
            return !rest.includes('/');
          }
          return false;
        });
        resolve(res);
      };
      req.onerror = () => reject(req.error);
    });
  }

  async seed(extraEntries = []) {
    const existing = await this.getEntry('/');
    if (existing) return; // already seeded

    const now = Date.now();
    const encoder = new TextEncoder();
    const defaultEntries = [
      { path: '/', type: 'dir', mode: 0o755, mtime: now },
      { path: '/bin', type: 'dir', mode: 0o755, mtime: now },
      { path: '/etc', type: 'dir', mode: 0o755, mtime: now },
      { path: '/etc/hosts', type: 'file', content: encoder.encode('127.0.0.1 localhost\n'), size: 20, mode: 0o644, mtime: now },
      { path: '/etc/hostname', type: 'file', content: encoder.encode('rusticated-browser\n'), size: 19, mode: 0o644, mtime: now },
      { path: '/etc/passwd', type: 'file', content: encoder.encode('root:x:0:0:root:/root:/bin/sh\nuser:x:1000:1000:user:/home/user:/bin/sh\n'), size: 73, mode: 0o644, mtime: now },
      { path: '/etc/motd', type: 'file', content: encoder.encode('Welcome to the Rusticated Browser Environment!\n'), size: 47, mode: 0o644, mtime: now },
      { path: '/home', type: 'dir', mode: 0o755, mtime: now },
      { path: '/home/user', type: 'dir', mode: 0o755, mtime: now },
      { path: '/dev', type: 'dir', mode: 0o755, mtime: now },
      { path: '/tmp', type: 'dir', mode: 0o777, mtime: now },
      { path: '/var', type: 'dir', mode: 0o755, mtime: now },
      { path: '/usr', type: 'dir', mode: 0o755, mtime: now },
      ...extraEntries
    ];

    for (const ent of defaultEntries) {
      if (!ent.size) ent.size = ent.content ? ent.content.length : 0;
      await this.putEntry(ent);
    }
  }
}

// ── HostState: Handles, Overlapped Ops, and Wakers ────────────────────────────
export class HostState {
  constructor(memory = null) {
    this.memory = memory;
    this.handles = new Map();
    this.nextHandle = 3n;

    // stdio descriptors
    this.handles.set(0n, { type: 'stdin', fd: 0 });
    this.handles.set(1n, { type: 'stdout', fd: 1 });
    this.handles.set(2n, { type: 'stderr', fd: 2 });

    this.activeOps = new Map(); // ovPtr -> opId
    this.nextOpId = 1;
    this.pendingCount = 0;

    this.signalWaiters = new Map(); // signum -> { ovPtr, opId }
    this.completionQueue = [];

    // Stdin FIFO and pending read requests
    this.stdinBuffer = [];
    this.pendingStdinReads = []; // { ovPtr, opId, ptr, len }

    this.cwd = '/home/user';
    this.forcedExitCode = -1;
    this.rawTtyMode = 0;

    // Async wake coordination
    this._wakeResolver = null;
  }

  get view() {
    return new DataView(this.memory.buffer);
  }

  getGuestSlice(ptr, len) {
    return new Uint8Array(this.memory.buffer, ptr, len);
  }

  allocHandle(item) {
    const h = this.nextHandle++;
    this.handles.set(h, item);
    return h;
  }

  getHandle(h) {
    return this.handles.get(typeof h === 'bigint' ? h : BigInt(h));
  }

  freeHandle(h) {
    const bh = typeof h === 'bigint' ? h : BigInt(h);
    const item = this.handles.get(bh);
    if (!item) return;
    this.handles.delete(bh);
    if (bh >= 3n && item.close) {
      try { item.close(); } catch (_) {}
    }
  }

  registerOp(ovPtr) {
    const opId = this.nextOpId++;
    this.activeOps.set(ovPtr, opId);
    this.pendingCount++;
    return opId;
  }

  isOpActive(ovPtr, opId) {
    return this.activeOps.get(ovPtr) === opId;
  }

  completeOp(ovPtr, opId, errorCode, continued, resultExt) {
    if (!this.isOpActive(ovPtr, opId)) return false;
    this.activeOps.delete(ovPtr);
    this.pendingCount--;
    writeOverlapped(this.view, ovPtr, errorCode, continued, resultExt);
    return true;
  }

  cancelOp(ovPtr) {
    if (!this.activeOps.has(ovPtr)) return;
    this.activeOps.delete(ovPtr);
    this.pendingCount--;
    for (const [signum, w] of this.signalWaiters) {
      if (w.ovPtr === ovPtr) { this.signalWaiters.delete(signum); break; }
    }
    // Also remove from pending stdin reads if present
    this.pendingStdinReads = this.pendingStdinReads.filter(r => r.ovPtr !== ovPtr);
  }

  hasPending() {
    return this.pendingCount > 0;
  }

  enqueue(fn) {
    this.completionQueue.push(fn);
    this.wakeScheduler();
  }

  drainQueue() {
    while (this.completionQueue.length > 0) {
      const fn = this.completionQueue.shift();
      fn();
    }
  }

  wakeScheduler() {
    if (this._wakeResolver) {
      const resolve = this._wakeResolver;
      this._wakeResolver = null;
      resolve();
    }
  }

  waitForNotification() {
    if (this.completionQueue.length > 0) return Promise.resolve();
    return new Promise((resolve) => {
      this._wakeResolver = resolve;
    });
  }

  writeOverlapped(ovPtr, errorCode, continued, resultExt) {
    writeOverlapped(this.view, ovPtr, errorCode, continued, resultExt);
  }

  // ── Stdin interaction ──────────────────────────────────────────────────────
  pushStdin(data) {
    const encoder = new TextEncoder();
    const bytes = typeof data === 'string' ? encoder.encode(data) : data;
    for (let i = 0; i < bytes.length; i++) {
      this.stdinBuffer.push(bytes[i]);
    }
    this.pumpStdinReads();
    this.wakeScheduler();
  }

  pumpStdinReads() {
    if (this.pendingStdinReads.length === 0 || this.stdinBuffer.length === 0) return;
    const { ovPtr, opId, ptr, len } = this.pendingStdinReads.shift();
    if (!this.isOpActive(ovPtr, opId)) {
      this.pumpStdinReads();
      return;
    }
    const toRead = Math.min(len, this.stdinBuffer.length);
    const chunk = this.stdinBuffer.splice(0, toRead);
    const guestBuf = this.getGuestSlice(ptr, toRead);
    guestBuf.set(chunk);

    this.enqueue(() => {
      this.completeOp(ovPtr, opId, 0, 0, toRead);
    });
  }

  // ── Signals ────────────────────────────────────────────────────────────────
  triggerSignal(signum) {
    let completed = 0;
    const waiter = this.signalWaiters.get(signum);
    if (waiter) {
      this.signalWaiters.delete(signum);
      this.enqueue(() => {
        if (this.isOpActive(waiter.ovPtr, waiter.opId)) {
          this.completeOp(waiter.ovPtr, waiter.opId, 0, 0, signum);
        }
      });
      completed++;
    }
    this.wakeScheduler();
    return completed;
  }
}

// ── ABI Import Constructor ───────────────────────────────────────────────────
export function makeBrowserImports(hostState, fs, params = {}) {
  const encoder = new TextEncoder();
  const decoder = new TextDecoder();

  const stdoutCb = params.stdout || ((bytes) => {});
  const stderrCb = params.stderr || ((bytes) => {});
  const getTtySizeCb = params.getTtySize || (() => ({ cols: 80, rows: 24 }));
  const setTtySizeCb = params.setTtySize;
  const args = params.args || ['vegetable'];
  const envObj = params.env || {
    TERM: 'xterm-256color',
    COLORTERM: 'truecolor',
    PATH: '/bin',
    HOME: '/home/user'
  };
  const extensions = params.extensions || {};

  function pathStr(ptr, len) {
    const raw = decoder.decode(hostState.getGuestSlice(Number(ptr), Number(len)));
    return resolvePath(hostState.cwd, raw);
  }

  function finishPipeReads(pipe) {
    while (pipe.readers.length > 0 && (pipe.buffer.length > 0 || pipe.writerClosed || pipe.readerClosed)) {
      const request = pipe.readers.shift();
      if (!hostState.isOpActive(request.ovPtr, request.opId)) continue;
      if (pipe.readerClosed) {
        hostState.enqueue(() => hostState.completeOp(request.ovPtr, request.opId, wasiEBADF, 0, 0));
        continue;
      }
      const count = Math.min(request.len, pipe.buffer.length);
      if (count > 0) {
        hostState.getGuestSlice(request.ptr, count).set(pipe.buffer.splice(0, count));
      }
      hostState.enqueue(() => hostState.completeOp(request.ovPtr, request.opId, 0, 0, count));
    }
  }

  const env = {
    // ── Process & Environment ────────────────────────────────────────────────
    get_args: (ptr, len) => {
      let totalLen = 0;
      const encoded = args.map(a => {
        const e = encoder.encode(a + '\0');
        totalLen += e.length;
        return e;
      });
      if (ptr !== 0 && len >= totalLen) {
        let offset = Number(ptr);
        for (const e of encoded) {
          hostState.getGuestSlice(offset, e.length).set(e);
          offset += e.length;
        }
      }
      return (BigInt(args.length) << 32n) | BigInt(totalLen);
    },

    get_env: (ptr, len) => {
      const pairs = Array.isArray(envObj)
        ? envObj
        : Object.entries(envObj).map(([k, v]) => `${k}=${v}`);
      let totalLen = 0;
      const encoded = pairs.map(p => {
        const e = encoder.encode(p + '\0');
        totalLen += e.length;
        return e;
      });
      if (ptr !== 0 && len >= totalLen) {
        let offset = Number(ptr);
        for (const e of encoded) {
          hostState.getGuestSlice(offset, e.length).set(e);
          offset += e.length;
        }
      }
      return (BigInt(pairs.length) << 32n) | BigInt(totalLen);
    },

    get_cwd: (ptr, len) => {
      const enc = encoder.encode(hostState.cwd);
      if (ptr !== 0) {
        const n = Math.min(enc.length, Number(len));
        hostState.getGuestSlice(Number(ptr), n).set(enc.subarray(0, n));
      }
      return BigInt(enc.length);
    },

    set_cwd: (ptr, len) => {
      const p = pathStr(ptr, len);
      // Asynchronously verify against fs or update cwd optimistically
      hostState.cwd = p;
      return 0;
    },

    get_time: () => BigInt(Date.now()) * 1000000n,

    get_random: (ptr, len) => {
      if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
        crypto.getRandomValues(hostState.getGuestSlice(Number(ptr), Number(len)));
      }
    },

    timer_set: (ovPtr, delayMs) => {
      const opId = hostState.registerOp(Number(ovPtr));
      setTimeout(() => {
        hostState.enqueue(() => {
          hostState.completeOp(Number(ovPtr), opId, 0, 0, 0);
        });
      }, Math.max(0, Number(delayMs)));
    },

    cancel: (ovPtr) => {
      const p = Number(ovPtr);
      if (hostState.activeOps.has(p)) {
        const opId = hostState.activeOps.get(p);
        hostState.completeOp(p, opId, wasiECANCELED, 0, 0);
      }
    },

    process_exit: (code) => {
      hostState.forcedExitCode = Number(code);
    },

    host_panic: (ptr, len) => {
      const msg = decoder.decode(hostState.getGuestSlice(Number(ptr), Number(len)));
      console.error('[HOST PANIC]', msg);
      if (params.stderr) params.stderr(encoder.encode(`\r\n[HOST PANIC]: ${msg}\r\n`));
      throw new Error(`Guest panicked: ${msg}`);
    },

    rusticated_debug: (val) => {
      console.debug('[GUEST DEBUG]', Number(val));
    },

    // ── Overlapped I/O ───────────────────────────────────────────────────────
    read: (ovPtr, handle, ptr, len) => {
      const h = hostState.getHandle(handle);
      const opId = hostState.registerOp(Number(ovPtr));
      const uPtr = Number(ptr);
      const uLen = Number(len);

      if (!h) {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiEBADF, 0, 0));
        return;
      }

      if (h.type === 'stdin') {
        if (hostState.stdinBuffer.length > 0) {
          const toRead = Math.min(uLen, hostState.stdinBuffer.length);
          const chunk = hostState.stdinBuffer.splice(0, toRead);
          hostState.getGuestSlice(uPtr, toRead).set(chunk);
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, toRead));
        } else {
          // Park read until stdin data is pushed
          hostState.pendingStdinReads.push({ ovPtr: Number(ovPtr), opId, ptr: uPtr, len: uLen });
        }
        return;
      }

      if (h.type === 'pipe_r') {
        if (uLen === 0) {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, 0));
          return;
        }
        h.pipe.readers.push({ ovPtr: Number(ovPtr), opId, ptr: uPtr, len: uLen });
        finishPipeReads(h.pipe);
        return;
      }

      if (h.type === 'file') {
        const entry = h.entry;
        const offset = h.offset || 0;
        const available = entry.content ? entry.content.length - offset : 0;
        const toRead = Math.min(uLen, Math.max(0, available));
        if (toRead > 0 && entry.content) {
          hostState.getGuestSlice(uPtr, toRead).set(entry.content.subarray(offset, offset + toRead));
          h.offset = offset + toRead;
        }
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, toRead));
        return;
      }

      if (h.type === 'proxy') {
        Promise.resolve(h.proxy.read(uLen)).then((chunk) => {
          hostState.enqueue(() => {
            if (!hostState.isOpActive(Number(ovPtr), opId)) return;
            const n = Math.min(uLen, chunk ? chunk.length : 0);
            if (n > 0) hostState.getGuestSlice(uPtr, n).set(chunk.subarray(0, n));
            hostState.completeOp(Number(ovPtr), opId, 0, 0, n);
          });
        }).catch((err) => {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
        });
        return;
      }

      hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiEINVAL, 0, 0));
    },

    write: (ovPtr, handle, ptr, len) => {
      const h = hostState.getHandle(handle);
      const opId = hostState.registerOp(Number(ovPtr));
      const uPtr = Number(ptr);
      const uLen = Number(len);

      if (!h) {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiEBADF, 0, 0));
        return;
      }

      const chunk = hostState.getGuestSlice(uPtr, uLen).slice();

      if (h.type === 'stdout') {
        stdoutCb(chunk);
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, uLen));
        return;
      }

      if (h.type === 'stderr') {
        stderrCb(chunk);
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, uLen));
        return;
      }

      if (h.type === 'pipe_w') {
        if (h.pipe.readerClosed) {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiEPIPE, 0, 0));
          return;
        }
        for (const byte of chunk) h.pipe.buffer.push(byte);
        finishPipeReads(h.pipe);
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, uLen));
        return;
      }

      if (h.type === 'file') {
        const entry = h.entry;
        const offset = h.offset || 0;
        const needed = offset + uLen;
        let buf = entry.content;
        if (!buf || buf.length < needed) {
          const next = new Uint8Array(Math.max(needed, (buf ? buf.length : 0) * 2));
          if (buf) next.set(buf);
          buf = next;
        }
        buf.set(chunk, offset);
        const newSize = Math.max(entry.size || 0, needed);
        entry.size = newSize;
        entry.content = buf.subarray(0, newSize);
        entry.mtime = Date.now();
        h.offset = needed;

        fs.putEntry(entry).then(() => {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, uLen));
        }).catch((err) => {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
        });
        return;
      }

      if (h.type === 'proxy') {
        try {
          h.proxy.write(chunk);
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, uLen));
        } catch (err) {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
        }
        return;
      }

      hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiEINVAL, 0, 0));
    },

    handle_close: (handle) => {
      hostState.freeHandle(handle);
      return 0;
    },

    seek: (ovPtr, handle, offset, whence) => {
      const h = hostState.getHandle(handle);
      const ptr = Number(ovPtr);
      const opId = hostState.registerOp(ptr);
      if (!h || h.type !== 'file') {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiEBADF, 0, 0));
        return;
      }

      const currentOffset = Number(h.offset || 0);
      const fileSize = Number(h.entry.size ?? h.entry.content?.length ?? 0);
      const seekOffset = Number(offset);
      const whenceValue = Number(whence);
      const base = whenceValue === 0 ? 0
        : whenceValue === 1 ? currentOffset
        : whenceValue === 2 ? fileSize
        : NaN;
      const nextOffset = base + seekOffset;
      if (!Number.isSafeInteger(seekOffset) || !Number.isSafeInteger(nextOffset) || nextOffset < 0) {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiEINVAL, 0, 0));
        return;
      }

      h.offset = nextOffset;
      hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, nextOffset));
    },

    // ── Filesystem ───────────────────────────────────────────────────────────
    path_open: (ovPtr, pathPtr, pathLen, flags, mode) => {
      const p = pathStr(pathPtr, pathLen);
      const opId = hostState.registerOp(Number(ovPtr));

      if (p === '/dev/eval' || p === 'dev/eval') {
        const evalProxy = {
          reqChunks: [],
          respBytes: null,
          readOffset: 0,
          write(chunk) {
            this.reqChunks.push(chunk);
          },
          read(uLen) {
            if (!this.respBytes) {
              let totalLen = 0;
              for (const c of this.reqChunks) totalLen += c.length;
              const allReq = new Uint8Array(totalLen);
              let off = 0;
              for (const c of this.reqChunks) {
                allReq.set(c, off);
                off += c.length;
              }
              const code = decoder.decode(allReq);
              let resultPromise;
              try {
                const res = (0, eval)(code);
                resultPromise = Promise.resolve(res);
              } catch (syncErr) {
                resultPromise = Promise.reject(syncErr);
              }
              return resultPromise.then((val) => {
                const text = String(val ?? '');
                const encoded = encoder.encode(text);
                const out = new Uint8Array(1 + encoded.length);
                out[0] = 0; // status ok
                out.set(encoded, 1);
                this.respBytes = out;
                const sliceLen = Math.min(uLen, out.length - this.readOffset);
                const chunk = out.subarray(this.readOffset, this.readOffset + sliceLen);
                this.readOffset += sliceLen;
                return chunk;
              }).catch((err) => {
                const errText = String(err?.stack || err?.message || err);
                const encoded = encoder.encode(errText);
                const out = new Uint8Array(1 + encoded.length);
                out[0] = 1; // status error
                out.set(encoded, 1);
                this.respBytes = out;
                const sliceLen = Math.min(uLen, out.length - this.readOffset);
                const chunk = out.subarray(this.readOffset, this.readOffset + sliceLen);
                this.readOffset += sliceLen;
                return chunk;
              });
            }
            if (this.readOffset >= this.respBytes.length) {
              return new Uint8Array(0); // EOF
            }
            const sliceLen = Math.min(uLen, this.respBytes.length - this.readOffset);
            const chunk = this.respBytes.subarray(this.readOffset, this.readOffset + sliceLen);
            this.readOffset += sliceLen;
            return chunk;
          }
        };
        const handle = hostState.allocHandle({
          type: 'proxy',
          proxy: evalProxy,
        });
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, Number(handle)));
        return;
      }

      const openFlags = Number(flags);
      const O_CREAT = 64;
      const O_EXCL = 128;
      const O_TRUNC = 512;
      const O_APPEND = 1024;

      fs.getEntry(p).then((entry) => {
        if (!entry) {
          // If write flags allow creation
          if ((openFlags & O_CREAT) !== 0) {
            const newEntry = { path: p, type: 'file', content: new Uint8Array(0), size: 0, mode: Number(mode) || 0o644, mtime: Date.now() };
            return fs.putEntry(newEntry).then(() => newEntry);
          }
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOENT, 0, 0));
          return null;
        }
        if ((openFlags & O_CREAT) !== 0 && (openFlags & O_EXCL) !== 0) {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiEEXIST, 0, 0));
          return null;
        }
        if (entry.type === 'file' && (openFlags & O_TRUNC) !== 0) {
          entry.content = new Uint8Array(0);
          entry.size = 0;
          entry.mtime = Date.now();
          return fs.putEntry(entry).then(() => entry);
        }
        return entry;
      }).then((entry) => {
        if (!entry) return;
        const initialOffset = (entry.type === 'file' && (openFlags & O_APPEND) !== 0)
          ? Number(entry.size ?? entry.content?.length ?? 0)
          : 0;
        const handle = hostState.allocHandle({
          type: entry.type === 'dir' ? 'dir' : 'file',
          entry,
          offset: initialOffset,
          flags: openFlags
        });
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, Number(handle)));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      });
    },

    path_stat: (ovPtr, pathPtr, pathLen, flags, outPtr, outLen) => {
      const p = pathStr(pathPtr, pathLen);
      const opId = hostState.registerOp(Number(ovPtr));

      if (p === '/dev/eval' || p === 'dev/eval') {
        const statBytes = marshalStat({ path: p, type: 'file', size: 0, mode: 0o666, mtime: Date.now() });
        hostState.getGuestSlice(Number(outPtr), 64).set(statBytes);
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, 64));
        return;
      }

      const statPromise = typeof fs.statEntry === 'function' ? fs.statEntry(p) : fs.getEntry(p);
      statPromise.then((entry) => {
        if (!entry) {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOENT, 0, 0));
          return;
        }
        if (Number(outLen) < 64) {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiERANGE, 0, 64));
          return;
        }
        const statBytes = marshalStat(entry);
        hostState.getGuestSlice(Number(outPtr), 64).set(statBytes);
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, 64));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      });
    },

    dir_read: (ovPtr, handle, bufPtr, bufLen) => {
      const h = hostState.getHandle(handle);
      const opId = hostState.registerOp(Number(ovPtr));

      if (!h || h.type !== 'dir') {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiEBADF, 0, 0));
        return;
      }

      fs.readdir(h.entry.path).then((children) => {
        let offset = 0;
        const dest = hostState.getGuestSlice(Number(bufPtr), Number(bufLen));
        const startIdx = h.dirIdx || 0;
        let count = 0;

        for (let i = startIdx; i < children.length; i++) {
          const c = children[i];
          const name = c.path.slice(c.path.lastIndexOf('/') + 1);
          const encName = encoder.encode(name);
          const entryLen = 1 + encName.length + 1;
          if (offset + entryLen > Number(bufLen)) break;
          dest[offset] = c.type === 'dir' ? 2 : c.type === 'symlink' ? 3 : 1;
          dest.subarray(offset + 1, offset + entryLen).set(encName);
          dest[offset + entryLen - 1] = 0;
          offset += entryLen;
          count++;
          h.dirIdx = i + 1;
        }
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, offset));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      });
    },

    path_chmod: (ovPtr, pathPtr, pathLen, mode) => {
      const p = pathStr(pathPtr, pathLen);
      const opId = hostState.registerOp(Number(ovPtr));
      fs.getEntry(p).then((entry) => {
        if (!entry) {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOENT, 0, 0));
          return;
        }
        entry.mode = Number(mode);
        return fs.putEntry(entry).then(() => {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, 0));
        });
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      });
    },

    path_remove: (ovPtr, pathPtr, pathLen) => {
      const p = pathStr(pathPtr, pathLen);
      const opId = hostState.registerOp(Number(ovPtr));
      fs.removeEntry(p).then((ok) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, ok ? 0 : wasiENOENT, 0, 0));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      });
    },

    path_mkdir: (ovPtr, pathPtr, pathLen, mode) => {
      const p = pathStr(pathPtr, pathLen);
      const opId = hostState.registerOp(Number(ovPtr));
      const entry = { path: p, type: 'dir', mode: Number(mode) || 0o755, mtime: Date.now() };
      fs.putEntry(entry).then(() => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, 0));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      });
    },

    path_rename: (ovPtr, srcPtr, srcLen, dstPtr, dstLen) => {
      const src = pathStr(srcPtr, srcLen);
      const dst = pathStr(dstPtr, dstLen);
      const opId = hostState.registerOp(Number(ovPtr));
      if (typeof fs.renameEntry === 'function') {
        fs.renameEntry(src, dst).then((ok) => {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, ok === false ? wasiENOENT : 0, 0, 0));
        }).catch((err) => {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
        });
        return;
      }
      fs.getEntry(src).then((ent) => {
        if (!ent) {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOENT, 0, 0));
          return;
        }
        ent.path = dst;
        return fs.putEntry(ent).then(() => fs.removeEntry(src)).then(() => {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, 0));
        });
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      });
    },

    // ── TTY & Terminal ───────────────────────────────────────────────────────
    signal_wait: (ovPtr, signum) => {
      const sig = Number(signum);
      const opId = hostState.registerOp(Number(ovPtr));
      hostState.signalWaiters.set(sig, { ovPtr: Number(ovPtr), opId });
    },

    tty_get_size: (ptr) => {
      const { cols, rows } = getTtySizeCb();
      if (ptr !== 0) {
        const view = hostState.view;
        view.setUint16(Number(ptr), rows, true);
        view.setUint16(Number(ptr) + 2, cols, true);
      }
      return ((cols & 0xffff) << 16) | (rows & 0xffff);
    },

    tty_set_size: (handle, cols, rows) => {
      const h = hostState.getHandle(handle);
      if (!h) return wasiEBADF;
      if (!Number.isInteger(cols) || !Number.isInteger(rows) || cols <= 0 || rows <= 0 || cols > 0xffff || rows > 0xffff) {
        return wasiEINVAL;
      }
      if (typeof h.resize === 'function') {
        h.resize(cols, rows);
        return wasiSuccess;
      }
      if (h.type !== 'stdin' && h.type !== 'stdout' && h.type !== 'stderr') return wasiENOTTY;
      if (typeof setTtySizeCb !== 'function') return wasiENOTTY;
      setTtySizeCb(cols, rows);
      return wasiSuccess;
    },

    tty_set_mode: (handle, mode) => {
      hostState.rawTtyMode = Number(mode);
      return 0;
    },

    fd_isatty: (handle) => {
      const h = hostState.getHandle(handle);
      return (h && (h.type === 'stdin' || h.type === 'stdout' || h.type === 'stderr')) ? 1 : 0;
    },

    get_platform_info: (ptr, maxLen) => {
      const descriptor = new Uint8Array(676);
      const view = new DataView(descriptor.buffer);
      view.setUint32(0, 1, true);
      descriptor[4] = '/'.charCodeAt(0);
      descriptor[5] = ':'.charCodeAt(0);
      view.setUint16(6, 0, true);
      descriptor[18] = 32;

      const setString = (offset, value) => {
        descriptor.set(encoder.encode(value).subarray(0, 63), offset);
      };
      setString(20, 'browser');
      setString(84, 'browser');
      setString(220, 'washmhost');
      setString(548, '/home/user');
      setString(612, '/tmp');

      if (Number(maxLen) < descriptor.length) return wasiE2BIG;
      hostState.getGuestSlice(Number(ptr), descriptor.length).set(descriptor);
      return 0;
    },

    // ── Extension & Dynamic Library Hooks ────────────────────────────────────
    dylib_open: (ovPtr, pathPtr, pathLen, flags) => {
      const raw = decoder.decode(hostState.getGuestSlice(Number(pathPtr), Number(pathLen)));
      const opId = hostState.registerOp(Number(ovPtr));
      if (raw === 'eval' || raw.endsWith('/eval')) {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, EVAL_LIB_HANDLE));
        return;
      }
      if (!extensions.dylib) {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOSYS, 0, 0));
        return;
      }
      const libPath = pathStr(pathPtr, pathLen);
      Promise.resolve(extensions.dylib.open(libPath, Number(flags))).then((handle) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, handle == null ? wasiENOENT : 0, 0, handle || 0));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      });
    },

    dylib_sym: (ovPtr, libHandle, symPtr, symLen) => {
      const sym = decoder.decode(hostState.getGuestSlice(Number(symPtr), Number(symLen)));
      const opId = hostState.registerOp(Number(ovPtr));
      if (BigInt(libHandle) === EVAL_LIB_HANDLE) {
        if (sym === 'eval') {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, EVAL_SYM_HANDLE));
        } else {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOENT, 0, 0));
        }
        return;
      }
      if (!extensions.dylib || !extensions.dylib.resolveSymbol) {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOSYS, 0, 0));
        return;
      }
      Promise.resolve(extensions.dylib.resolveSymbol(BigInt(libHandle), sym)).then((symHandle) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, symHandle == null ? wasiENOENT : 0, 0, symHandle || 0));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      });
    },

    dylib_resolve: (libHandle, symPtr, symLen) => {
      const sym = decoder.decode(hostState.getGuestSlice(Number(symPtr), Number(symLen)));
      if (BigInt(libHandle) === EVAL_LIB_HANDLE) {
        return sym === 'eval' ? EVAL_SYM_HANDLE : 0;
      }
      if (!extensions.dylib || !extensions.dylib.resolveSymbol) return 0;
      return extensions.dylib.resolveSymbol(BigInt(libHandle), sym) || 0;
    },

    dylib_close: (ovPtr, libHandle) => {
      const ptr = Number(ovPtr);
      const opId = hostState.registerOp(ptr);
      if (BigInt(libHandle) === EVAL_LIB_HANDLE) {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, 0));
        return;
      }
      if (!extensions.dylib || !extensions.dylib.close) {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, 0));
        return;
      }
      Promise.resolve().then(() => extensions.dylib.close(BigInt(libHandle))).then(() => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, 0));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, mapErrno(err), 0, 0));
      });
    },

    dylib_call: (ovPtr, symHandle, sigPtr, sigLen, argsPtr, argsCount, parentInvID, resultsPtr, resultsCap) => {
      const opId = hostState.registerOp(Number(ovPtr));
      if (BigInt(symHandle) === EVAL_SYM_HANDLE) {
        try {
          const count = Number(argsCount || 0);
          const base = Number(argsPtr || 0);
          if (count < 5 || base === 0) {
            hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiEINVAL, 0, 0));
            return;
          }
          const initialView = hostState.view;
          const codePtr = Number(initialView.getBigUint64(base + 0, true));
          const codeLen = Number(initialView.getBigUint64(base + 8, true));
          const outPtr = Number(initialView.getBigUint64(base + 16, true));
          const outCap = Number(initialView.getBigUint64(base + 24, true));
          const outLenPtr = Number(initialView.getBigUint64(base + 32, true));

          const code = decoder.decode(hostState.getGuestSlice(codePtr, codeLen));

          const finishEval = (status, text) => {
            const rawStr = String(text ?? '');
            const encoded = encoder.encode(rawStr);
            const currentView = hostState.view;
            if (outLenPtr !== 0) {
              currentView.setUint32(outLenPtr, encoded.length, true);
            }
            if (outCap > 0 && encoded.length > 0 && outPtr !== 0) {
              const toCopy = Math.min(outCap, encoded.length);
              hostState.getGuestSlice(outPtr, toCopy).set(encoded.subarray(0, toCopy));
            }
            let rc = BigInt(status);
            if (encoded.length > outCap) {
              rc = 2n; // buffer overflow
              if (typeof globalThis !== 'undefined') {
                globalThis.__rustica_overflow = rawStr;
                globalThis.__rustica_take_overflow = () => {
                  const s = globalThis.__rustica_overflow;
                  globalThis.__rustica_overflow = '';
                  return s;
                };
              }
            }
            if (Number(resultsCap || 0) > 0 && Number(resultsPtr || 0) !== 0) {
              currentView.setBigUint64(Number(resultsPtr), rc, true);
            }
            hostState.completeOp(Number(ovPtr), opId, 0, 0, rc);
          };

          let result;
          try {
            result = (0, eval)(code);
          } catch (syncErr) {
            hostState.enqueue(() => finishEval(1, syncErr?.stack || syncErr?.message || String(syncErr)));
            return;
          }

          if (result != null && typeof result.then === 'function') {
            Promise.resolve(result).then(
              (val) => hostState.enqueue(() => finishEval(0, val)),
              (err) => hostState.enqueue(() => finishEval(1, err?.stack || err?.message || String(err)))
            );
          } else {
            hostState.enqueue(() => finishEval(0, result));
          }
        } catch (e) {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(e), 0, 0));
        }
        return;
      }

      if (!extensions.dylib || !extensions.dylib.call) {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOSYS, 0, 0));
        return;
      }
      Promise.resolve(extensions.dylib.call(BigInt(symHandle), Number(sigPtr), Number(sigLen), { hostState, fs, argsPtr, argsCount, parentInvID, resultsPtr, resultsCap })).then((res) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, res || 0));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      });
    },

    dylib_alloc: (ovPtr, libHandle, size, align) => {
      const ptr = Number(ovPtr);
      const opId = hostState.registerOp(ptr);
      if (!extensions.dylib || !extensions.dylib.alloc) {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiENOSYS, 0, 0));
        return;
      }
      Promise.resolve().then(() => extensions.dylib.alloc(BigInt(libHandle), BigInt(size), Number(align))).then((address) => {
        if (address == null) {
          hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiENOMEM, 0, 0));
          return;
        }
        hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, address));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, mapErrno(err), 0, 0));
      });
    },

    dylib_free: (ovPtr, libHandle, address) => {
      const ptr = Number(ovPtr);
      const opId = hostState.registerOp(ptr);
      if (!extensions.dylib || !extensions.dylib.free) {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiENOSYS, 0, 0));
        return;
      }
      Promise.resolve().then(() => extensions.dylib.free(BigInt(libHandle), BigInt(address))).then(() => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, 0));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, mapErrno(err), 0, 0));
      });
    },

    dylib_read_mem: (ovPtr, libHandle, address, guestPtr, length) => {
      const ptr = Number(ovPtr);
      const opId = hostState.registerOp(ptr);
      if (!extensions.dylib || !extensions.dylib.readMem) {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiENOSYS, 0, 0));
        return;
      }
      Promise.resolve().then(() => extensions.dylib.readMem(BigInt(libHandle), BigInt(address), Number(length))).then((bytes) => {
        if (!(bytes instanceof Uint8Array) || bytes.length < Number(length)) {
          hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiEIO, 0, 0));
          return;
        }
        hostState.getGuestSlice(Number(guestPtr), Number(length)).set(bytes.subarray(0, Number(length)));
        hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, 0));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, mapErrno(err), 0, 0));
      });
    },

    dylib_write_mem: (ovPtr, libHandle, address, guestPtr, length) => {
      const ptr = Number(ovPtr);
      const opId = hostState.registerOp(ptr);
      if (!extensions.dylib || !extensions.dylib.writeMem) {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiENOSYS, 0, 0));
        return;
      }
      const bytes = hostState.getGuestSlice(Number(guestPtr), Number(length)).slice();
      Promise.resolve().then(() => extensions.dylib.writeMem(BigInt(libHandle), BigInt(address), bytes)).then(() => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, 0));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, mapErrno(err), 0, 0));
      });
    },

    dylib_callback_register: (ovPtr, libHandle, sigPtr, sigLen, cbOvPtr, cbOvLen) => {
      const ptr = Number(ovPtr);
      const opId = hostState.registerOp(ptr);
      if (!extensions.dylib || !extensions.dylib.registerCallback) {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiENOSYS, 0, 0));
        return;
      }
      const signature = hostState.getGuestSlice(Number(sigPtr), Number(sigLen)).slice();
      Promise.resolve().then(() => extensions.dylib.registerCallback(
        BigInt(libHandle),
        signature,
        Number(cbOvPtr),
        Number(cbOvLen),
        { hostState, fs }
      )).then((trampoline) => {
        if (trampoline == null) {
          hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiENOSYS, 0, 0));
          return;
        }
        hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, trampoline));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, mapErrno(err), 0, 0));
      });
    },

    // ── Network Hooks ────────────────────────────────────────────────────────
    net_open: (ovPtr, addrPtr, addrLen, port, flags) => {
      const addr = decoder.decode(hostState.getGuestSlice(Number(addrPtr), Number(addrLen)));
      const opId = hostState.registerOp(Number(ovPtr));
      if (!extensions.net || !extensions.net.open) {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOTSUP, 0, 0));
        return;
      }
      try {
        const proxy = extensions.net.open(addr, Number(port), Number(flags));
        if (!proxy) {
          hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiECONNREFUSED, 0, 0));
          return;
        }
        const handle = hostState.allocHandle({ type: 'proxy', proxy });
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, Number(handle)));
      } catch (err) {
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, mapErrno(err), 0, 0));
      }
    },

    net_accept: (ovPtr, handle) => {
      const ptr = Number(ovPtr);
      const opId = hostState.registerOp(ptr);
      if (!extensions.net || !extensions.net.accept) {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiENOTSUP, 0, 0));
        return;
      }
      Promise.resolve().then(() => extensions.net.accept(BigInt(handle))).then((proxy) => {
        if (!proxy) {
          hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiECONNABORTED, 0, 0));
          return;
        }
        const clientHandle = hostState.allocHandle({ type: 'proxy', proxy });
        hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, Number(clientHandle)));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, mapErrno(err), 0, 0));
      });
    },

    net_lookup: (ovPtr, hostPtr, hostLen, outPtr, outLen) => {
      const hostname = decoder.decode(hostState.getGuestSlice(Number(hostPtr), Number(hostLen)));
      const opId = hostState.registerOp(Number(ovPtr));
      if (hostname === '127.0.0.1' || hostname === 'localhost') {
        const ip = new Uint8Array([127, 0, 0, 1]);
        hostState.getGuestSlice(Number(outPtr), Math.min(4, Number(outLen))).set(ip);
        hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, 0, 0, 4));
        return;
      }
      hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiEHOSTUNREACH, 0, 0));
    },

    net_cert_verify: (ovPtr, namePtr, nameLen, chainPtr, chainLen) => {
      const ptr = Number(ovPtr);
      const opId = hostState.registerOp(ptr);
      if (!extensions.net || !extensions.net.verifyCertificate) {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, wasiENOSYS, 0, 0));
        return;
      }
      const name = decoder.decode(hostState.getGuestSlice(Number(namePtr), Number(nameLen)));
      const chain = hostState.getGuestSlice(Number(chainPtr), Number(chainLen)).slice();
      Promise.resolve().then(() => extensions.net.verifyCertificate(name, chain)).then((verified) => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, verified ? 1 : 0));
      }).catch((err) => {
        hostState.enqueue(() => hostState.completeOp(ptr, opId, mapErrno(err), 0, 0));
      });
    },

    // ── Process Control Stubs ────────────────────────────────────────────────
    process_spawn: (ovPtr) => {
      const opId = hostState.registerOp(Number(ovPtr));
      hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOSYS, 0, 0));
    },
    process_pipe: (ovPtr) => {
      const ptr = Number(ovPtr);
      const opId = hostState.registerOp(ptr);
      const pipe = { buffer: [], readers: [], readerClosed: false, writerClosed: false };
      const readHandle = hostState.allocHandle({
        type: 'pipe_r',
        pipe,
        close: () => {
          pipe.readerClosed = true;
          pipe.buffer.length = 0;
          finishPipeReads(pipe);
        }
      });
      const writeHandle = hostState.allocHandle({
        type: 'pipe_w',
        pipe,
        close: () => {
          pipe.writerClosed = true;
          finishPipeReads(pipe);
        }
      });
      const resultExt = (writeHandle << 32n) | readHandle;
      hostState.enqueue(() => hostState.completeOp(ptr, opId, 0, 0, resultExt));
    },
    process_wait: (ovPtr) => {
      const opId = hostState.registerOp(Number(ovPtr));
      hostState.enqueue(() => hostState.completeOp(Number(ovPtr), opId, wasiENOSYS, 0, 0));
    },
    process_signal: () => wasiENOSYS
  };

  // ABI aliases for compatibility across builds
  env.proc_spawn = env.process_spawn;
  env.process_kill = env.process_signal;
  env.get_pid = () => 1234n;
  env.process_get_pid = env.get_pid;
  env.fs_chmod = env.path_chmod;
  env.fs_remove = env.path_remove;
  env.fs_mkdir = env.path_mkdir;

  return { env };
}

// ── Cooperative Event Loop ───────────────────────────────────────────────────
export async function runEventLoop(hostState, runFunc, onExit) {
  let firstTurn = true;
  while (true) {
    // 1. Drain pending completions into guest WASM memory
    hostState.drainQueue();

    if (hostState.forcedExitCode !== -1) {
      if (onExit) onExit(hostState.forcedExitCode);
      break;
    }

    // 2. Call guest run loop
    try {
      runFunc();
    } catch (err) {
      if (hostState.forcedExitCode !== -1) {
        if (onExit) onExit(hostState.forcedExitCode);
        break;
      }
      console.error('[washmhost] run() threw error:', err);
      hostState.forcedExitCode = 1;
      if (onExit) onExit(1);
      break;
    }

    // 3. Check for explicit exit
    if (hostState.forcedExitCode !== -1) {
      if (onExit) onExit(hostState.forcedExitCode);
      break;
    }

    // 4. Synchronously queued completions need an immediate next pass
    if (hostState.completionQueue.length > 0) {
      continue;
    }

    // 5. A parked guest can have no outstanding host operation yet (for
    // example, before its input goroutine starts). Give startup one extra turn,
    // then sleep until stdin, a signal, or an async completion wakes it.
    if (!hostState.hasPending()) {
      if (firstTurn) {
        firstTurn = false;
        await new Promise((resolve) => setTimeout(resolve, 0));
        continue;
      }
      await hostState.waitForNotification();
      continue;
    }

    // 6. Outstanding ops exist (IndexedDB I/O, timers, net socket).
    // Sleep until notified by enqueue(); do NOT spin-hammer guest.
    firstTurn = false;
    await hostState.waitForNotification();
  }
}

// ── Vegetable Decompression Helper ───────────────────────────────────────────
export async function unpackVegetable(buffer) {
  const bytes = buffer instanceof Uint8Array ? buffer : new Uint8Array(buffer);
  const headerSlice = bytes.subarray(0, Math.min(bytes.length, 32768));
  const headerText = new TextDecoder('utf-8', { fatal: false }).decode(headerSlice);

  const parseVar = (patterns) => {
    for (const pat of patterns) {
      const m = headerText.match(pat);
      if (m && m[1]) return parseInt(m[1].trim(), 10);
    }
    return null;
  };

  const poolLen = parseVar([
    /P_LEN\s*=\s*["']?([0-9]+)/,
    /poolLen\s*=\s*["']?([0-9]+)/,
    /set\s+"P_LEN=([0-9]+)/
  ]);
  const pldOff = parseVar([
    /PLD_OFF\s*=\s*["']?([0-9]+)/,
    /payloadOff\s*=\s*["']?([0-9]+)/,
    /set\s+"PLD_OFF=([0-9]+)/
  ]);
  const pldLen = parseVar([
    /PLD_LEN\s*=\s*["']?([0-9]+)/,
    /payloadLen\s*=\s*["']?([0-9]+)/,
    /set\s+"PLD_LEN=([0-9]+)/
  ]);
  const wasmLen = parseVar([
    /wasmLen\s*=\s*["']?([0-9]+)/
  ]);
  const wasmOff = parseVar([
    /wasmOff\s*=\s*["']?([0-9]+)/
  ]);

  if (poolLen === null || pldOff === null || pldLen === null) {
    throw new Error('Invalid vegetable: missing required pool or payload metadata');
  }

  // Locate decompressor WASM
  let decompressorBytes = null;
  if (wasmOff !== null && wasmLen !== null && wasmOff + wasmLen <= bytes.length) {
    decompressorBytes = bytes.subarray(wasmOff, wasmOff + wasmLen);
  } else {
    // Scan for WASM magic in Zone B
    const wasmMagic = [0x00, 0x61, 0x73, 0x6D];
    let found = -1;
    for (let i = 0; i < Math.min(bytes.length - 4, 100000); i++) {
      if (bytes[i] === wasmMagic[0] &&
          bytes[i+1] === wasmMagic[1] &&
          bytes[i+2] === wasmMagic[2] &&
          bytes[i+3] === wasmMagic[3]) {
        found = i;
        break;
      }
    }
    if (found === -1) {
      throw new Error('Decompressor WASM module not found in vegetable');
    }
    const end = (wasmLen !== null) ? found + wasmLen : bytes.length - poolLen;
    decompressorBytes = bytes.subarray(found, end);
  }

  // Instantiate decompressor WASM
  const { instance } = await WebAssembly.instantiate(decompressorBytes, {});
  const { memory, brot_alloc, brot_decompress } = instance.exports;
  if (!brot_alloc || !brot_decompress || !memory) {
    throw new Error('Vegetable decompressor WASM missing required exports');
  }

  const poolStart = bytes.length - poolLen;
  if (poolStart < 0) {
    throw new Error(`Invalid poolStart (${poolStart}) for vegetable of size ${bytes.length}`);
  }
  const compressedPool = bytes.subarray(poolStart);

  const inPtr = brot_alloc(compressedPool.length);
  new Uint8Array(memory.buffer, inPtr, compressedPool.length).set(compressedPool);

  const uncompressedLen = pldOff + pldLen;
  const packed = brot_decompress(inPtr, compressedPool.length, uncompressedLen);
  if (packed === 0n) {
    throw new Error('Vegetable pool decompression failed');
  }

  const outPtr = Number(packed & 0xFFFFFFFFn);
  const brainWasm = new Uint8Array(memory.buffer, outPtr + pldOff, pldLen);

  // Return an isolated ArrayBuffer copy of the payload WASM
  const outBuf = new ArrayBuffer(pldLen);
  new Uint8Array(outBuf).set(brainWasm);
  return outBuf;
}

// ── Main Entry Closure ───────────────────────────────────────────────────────
export default async function washmhost(params = {}) {
  const hostState = new HostState();
  if (typeof params.cwd === 'string' && params.cwd.trim() !== '') {
    hostState.cwd = resolvePath('/', params.cwd);
  }
  const fs = params.fs || new AsyncIndexedDBFS(params.dbName || 'RusticatedFS');

  // Whitebox / Unit Test Mode
  if (params.test === true) {
    if (params.memory) hostState.memory = params.memory;
    await fs.init();
    const { env } = makeBrowserImports(hostState, fs, params);
    return {
      hostState,
      fs,
      env,
      resolvePath: (target) => resolvePath(hostState.cwd, target),
      marshalStat,
      writeOverlapped: (ovPtr, code, cont, ext) => hostState.writeOverlapped(ovPtr, code, cont, ext)
    };
  }

  // Production Mode: Initialize storage
  await fs.init();
  await fs.seed(params.initialFiles || []);

  // Fetch / load WASM binary or Vegetable
  let wasmBinary = params.wasm;
  if (typeof wasmBinary === 'string') {
    const res = await fetch(wasmBinary);
    if (!res.ok) throw new Error(`HTTP ${res.status} fetching WASM from ${wasmBinary}`);
    wasmBinary = await res.arrayBuffer();
  } else if (wasmBinary instanceof Response) {
    wasmBinary = await wasmBinary.arrayBuffer();
  } else if (wasmBinary instanceof Blob) {
    wasmBinary = await wasmBinary.arrayBuffer();
  } else if (ArrayBuffer.isView(wasmBinary)) {
    wasmBinary = wasmBinary.buffer.slice(wasmBinary.byteOffset, wasmBinary.byteOffset + wasmBinary.byteLength);
  }

  // Check if wasmBinary is a vegetable (.bat / polyglot) instead of raw WASM
  const headBytes = new Uint8Array(wasmBinary, 0, Math.min(wasmBinary.byteLength, 4));
  const isRawWasm = headBytes.length >= 4 &&
                    headBytes[0] === 0x00 &&
                    headBytes[1] === 0x61 &&
                    headBytes[2] === 0x73 &&
                    headBytes[3] === 0x6D;

  if (!isRawWasm) {
    // Unpack payload WASM (brain) directly from the vegetable
    wasmBinary = await unpackVegetable(wasmBinary);
  }

  const { env } = makeBrowserImports(hostState, fs, params);
  const { instance } = await WebAssembly.instantiate(wasmBinary, { env });

  hostState.memory = instance.exports.memory;

  const runFunc = instance.exports.run || instance.exports._start;
  if (!runFunc) {
    throw new Error('WASM module missing "run" or "_start" export');
  }

  // Trigger initial resize signals to notify TUI (27 = Go wasip1 SIGWINCH, 28 = Unix/Rust SIGWINCH)
  hostState.triggerSignal(27);
  hostState.triggerSignal(28);

  // Launch cooperative scheduler in background
  runEventLoop(hostState, runFunc, params.onExit);

  return {
    pushStdin: (data) => hostState.pushStdin(data),
    triggerSignal: (signum) => hostState.triggerSignal(signum),
    isRunning: () => hostState.forcedExitCode === -1,
    getExitCode: () => hostState.forcedExitCode !== -1 ? hostState.forcedExitCode : null,
    getMemory: () => hostState.memory,
    getInstance: () => instance,
    getFS: () => fs,
    terminate: (exitCode = 0) => {
      hostState.forcedExitCode = exitCode;
      hostState.wakeScheduler();
    }
  };
}

if (typeof window !== 'undefined') {
  window.washmhost = washmhost;
}
if (typeof globalThis !== 'undefined') {
  globalThis.washmhost = washmhost;
  globalThis.washmhostExports = {
    washmhost,
    HostState,
    AsyncIndexedDBFS,
    resolvePath,
    marshalStat,
    STAT_KIND_UNKNOWN,
    STAT_KIND_FILE,
    STAT_KIND_DIR,
    STAT_KIND_SYMLINK,
    FLAG_COMPLETED,
    mapErrno,
    unpackVegetable,
    makeBrowserImports,
    runEventLoop
  };
}
