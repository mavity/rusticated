import fs from 'node:fs';
import net from 'node:net';
import child_process from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { PassThrough } from 'node:stream';
import crypto from 'node:crypto';
import process from 'node:process';
import os from 'node:os';

// Patched by buildNodeSlot:
const VERBOSE = false; // {{VERBOSE}}
const L = (...args) => (VERBOSE || process.env.MOHABBAT_VERBOSE) && console.error(...args);

const FLAG_COMPLETED = 1;

// ─────────────────────────────────────────────────────────────────────────────
// errno constants (mirrors Go's mapErrno)
// ─────────────────────────────────────────────────────────────────────────────
function mapErrno(e) {
  if (!e) return 0;
  switch (e.code) {
    case 'ENOENT':    return 2;
    case 'ESRCH':     return 3;
    case 'EINTR':     return 4;
    case 'EIO':       return 5;
    case 'ENXIO':     return 6;
    case 'EBADF':     return 9;
    case 'ECHILD':    return 10;
    case 'EAGAIN':    return 11;
    case 'ENOMEM':    return 12;
    case 'EACCES':    return 13;
    case 'EFAULT':    return 14;
    case 'EBUSY':     return 16;
    case 'EEXIST':    return 17;
    case 'EXDEV':     return 18;
    case 'ENODEV':    return 19;
    case 'ENOTDIR':   return 20;
    case 'EISDIR':    return 21;
    case 'EINVAL':    return 22;
    case 'EMFILE':    return 24;
    case 'ENOSPC':    return 28;
    case 'EPIPE':     return 32;
    case 'ERANGE':    return 34;
    case 'ENOTEMPTY': return 39;
    case 'ECONNREFUSED': return 111;
    case 'EADDRINUSE': return 98;
    default:
      return (e.errno != null ? Math.abs(e.errno) : 0) || 5;
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Path translation — /tmp -> os.tmpdir(), otherwise pass-through
// (mirrors Go translatePath)
// ─────────────────────────────────────────────────────────────────────────────
function translatePath(p) {
  // Normalize backslashes
  p = p.replace(/\\/g, '/');
  if (p.startsWith('/tmp/')) return os.tmpdir() + '/' + p.slice(5);
  if (p === '/tmp') return os.tmpdir();
  return p;
}

// ─────────────────────────────────────────────────────────────────────────────
// AbiStat marshalling  (64 bytes, little-endian)
// Layout mirrors marshalAbiStat / createAbiStat in env_fs.go
// ─────────────────────────────────────────────────────────────────────────────
const STAT_KIND_UNKNOWN = 0;
const STAT_KIND_FILE    = 1;
const STAT_KIND_DIR     = 2;
const STAT_KIND_SYMLINK = 3;

function marshalStat(st) {
  let kind = STAT_KIND_UNKNOWN;
  let modeBase = 0;
  if (st.isSymbolicLink())      { kind = STAT_KIND_SYMLINK; modeBase = 0o120000; }
  else if (st.isDirectory())    { kind = STAT_KIND_DIR;     modeBase = 0o040000; }
  else                           { kind = STAT_KIND_FILE;    modeBase = 0o100000; }

  const mode = modeBase | (st.mode & 0o7777);
  const size = BigInt(st.size);
  const mtime = BigInt(st.mtimeNs != null ? st.mtimeNs : Math.round(st.mtimeMs * 1e6));
  const buf = Buffer.alloc(64);
  buf.writeUInt32LE(kind, 0);
  buf.writeUInt32LE(mode, 4);
  buf.writeUInt32LE(0, 8);   // uid
  buf.writeUInt32LE(0, 12);  // gid
  buf.writeBigUInt64LE(size, 16);
  buf.writeBigUInt64LE(mtime, 24); // modifiedNs
  buf.writeBigUInt64LE(mtime, 32); // accessedNs
  buf.writeBigUInt64LE(mtime, 40); // createdNs
  buf.writeBigUInt64LE(1n, 48);    // nlink
  buf.writeBigUInt64LE(0n, 56);    // inode
  return buf;
}

// ─────────────────────────────────────────────────────────────────────────────
// Overlapped result write — mirrors writeOverlapped in env_utils.go
// Layout: u32 FLAG_COMPLETED | u32 errorCode | u64 continued | u64 resultExt
// ─────────────────────────────────────────────────────────────────────────────
function writeOverlapped(view, ovPtr, errorCode, continued, resultExt) {
  view.setUint32(ovPtr,      FLAG_COMPLETED, true);
  view.setUint32(ovPtr + 4,  errorCode,      true);
  view.setBigUint64(ovPtr + 8,  BigInt(continued), true);
  view.setBigUint64(ovPtr + 16, BigInt(resultExt), true);
}

// ─────────────────────────────────────────────────────────────────────────────
// HostState  — owns handles, pending-op tracking, signal delivery
// ─────────────────────────────────────────────────────────────────────────────
class HostState {
  constructor() {
    this.memory     = null;   // set after instantiation
    this.handles    = new Map();
    this.nextHandle = 3n;
    // stdio
    this.handles.set(0n, { type: 'stdio', stream: process.stdin,  fd: 0 });
    this.handles.set(1n, { type: 'stdio', stream: process.stdout, fd: 1 });
    this.handles.set(2n, { type: 'stdio', stream: process.stderr, fd: 2 });

    // Pending-op tracking (mirrors outstandingOps / activeOps)
    this.activeOps    = new Map();   // ovPtr(number) -> opId(number)
    this.nextOpId     = 1;
    this.pendingCount = 0;

    // Signal waiters: signum(number) -> {ovPtr, opId}
    this.signalWaiters = new Map();

    // Pending completions queue (functions to call before next run())
    this.completionQueue = [];

    this.forcedExitCode = -1;

    this._setupSignals();
  }

  // ── handle helpers ──────────────────────────────────────────────────────────
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
    if (item == null) return;
    this.handles.delete(bh);
    // Close underlying resource if owned (handle >= 3)
    if (bh >= 3n) {
      try {
        if (item.type === 'file')        fs.close(item.fd, () => {});
        else if (item.type === 'socket') item.socket.destroy();
        else if (item.type === 'server') item.server.close();
        else if (item.type === 'pipe_r') item.stream.destroy();
        else if (item.type === 'pipe_w') item.stream.destroy();
        else if (item.type === 'dirscan' && item._dir) item._dir.close().catch(() => {});
      } catch (_) { /* best effort */ }
    }
  }

  // ── view helper ────────────────────────────────────────────────────────────
  get view() { return new DataView(this.memory.buffer); }

  getGuestSlice(ptr, len) {
    return new Uint8Array(this.memory.buffer, ptr, len);
  }

  // ── op management ───────────────────────────────────────────────────────────
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
    // Also remove from signal waiters
    for (const [signum, w] of this.signalWaiters) {
      if (w.ovPtr === ovPtr) { this.signalWaiters.delete(signum); break; }
    }
  }

  hasPending() {
    return this.pendingCount > 0;
  }

  // Enqueue a completion callback to run before the next run() call.
  enqueue(fn) {
    this.completionQueue.push(fn);
  }

  // Drain all enqueued completions.
  drainQueue() {
    while (this.completionQueue.length > 0) {
      const fn = this.completionQueue.shift();
      fn();
    }
  }

  // ── signals ─────────────────────────────────────────────────────────────────
  _setupSignals() {
    const deliver = (signum) => {
      const w = this.signalWaiters.get(signum);
      if (w) {
        this.signalWaiters.delete(signum);
        this.enqueue(() => {
          if (this.isOpActive(w.ovPtr, w.opId)) {
            this.activeOps.delete(w.ovPtr);
            this.pendingCount--;
            writeOverlapped(this.view, w.ovPtr, 0, 0, BigInt(signum));
          }
        });
      }
    };
    try { process.on('SIGINT',  () => deliver(2));  } catch (_) {}
    try { process.on('SIGTERM', () => deliver(15)); } catch (_) {}
    // SIGWINCH = 27 — emit if terminal resizes
    if (process.stdout.isTTY) {
      try {
        process.stdout.on('resize', () => deliver(27));
      } catch (_) {}
    }
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// makeBrainImports — builds the "env" import object
// ─────────────────────────────────────────────────────────────────────────────
export function makeBrainImports(hostState, argv) {
  const hs = hostState;

  // Helper: write bytes to guest memory
  function guestWrite(ptr, buf) {
    new Uint8Array(hs.memory.buffer, ptr, buf.length).set(buf);
  }

  // ── get_time ────────────────────────────────────────────────────────────────
  // Returns: i64 nanoseconds
  function get_time() {
    return BigInt(Date.now()) * 1_000_000n;
  }

  // ── host_panic ──────────────────────────────────────────────────────────────
  function host_panic(ptr, len) {
    const msg = Buffer.from(hs.getGuestSlice(ptr, len)).toString();
    console.error('GUEST PANIC:', msg);
    process.exit(1);
  }

  // ── get_random ──────────────────────────────────────────────────────────────
  function get_random(ptr, len) {
    const buf = hs.getGuestSlice(ptr, len);
    crypto.getRandomValues(buf);
  }

  // ── get_args ─────────────────────────────────────────────────────────────────
  // Returns: i64  (count << 32) | totalBytes
  function get_args(ptr, len) {
    const args = argv;
    const encoded = args.map(a => Buffer.concat([Buffer.from(a), Buffer.from([0])]));
    const totalBytes = encoded.reduce((s, b) => s + b.length, 0);
    const count = BigInt(args.length);
    if (ptr !== 0 && len >= totalBytes) {
      let off = ptr;
      for (const b of encoded) { guestWrite(off, b); off += b.length; }
    }
    return (count << 32n) | BigInt(totalBytes);
  }

  // ── get_env ──────────────────────────────────────────────────────────────────
  // Returns: i64  (count << 32) | totalBytes
  function get_env(ptr, len) {
    const vars = Object.entries(process.env).map(([k, v]) => Buffer.concat([Buffer.from(`${k}=${v}`), Buffer.from([0])]));
    const totalBytes = vars.reduce((s, b) => s + b.length, 0);
    const count = BigInt(vars.length);
    if (ptr !== 0 && len >= totalBytes) {
      let off = ptr;
      for (const b of vars) { guestWrite(off, b); off += b.length; }
    }
    return (count << 32n) | BigInt(totalBytes);
  }

  // ── get_cwd ──────────────────────────────────────────────────────────────────
  // Returns: i64  (0 << 32) | bytesNeeded   (high 32 = errno on failure)
  function get_cwd(ptr, len) {
    const cwd = process.cwd();
    const bytes = Buffer.byteLength(cwd);
    if (ptr !== 0 && len >= bytes) guestWrite(ptr, Buffer.from(cwd));
    return BigInt(bytes);
  }

  // ── set_cwd ──────────────────────────────────────────────────────────────────
  // Returns: i32 errno
  function set_cwd(ptr, len) {
    const path = Buffer.from(hs.getGuestSlice(ptr, len)).toString();
    try { process.chdir(path); return 0; }
    catch (e) { return mapErrno(e); }
  }

  // ── timer_set ────────────────────────────────────────────────────────────────
  function timer_set(ovPtr, delayMs) {
    const opId = hs.registerOp(ovPtr);
    const id = setTimeout(() => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, 0));
    }, Number(delayMs));
    // store timer id on hs for cancel
    hs._timers = hs._timers || new Map();
    hs._timers.set(ovPtr, id);
  }

  // ── cancel ───────────────────────────────────────────────────────────────────
  function cancel(ovPtr) {
    if (hs._timers && hs._timers.has(ovPtr)) {
      clearTimeout(hs._timers.get(ovPtr));
      hs._timers.delete(ovPtr);
    }
    hs.cancelOp(ovPtr);
  }

  // ── read ─────────────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, handle i64, bufferPtr i32, bufferLen i32)
  function read(ovPtr, handle, bufferPtr, bufferLen) {
    const h = hs.getHandle(handle);
    if (!h) { writeOverlapped(hs.view, ovPtr, 9, 0, 0); return; }

    const bLen = Number(bufferLen);
    const bPtr = Number(bufferPtr);
    const opId = hs.registerOp(ovPtr);

    if (handle === 0n || h.type === 'stdio') {
      // stdin — use readable event for async
      const stream = process.stdin;
      const onData = (chunk) => {
        cleanup();
        const n = Math.min(chunk.length, bLen);
        const slice = chunk.slice(0, n);
        hs.enqueue(() => {
          if (!hs.isOpActive(ovPtr, opId)) return;
          guestWrite(bPtr, slice);
          hs.completeOp(ovPtr, opId, 0, 0, n);
        });
      };
      const onEnd = () => {
        cleanup();
        hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, 0));
      };
      const onError = (e) => {
        cleanup();
        hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
      };
      const cleanup = () => {
        stream.removeListener('data', onData);
        stream.removeListener('end', onEnd);
        stream.removeListener('error', onError);
      };
      stream.once('data', onData);
      stream.once('end', onEnd);
      stream.once('error', onError);
      return;
    }

    if (h.type === 'file') {
      const tmp = Buffer.allocUnsafe(bLen);
      fs.read(h.fd, tmp, 0, bLen, null, (err, n) => {
        hs.enqueue(() => {
          if (!hs.isOpActive(ovPtr, opId)) return;
          if (err) { hs.completeOp(ovPtr, opId, mapErrno(err), 0, 0); return; }
          guestWrite(bPtr, tmp.slice(0, n));
          hs.completeOp(ovPtr, opId, 0, 0, n);
        });
      });
      return;
    }

    if (h.type === 'socket' || h.type === 'pipe_r') {
      const stream = h.socket || h.stream;
      const onData = (chunk) => {
        cleanup();
        const n = Math.min(chunk.length, bLen);
        const slice = chunk.slice(0, n);
        hs.enqueue(() => {
          if (!hs.isOpActive(ovPtr, opId)) return;
          guestWrite(bPtr, slice);
          hs.completeOp(ovPtr, opId, 0, 0, n);
        });
      };
      const onEnd = () => {
        cleanup();
        hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, 0));
      };
      const onError = (e) => {
        cleanup();
        hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
      };
      const cleanup = () => {
        stream.removeListener('data', onData);
        stream.removeListener('end', onEnd);
        stream.removeListener('error', onError);
      };
      stream.once('data', onData);
      stream.once('end', onEnd);
      stream.once('error', onError);
      return;
    }

    // Unreadable handle type
    hs.activeOps.delete(ovPtr); hs.pendingCount--;
    writeOverlapped(hs.view, ovPtr, 9, 0, 0);
  }

  // ── write ────────────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, handle i64, bufferPtr i32, bufferLen i32)
  function write(ovPtr, handle, bufferPtr, bufferLen) {
    const bLen = Number(bufferLen);
    const bPtr = Number(bufferPtr);
    const dataCopy = Buffer.from(hs.getGuestSlice(bPtr, bLen));

    // stdout / stderr — synchronous, no op registration needed
    if (handle === 1n || handle === 2n) {
      const stream = handle === 1n ? process.stdout : process.stderr;
      stream.write(dataCopy);
      writeOverlapped(hs.view, ovPtr, 0, 0, bLen);
      return;
    }

    const h = hs.getHandle(handle);
    if (!h) { writeOverlapped(hs.view, ovPtr, 9, 0, 0); return; }

    const opId = hs.registerOp(ovPtr);

    if (h.type === 'file') {
      fs.write(h.fd, dataCopy, 0, dataCopy.length, null, (err, n) => {
        hs.enqueue(() => {
          if (!hs.isOpActive(ovPtr, opId)) return;
          hs.completeOp(ovPtr, opId, err ? mapErrno(err) : 0, 0, err ? 0 : n);
        });
      });
      return;
    }

    if (h.type === 'socket' || h.type === 'pipe_w') {
      const stream = h.socket || h.stream;
      stream.write(dataCopy, (err) => {
        hs.enqueue(() => {
          if (!hs.isOpActive(ovPtr, opId)) return;
          hs.completeOp(ovPtr, opId, err ? mapErrno(err) : 0, 0, err ? 0 : dataCopy.length);
        });
      });
      return;
    }

    hs.activeOps.delete(ovPtr); hs.pendingCount--;
    writeOverlapped(hs.view, ovPtr, 9, 0, 0);
  }

  // ── handle_close ─────────────────────────────────────────────────────────────
  // Signature: (handle i64)
  function handle_close(handle) {
    hs.freeHandle(handle);
  }

  // ── path_open ────────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, pathPtr i32, pathLen i32, flags i32)
  // flags: O_RDONLY=0, O_WRONLY=1, O_RDWR=2, O_CREAT=64, O_EXCL=128, O_TRUNC=512, O_APPEND=1024
  function path_open(ovPtr, pathPtr, pathLen, flags) {
    const rawPath = Buffer.from(hs.getGuestSlice(Number(pathPtr), Number(pathLen))).toString();
    const pathStr  = translatePath(rawPath);
    const iFlags   = Number(flags);
    const access   = iFlags & 3;
    const isCreate = (iFlags & 64)   !== 0;
    const isExcl   = (iFlags & 128)  !== 0;
    const isTrunc  = (iFlags & 512)  !== 0;
    const isAppend = (iFlags & 1024) !== 0;

    let nodeFlags;
    if (isCreate) {
      if (isExcl)          nodeFlags = (access === 2) ? 'wx+' : 'wx';
      else if (isTrunc)    nodeFlags = (access === 2) ? 'w+'  : 'w';
      else if (isAppend)   nodeFlags = (access === 2) ? 'a+'  : 'a';
      else                 nodeFlags = (access === 2) ? 'a+'  : (access === 1 ? 'a' : 'r+');
    } else {
      if (isTrunc)         nodeFlags = (access === 2) ? 'w+'  : 'w';
      else if (isAppend)   nodeFlags = (access === 2) ? 'a+'  : 'a';
      else if (access === 2) nodeFlags = 'r+'; // O_RDWR existing file
      else if (access === 1) nodeFlags = 'r+'; // O_WRONLY existing file (r+ is write-capable superset)
      else                   nodeFlags = 'r';  // O_RDONLY
    }

    const opId = hs.registerOp(ovPtr);
    fs.open(pathStr, nodeFlags, 0o666, (err, fd) => {
      hs.enqueue(() => {
        if (!hs.isOpActive(ovPtr, opId)) {
          if (!err) fs.close(fd, () => {});
          return;
        }
        if (err) { hs.completeOp(ovPtr, opId, mapErrno(err), 0, 0); return; }
        const h = hs.allocHandle({ type: 'file', fd, path: pathStr });
        hs.completeOp(ovPtr, opId, 0, 0, h);
      });
    });
  }

  // ── dir_read ─────────────────────────────────────────────────────────────────
  // Encoding per entry: [1 byte kind][N bytes name][0 byte terminator]
  // Returns: resultExt = bytes copied
  function dir_read(ovPtr, handle, bufferPtr, bufferLen) {
    const bLen = Number(bufferLen);
    const bPtr = Number(bufferPtr);
    const h = hs.getHandle(handle);
    if (!h) { writeOverlapped(hs.view, ovPtr, 9, 0, 0); return; }

    const opId = hs.registerOp(ovPtr);

    // Upgrade plain file handle to dirscan on first dir_read
    let scan;
    if (h.type === 'file') {
      scan = { type: 'dirscan', fd: h.fd, path: h.path, leftovers: Buffer.alloc(0), done: false };
      // Replace handle in map
      const bh = typeof handle === 'bigint' ? handle : BigInt(handle);
      hs.handles.set(bh, scan);
    } else if (h.type === 'dirscan') {
      scan = h;
    } else {
      hs.activeOps.delete(ovPtr); hs.pendingCount--;
      writeOverlapped(hs.view, ovPtr, 9, 0, 0); return;
    }

    const fillAndDeliver = async () => {
      while (scan.leftovers.length < bLen && !scan.done) {
        let entries;
        try {
          // Read 32 entries at a time
          if (!scan._dir) scan._dir = await fs.promises.opendir(scan.path);
          const batch = [];
          for (let i = 0; i < 32; i++) {
            const dirent = await scan._dir.read();
            if (!dirent) { scan.done = true; break; }
            if (dirent.name === '.' || dirent.name === '..') continue;
            batch.push(dirent);
          }
          entries = batch;
        } catch (e) {
          hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
          return;
        }
        for (const de of entries) {
          let kind = STAT_KIND_UNKNOWN;
          if (de.isDirectory())      kind = STAT_KIND_DIR;
          else if (de.isSymbolicLink()) kind = STAT_KIND_SYMLINK;
          else                        kind = STAT_KIND_FILE;
          const nameBuf = Buffer.from(de.name);
          const entry = Buffer.concat([Buffer.from([kind]), nameBuf, Buffer.from([0])]);
          scan.leftovers = Buffer.concat([scan.leftovers, entry]);
        }
        if (entries.length === 0) { scan.done = true; break; }
      }

      const toCopy = Math.min(scan.leftovers.length, bLen);
      const payload = scan.leftovers.slice(0, toCopy);
      scan.leftovers = scan.leftovers.slice(toCopy);
      hs.enqueue(() => {
        if (!hs.isOpActive(ovPtr, opId)) return;
        if (toCopy > 0) guestWrite(bPtr, payload);
        hs.completeOp(ovPtr, opId, 0, 0, toCopy);
      });
    };

    fillAndDeliver().catch((e) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
    });
  }

  // ── path_stat ────────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, pathPtr i32, pathLen i32, flags i32, outPtr i32, outLen i32)
  // flags bit 0 = STAT_FLAG_NO_FOLLOW (use lstat)
  function path_stat(ovPtr, pathPtr, pathLen, flags, outPtr, outLen) {
    const pathStr = translatePath(Buffer.from(hs.getGuestSlice(Number(pathPtr), Number(pathLen))).toString());
    const noFollow = (Number(flags) & 1) !== 0;
    const opId = hs.registerOp(ovPtr);

    const statFn = noFollow ? fs.promises.lstat : fs.promises.stat;
    statFn(pathStr).then((st) => {
      hs.enqueue(() => {
        if (!hs.isOpActive(ovPtr, opId)) return;
        if (Number(outLen) < 64) { hs.completeOp(ovPtr, opId, 34, 0, 64); return; }
        guestWrite(Number(outPtr), marshalStat(st));
        hs.completeOp(ovPtr, opId, 0, 0, 64);
      });
    }).catch((e) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
    });
  }

  // ── path_chmod ───────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, pathPtr i32, pathLen i32, mode i32)
  // Go impl is synchronous for chmod; mirror that.
  function path_chmod(ovPtr, pathPtr, pathLen, mode) {
    const pathStr = translatePath(Buffer.from(hs.getGuestSlice(Number(pathPtr), Number(pathLen))).toString());
    const opId = hs.registerOp(ovPtr);
    fs.chmod(pathStr, Number(mode), (err) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(err), 0, 0));
    });
  }

  // ── path_remove ──────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, pathPtr i32, pathLen i32)
  // Go impl uses os.Remove (non-recursive). Mirror that.
  function path_remove(ovPtr, pathPtr, pathLen) {
    const pathStr = translatePath(Buffer.from(hs.getGuestSlice(Number(pathPtr), Number(pathLen))).toString());
    const opId = hs.registerOp(ovPtr);
    fs.rm(pathStr, { recursive: false }, (err) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(err), 0, 0));
    });
  }

  // ── path_mkdir ───────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, pathPtr i32, pathLen i32, mode i32)
  function path_mkdir(ovPtr, pathPtr, pathLen, mode) {
    const pathStr = translatePath(Buffer.from(hs.getGuestSlice(Number(pathPtr), Number(pathLen))).toString());
    const opId = hs.registerOp(ovPtr);
    fs.mkdir(pathStr, { mode: Number(mode) }, (err) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(err), 0, 0));
    });
  }

  // ── path_rename ──────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, oldPtr i32, oldLen i32, newPtr i32, newLen i32)
  function path_rename(ovPtr, oldPtr, oldLen, newPtr, newLen) {
    const oldStr = translatePath(Buffer.from(hs.getGuestSlice(Number(oldPtr), Number(oldLen))).toString());
    const newStr = translatePath(Buffer.from(hs.getGuestSlice(Number(newPtr), Number(newLen))).toString());
    const opId = hs.registerOp(ovPtr);
    fs.rename(oldStr, newStr, (err) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(err), 0, 0));
    });
  }

  // ── net_open ─────────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, addrPtr i32, addrLen i32, port i32, flags i32)
  // flags bit 0 = connect (1) vs listen (0)
  function net_open(ovPtr, addrPtr, addrLen, port, flags) {
    const addr      = Buffer.from(hs.getGuestSlice(Number(addrPtr), Number(addrLen))).toString();
    const iPort     = Number(port);
    const isConnect = (Number(flags) & 1) !== 0;
    const opId = hs.registerOp(ovPtr);

    if (isConnect) {
      const socket = net.createConnection({ host: addr, port: iPort });
      socket.once('connect', () => {
        const h = hs.allocHandle({ type: 'socket', socket });
        hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, h));
      });
      socket.once('error', (e) => {
        hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
      });
    } else {
      const server = net.createServer();
      server.once('error', (e) => {
        hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
      });
      server.listen(iPort, addr, () => {
        const h = hs.allocHandle({ type: 'server', server });
        hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, h));
      });
    }
  }

  // ── net_accept ───────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, listenHandle i64)
  function net_accept(ovPtr, listenHandle) {
    const h = hs.getHandle(listenHandle);
    if (!h || h.type !== 'server') { writeOverlapped(hs.view, ovPtr, 22, 0, 0); return; }
    const opId = hs.registerOp(ovPtr);
    h.server.once('connection', (socket) => {
      const sh = hs.allocHandle({ type: 'socket', socket });
      hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, sh));
    });
    h.server.once('error', (e) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
    });
  }

  // ── process_spawn ────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, cfgPtr i32, cfgLen i32)
  // Wire protocol: NUL-separated bytes.  Four sections separated by empty tokens:
  //   section 0: [program, arg1, arg2, ...]
  //   section 1: env overrides ("KEY=VALUE")
  //   section 2: cwd (first non-empty token)
  //   section 3: stdio specs ("inherit"|"pipe"|"null"|"fd:<handle>")
  function process_spawn(ovPtr, cfgPtr, cfgLen) {
    const cfg   = Buffer.from(hs.getGuestSlice(Number(cfgPtr), Number(cfgLen)));
    const opId  = hs.registerOp(ovPtr);

    // Parse
    const parts = [];
    let start = 0;
    for (let i = 0; i < cfg.length; i++) {
      if (cfg[i] === 0) { parts.push(cfg.slice(start, i)); start = i + 1; }
    }
    if (start < cfg.length) parts.push(cfg.slice(start));

    if (parts.length === 0 || parts[0].length === 0) {
      hs.completeOp(ovPtr, opId, 22, 0, 0); return;
    }

    const program = translatePath(parts[0].toString());
    const sections = [[], [], [], []];
    let secIdx = 0;
    for (let i = 1; i < parts.length; i++) {
      if (parts[i].length === 0) { if (++secIdx > 3) break; continue; }
      sections[secIdx].push(parts[i].toString());
    }

    const spawnArgs   = sections[0];
    const envOverrides = sections[1];
    const cwd         = sections[2][0] ? translatePath(sections[2][0]) : process.cwd();
    const stdioSpecs  = sections[3];

    // Merge env
    const envObj = { ...process.env };
    for (const kv of envOverrides) {
      const eq = kv.indexOf('=');
      if (eq > 0) envObj[kv.slice(0, eq)] = kv.slice(eq + 1);
    }

    // Set up stdio
    const pipePairs = [];  // {rh, wh, rStream, wStream}
    const stdioArr  = [];
    for (let i = 0; i < stdioSpecs.length; i++) {
      const spec = stdioSpecs[i];
      if (spec === 'inherit') {
        stdioArr.push(i === 0 ? process.stdin : i === 1 ? process.stdout : process.stderr);
      } else if (spec === 'pipe') {
        const p = new PassThrough();
        // parent side: for stdin slot, parent writes; for stdout/stderr, parent reads
        if (i === 0) {
          const wh = hs.allocHandle({ type: 'pipe_w', stream: p });
          pipePairs.push(wh);
          stdioArr.push(p);           // child reads from p
        } else {
          const rh = hs.allocHandle({ type: 'pipe_r', stream: p });
          pipePairs.push(rh);
          stdioArr.push(p);           // child writes to p
        }
      } else if (spec === 'null') {
        stdioArr.push('ignore');
      } else if (spec.startsWith('fd:')) {
        const fh = BigInt(spec.slice(3));
        const fhItem = hs.getHandle(fh);
        if (fhItem && fhItem.type === 'file') stdioArr.push(fs.createReadStream(fhItem.path));
        else stdioArr.push('ignore');
      } else {
        stdioArr.push('ignore');
      }
    }
    if (stdioArr.length === 0) {
      stdioArr.push(process.stdin, process.stdout, process.stderr);
    }

    // Pack pipe handles into reserved (up to 4, 16 bits each)
    let pipePackedRes = 0n;
    for (let j = 0; j < pipePairs.length && j < 4; j++) {
      pipePackedRes |= (pipePairs[j] & 0xFFFFn) << BigInt(j * 16);
    }

    let cp;
    try {
      cp = child_process.spawn(program, spawnArgs, {
        cwd,
        env:   envObj,
        stdio: stdioArr.map(s => typeof s === 'string' ? s : 'pipe'),
      });
      // Wire up passthrough pipes to child stdio streams
      for (let i = 0; i < stdioArr.length; i++) {
        if (typeof stdioArr[i] !== 'string') {
          if (i === 0 && cp.stdin)   stdioArr[i].pipe(cp.stdin);
          else if (cp.stdio[i])      cp.stdio[i].pipe(stdioArr[i]);
        }
      }
    } catch (e) {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
      return;
    }

    const ch = hs.allocHandle({ type: 'child', cp });
    // resultExt = (pipeHandles << 32) | childHandle
    const extResult = ch | (pipePackedRes << 32n);
    hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, extResult));
  }

  // ── process_pipe ─────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32)
  // Creates a pipe: returns resultExt = (writeHandle << 32) | readHandle
  function process_pipe(ovPtr) {
    const r = new PassThrough();
    const w = new PassThrough();
    w.pipe(r);
    const rh = hs.allocHandle({ type: 'pipe_r', stream: r });
    const wh = hs.allocHandle({ type: 'pipe_w', stream: w });
    writeOverlapped(hs.view, ovPtr, 0, 0, (wh << 32n) | rh);
  }

  // ── process_wait ─────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, handle i64)
  // Returns resultExt = (exitCode << 32) | exitCode
  function process_wait(ovPtr, handle) {
    const h = hs.getHandle(handle);
    if (!h || h.type !== 'child') { writeOverlapped(hs.view, ovPtr, 9, 0, 0); return; }
    const opId = hs.registerOp(ovPtr);
    h.cp.once('exit', (code, signal) => {
      const exitCode = code != null ? code : (signal ? 1 : 0);
      const ec = BigInt(exitCode >>> 0);
      hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, (ec << 32n) | ec));
    });
  }

  // ── process_signal ───────────────────────────────────────────────────────────
  // Signature: (handle i64, signum i32)
  function process_signal(handle, signum) {
    const h = hs.getHandle(handle);
    if (!h || h.type !== 'child') return;
    const sigMap = { 2: 'SIGINT', 9: 'SIGKILL', 15: 'SIGTERM' };
    const sig = sigMap[Number(signum)];
    if (sig) try { h.cp.kill(sig); } catch (_) {}
  }

  // ── signal_wait ──────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, signum i32)
  function signal_wait(ovPtr, signum) {
    const sn = Number(signum);
    // Cancel any existing waiter for this signal
    const existing = hs.signalWaiters.get(sn);
    if (existing) {
      hs.cancelOp(existing.ovPtr);
      hs.signalWaiters.delete(sn);
    }
    const opId = hs.registerOp(ovPtr);
    hs.signalWaiters.set(sn, { ovPtr, opId });
  }

  // ── process_exit ─────────────────────────────────────────────────────────────
  // Signature: (code i32)
  function process_exit(code) {
    process.exit(Number(code));
  }

  // ── tty_set_mode ─────────────────────────────────────────────────────────────
  // Signature: (handle i64, mode i32)
  // mode 1 = raw, 0 = normal
  function tty_set_mode(handle, mode) {
    if (handle === 0n && process.stdin.isTTY) {
      try {
        if (Number(mode) === 1) process.stdin.setRawMode(true);
        else                    process.stdin.setRawMode(false);
      } catch (_) {}
    }
  }

  // ── tty_get_size ─────────────────────────────────────────────────────────────
  // Signature: (handle i64) -> i32  (width << 16) | height
  function tty_get_size(handle) {
    if ((handle === 1n || handle === 2n) && process.stdout.isTTY) {
      const w = process.stdout.columns || 80;
      const h = process.stdout.rows    || 24;
      return (w << 16) | h;
    }
    return (80 << 16) | 24;
  }

  // ── fd_isatty ────────────────────────────────────────────────────────────────
  // Signature: (fd i32) -> i32
  function fd_isatty(fd) {
    if (fd === 0) return process.stdin.isTTY  ? 1 : 0;
    if (fd === 1) return process.stdout.isTTY ? 1 : 0;
    if (fd === 2) return process.stderr.isTTY ? 1 : 0;
    return 0;
  }

  // ── get_platform_info ────────────────────────────────────────────────────────
  // Signature: (ptr i32, len i32) -> i32 errno
  // 548-byte binary struct — mirrors env_utils.go layout
  function get_platform_info(ptr, maxLen) {
    const structSize = 548;
    if (Number(maxLen) < structSize) return 7; // E2BIG

    const buf = Buffer.alloc(structSize);

    // Flags (bit 0: case-sensitive FS)
    const isWindows = process.platform === 'win32';
    const isDarwin  = process.platform === 'darwin';
    const caseSens  = (!isWindows && !isDarwin) ? 1 : 0;
    buf.writeUInt32LE(caseSens, 0);

    // Path separators
    buf[4] = isWindows ? 0x5C : 0x2F;  // '\' or '/'
    buf[5] = isWindows ? 0x3B : 0x3A;  // ';' or ':'

    // OS Kind: 1=Windows, 2=Linux, 3=Darwin
    const osKind = isWindows ? 1 : (isDarwin ? 3 : 2);
    buf.writeUInt16LE(osKind, 6);

    // CPU type: 1=x86_64, 2=arm64
    const arch = process.arch;
    const cpuType = arch === 'x64' ? 1 : (arch === 'arm64' ? 2 : 0);
    buf.writeUInt16LE(cpuType, 16);
    buf[18] = 64; // bitness

    const copySafe = (offset, s) => {
      const b = Buffer.from(s);
      b.copy(buf, offset, 0, Math.min(b.length, 63));
    };

    copySafe(20,  process.platform);
    copySafe(84,  'node');
    copySafe(156, process.version);
    copySafe(220, 'washmhost-node');
    copySafe(292, '0.0.0-dev');
    copySafe(356, '0.0.0-dev');
    copySafe(420, new Date().toISOString().slice(0, 19));
    copySafe(484, `${process.platform}-${arch}`);

    guestWrite(Number(ptr), buf);
    return 0;
  }

  // ── rusticated_debug ─────────────────────────────────────────────────────────
  function rusticated_debug(val) {
    L('GUEST DEBUG:', Number(val), '(0x' + Number(val).toString(16) + ')');
  }

  const env = {
    get_time,
    host_panic,
    get_random,
    get_args,
    get_env,
    get_cwd,
    set_cwd,
    timer_set,
    cancel,
    read,
    write,
    handle_close,
    path_open,
    dir_read,
    path_stat,
    path_chmod,
    path_remove,
    path_mkdir,
    path_rename,
    net_open,
    net_accept,
    process_spawn,
    process_pipe,
    process_wait,
    process_signal,
    signal_wait,
    process_exit,
    tty_set_mode,
    tty_get_size,
    fd_isatty,
    get_platform_info,
    rusticated_debug,
  };

  // Aliases used by some builds
  env.proc_spawn        = env.process_spawn;
  env.process_kill      = env.process_signal;
  env.get_pid           = () => 1234n;
  env.process_get_pid   = env.get_pid;
  env.fs_chmod          = env.path_chmod;
  env.fs_remove         = env.path_remove;
  env.fs_mkdir          = env.path_mkdir;

  return { env };
}

// ─────────────────────────────────────────────────────────────────────────────
// runHost — instantiates and drives the WASM brain
// ─────────────────────────────────────────────────────────────────────────────
export async function runHost(brainWasm, argv) {
  const hostState = new HostState();
  const imports   = makeBrainImports(hostState, argv);

  L('[host] instantiating brain wasm...');
  const { instance } = await WebAssembly.instantiate(brainWasm, imports);
  L('[host] brain wasm exports:', Object.keys(instance.exports));

  hostState.memory = instance.exports.memory;

  const runFunc = instance.exports.run;
  if (!runFunc) {
    console.error('[mohabbat] WASM module missing "run" export');
    process.exit(1);
  }

  L('[host] starting event loop');
  // Event loop: mirrors runtime.go RunWasm
  // Structure: drain → run → if sync completions continue, else await.
  // The second drainQueue is GONE — only one drain at the top ensures that
  // synchronously-enqueued completions (e.g. dir_read EOF) are not delivered
  // BEFORE run() gets to see FLAG_COMPLETED in WASM memory.
  while (true) {
    // Drain any pending completions into WASM memory first
    hostState.drainQueue();

    // Call run(); return value is i32 prospective exit code
    let result;
    try {
      result = runFunc();
    } catch (e) {
      console.error('[host] run() threw:', e);
      process.exit(1);
    }
    const retVal = typeof result === 'number' ? result : Number(result ?? 0);

    // Check forced exit (process_exit called inside run)
    if (hostState.forcedExitCode !== -1) {
      process.exit(hostState.forcedExitCode);
    }

    // If run() synchronously enqueued completions (e.g. dir_read EOF with
    // scan.done=true), deliver them on the next iteration via drainQueue.
    // This mirrors Go's tight runFunc loop where Poll() is only called when
    // there are genuinely no ready completions.
    if (hostState.completionQueue.length > 0) continue;

    // If no outstanding ops, we are done
    if (!hostState.hasPending()) {
      const code = (retVal === -1) ? 0 : retVal;
      process.exit(code);
    }

    // Wait for at least one completion to be enqueued
    await new Promise((resolve) => {
      const check = () => {
        if (hostState.completionQueue.length > 0) {
          resolve();
        } else {
          setImmediate(check);
        }
      };
      setImmediate(check);
    });
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// CLI mode: node index.js <brain.wasm> [args...]
// ─────────────────────────────────────────────────────────────────────────────
const isMain = process.argv[1] && (
  import.meta.url.startsWith('file:') &&
  fileURLToPath(import.meta.url) === fs.realpathSync(process.argv[1])
);
if (isMain) {
  const brainPath = process.argv[2];
  if (!brainPath) {
    console.error('Usage: node index.js <brain.wasm> [args...]');
    process.exit(1);
  }
  const brainWasm = fs.readFileSync(brainPath);
  const argv = process.argv.slice(2); // argv[0] = wasm path, matches washmhost convention
  runHost(brainWasm, argv).catch((err) => {
    console.error(err);
    process.exit(1);
  });
}

export { HostState };
