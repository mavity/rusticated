import fs from 'node:fs';
import net from 'node:net';
import dns from 'node:dns';
import tls from 'node:tls';
import child_process from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { PassThrough } from 'node:stream';
import crypto from 'node:crypto';
import { createRequire } from 'node:module';
import process from 'node:process';
import os from 'node:os';
import path from 'node:path';

// Patched by buildNodeSlot:
const VERBOSE = false; // {{VERBOSE}}
const L = (...args) => (VERBOSE || process.env.MOHABBAT_VERBOSE) && console.error(...args);

// Overlapped.flags: bit 0 is completion, bits 1-23 are shared-reserved, and
// bits 24-31 are operation-specific.
const FLAG_COMPLETED = 1;
// This request flag selects the read-specific u64 offset at ovPtr + 24.
const READ_FLAG_POSITIONAL = 1 << 24;

let cachedRootCerts = null;
function getRootCerts() {
  if (cachedRootCerts) return cachedRootCerts;
  cachedRootCerts = tls.rootCertificates.map(pem => {
    try { return new crypto.X509Certificate(pem); }
    catch (e) { return null; }
  }).filter(c => c !== null);
  return cachedRootCerts;
}

// ─────────────────────────────────────────────────────────────────────────────
// WASI (wasip1) errno mapping.
// Our ABI mandates that all system handlers return these values to the guest.
// These differ significantly from Linux/Darwin host errnos.
// ─────────────────────────────────────────────────────────────────────────────
const wasiSuccess      = 0;
const wasiE2BIG        = 1;
const wasiEACCES       = 2;
const wasiEADDRINUSE   = 3;
const wasiEADDRNOTAVAIL = 4;
const wasiEAFNOSUPPORT = 5;
const wasiEAGAIN       = 6;
const wasiEALREADY     = 7;
const wasiEBADF        = 8;
const wasiEBADMSG      = 9;
const wasiEBUSY        = 10;
const wasiECANCELED    = 11;
const wasiECHILD       = 12;
const wasiECONNABORTED = 13;
const wasiECONNREFUSED = 14;
const wasiECONNRESET   = 15;
const wasiEDEADLK      = 16;
const wasiEDESTADDRREQ = 17;
const wasiEDOM         = 18;
const wasiEDQUOT       = 19;
const wasiEEXIST       = 20;
const wasiEFAULT       = 21;
const wasiEFBIG        = 22;
const wasiEHOSTUNREACH = 23;
const wasiEIDRM        = 24;
const wasiEILSEQ       = 25;
const wasiEINPROGRESS  = 26;
const wasiEINTR        = 27;
const wasiEINVAL       = 28;
const wasiEIO          = 29;
const wasiEISCONN      = 30;
const wasiEISDIR       = 31;
const wasiELOOP        = 32;
const wasiEMFILE       = 33;
const wasiEMLINK       = 34;
const wasiEMSGSIZE     = 35;
const wasiEMULTIHOP    = 36;
const wasiENAMETOOLONG = 37;
const wasiENETDOWN     = 38;
const wasiENETRESET    = 39;
const wasiENETUNREACH  = 40;
const wasiENFILE       = 41;
const wasiENOBUFS      = 42;
const wasiENODEV       = 43;
const wasiENOENT       = 44;
const wasiENOEXEC      = 45;
const wasiENOLCK       = 46;
const wasiENOLINK      = 47;
const wasiENOMEM       = 48;
const wasiENOMSG       = 49;
const wasiENOPROTOOPT  = 50;
const wasiENOSPC       = 51;
const wasiENOSYS       = 52;
const wasiENOTCONN     = 53;
const wasiENOTDIR      = 54;
const wasiENOTEMPTY    = 55;
const wasiENOTRECOVERABLE = 56;
const wasiENOTSOCK     = 57;
const wasiENOTSUP      = 58;
const wasiENOTTY       = 59;
const wasiENXIO        = 60;
const wasiEOVERFLOW    = 61;
const wasiEOWNERDEAD   = 62;
const wasiEPERM        = 63;
const wasiEPIPE        = 64;
const wasiEPROTO       = 65;
const wasiEPROTONOSUPPORT = 66;
const wasiEPROTOTYPE   = 67;
const wasiERANGE       = 68;
const wasiEROFS        = 69;
const wasiESPIPE       = 70;
const wasiESRCH        = 71;
const wasiESTALE       = 72;
const wasiETIMEDOUT    = 73;
const wasiETXTBSY      = 74;
const wasiEXDEV        = 75;
const wasiENOTCAPABLE  = 76;

function mapErrno(e) {
  if (!e) return wasiSuccess;
  switch (e.code) {
    case 'ENOENT':    return wasiENOENT;
    case 'ESRCH':     return wasiESRCH;
    case 'EINTR':     return wasiEINTR;
    case 'EIO':       return wasiEIO;
    case 'ENXIO':     return wasiENXIO;
    case 'EBADF':     return wasiEBADF;
    case 'ECHILD':    return wasiECHILD;
    case 'EAGAIN':    return wasiEAGAIN;
    case 'ENOMEM':    return wasiENOMEM;
    case 'EACCES':    return wasiEACCES;
    case 'EFAULT':    return wasiEFAULT;
    case 'EBUSY':     return wasiEBUSY;
    case 'EEXIST':    return wasiEEXIST;
    case 'EFBIG':     return wasiEFBIG;
    case 'EXDEV':     return wasiEXDEV;
    case 'ENODEV':    return wasiENODEV;
    case 'ENOTDIR':   return wasiENOTDIR;
    case 'EISDIR':    return wasiEISDIR;
    case 'EPERM':     return wasiEPERM;
    case 'EINVAL':    return wasiEINVAL;
    case 'EMFILE':    return wasiEMFILE;
    case 'ENOSPC':    return wasiENOSPC;
    case 'ENOTSUP':
    case 'EOPNOTSUPP': return wasiENOTSUP;
    case 'EPIPE':     return wasiEPIPE;
    case 'ERANGE':    return wasiERANGE;
    case 'ENOTEMPTY': return wasiENOTEMPTY;
    case 'ECONNREFUSED': return wasiECONNREFUSED;
    case 'EADDRINUSE': return wasiEADDRINUSE;
    default:
      return wasiEIO;
  }
}

function createPtyAddonLoader(addonBytes) {
  let addon = null;
  const require = createRequire(process.execPath);
  return () => {
    if (addon) return addon;
    if (!addonBytes || addonBytes.length === 0) {
      throw new Error(`PTY addon is unavailable for ${process.platform}-${process.arch}`);
    }

    const digest = crypto.createHash('sha256').update(addonBytes).digest('hex');
    const addonPath = path.join(os.tmpdir(), `mohabbat-nodepty-${digest}.node`);
    try {
      const fd = fs.openSync(addonPath, 'wx', 0o700);
      try {
        fs.writeFileSync(fd, addonBytes);
      } catch (error) {
        fs.closeSync(fd);
        fs.rmSync(addonPath, { force: true });
        throw error;
      }
      fs.closeSync(fd);
    } catch (error) {
      if (error.code !== 'EEXIST') throw error;
      const stat = fs.lstatSync(addonPath);
      if (!stat.isFile() || stat.isSymbolicLink()) {
        throw new Error(`PTY addon cache path is not a regular file: ${addonPath}`);
      }
      const cachedBytes = fs.readFileSync(addonPath);
      if (!crypto.timingSafeEqual(
        crypto.createHash('sha256').update(cachedBytes).digest(),
        Buffer.from(digest, 'hex'),
      )) {
        throw new Error(`PTY addon cache integrity check failed: ${addonPath}`);
      }
    }
    addon = require(addonPath);
    return addon;
  };
}

function mapPtyError(code) {
  if (code === 0) return wasiSuccess;
  if (code === 22) return wasiEINVAL;
  if (process.platform === 'win32') {
    switch (code) {
      case 2:
      case 3: return wasiENOENT;
      case 5: return wasiEACCES;
      case 8:
      case 14: return wasiENOMEM;
      case 87: return wasiEINVAL;
      case 206: return wasiENAMETOOLONG;
      default: return wasiEIO;
    }
  }
  const errno = os.constants.errno;
  const name = Object.keys(errno).find(key => errno[key] === code);
  return name ? mapErrno({ code: name }) : wasiEIO;
}

async function resolvePtyProgram(program, env, cwd) {
  const searchPath = env.PATH ?? '/usr/bin:/bin';
  const candidates = program.includes('/')
    ? [path.resolve(cwd, program)]
    : searchPath.split(path.delimiter).map(entry => path.resolve(cwd, entry || '.', program));
  let denied = false;
  for (const candidate of candidates) {
    try {
      await fs.promises.access(candidate, fs.constants.X_OK);
      return candidate;
    } catch (error) {
      if (error.code === 'EACCES') denied = true;
      else if (error.code !== 'ENOENT' && error.code !== 'ENOTDIR') throw error;
    }
  }
  const error = new Error(`cannot find executable: ${program}`);
  error.code = denied ? 'EACCES' : 'ENOENT';
  throw error;
}

// ─────────────────────────────────────────────────────────────────────────────
// Path translation — only reverse the canonical guest-root encoding.
// The guest uses /C:/... while the host OS expects C:/... . Native host paths
// are left alone and temp/user/cwd paths are never rewritten.
// ─────────────────────────────────────────────────────────────────────────────
function translatePath(p) {
  if (typeof p !== 'string' || p.length === 0) return p;
  const s = p.replace(/\\/g, '/');
  if (/^\/[A-Za-z]:\//.test(s)) return s.slice(1);
  return s;
}

// ─────────────────────────────────────────────────────────────────────────────
// AbiStat marshalling (112 bytes, little-endian)
// Layout mirrors marshalAbiStat / createAbiStat in env_fs.go
// ─────────────────────────────────────────────────────────────────────────────
const STAT_KIND_UNKNOWN = 0;
const STAT_KIND_FILE    = 1;
const STAT_KIND_DIR     = 2;
const STAT_KIND_SYMLINK = 3;
const STAT_KIND_CHAR    = 4;
const STAT_KIND_BLOCK   = 5;
const STAT_KIND_FIFO    = 6;
const STAT_KIND_SOCKET  = 7;

const STAT_VALID_UID       = 1n << 0n;
const STAT_VALID_GID       = 1n << 1n;
const STAT_VALID_ATIME     = 1n << 2n;
const STAT_VALID_MTIME     = 1n << 3n;
const STAT_VALID_CTIME     = 1n << 4n;
const STAT_VALID_BIRTHTIME = 1n << 5n;
const STAT_VALID_NLINK     = 1n << 6n;
const STAT_VALID_INODE     = 1n << 7n;
const STAT_VALID_DEV       = 1n << 8n;
const STAT_VALID_RDEV      = 1n << 9n;
const STAT_VALID_BLKSIZE   = 1n << 10n;
const STAT_VALID_BLOCKS    = 1n << 11n;

export function marshalStat(st) {
  let kind = STAT_KIND_UNKNOWN;
  if (st.isFile())               kind = STAT_KIND_FILE;
  else if (st.isDirectory())     kind = STAT_KIND_DIR;
  else if (st.isSymbolicLink()) kind = STAT_KIND_SYMLINK;
  else if (st.isCharacterDevice()) kind = STAT_KIND_CHAR;
  else if (st.isBlockDevice())  kind = STAT_KIND_BLOCK;
  else if (st.isFIFO())         kind = STAT_KIND_FIFO;
  else if (st.isSocket())       kind = STAT_KIND_SOCKET;

  const mode = Number(st.mode & 0xffffffffn);
  const size = st.size;
  const blksize = st.blksize > 0n ? st.blksize : 4096n;
  const blocks = st.blocks > 0n || size === 0n ? st.blocks : (size + 511n) / 512n;
  let validMask = STAT_VALID_ATIME | STAT_VALID_MTIME | STAT_VALID_CTIME |
    STAT_VALID_BIRTHTIME | STAT_VALID_NLINK | STAT_VALID_INODE | STAT_VALID_DEV |
    STAT_VALID_RDEV | STAT_VALID_BLKSIZE | STAT_VALID_BLOCKS;
  if (process.platform !== 'win32') validMask |= STAT_VALID_UID | STAT_VALID_GID;
  const buf = Buffer.alloc(112);
  buf.writeUInt32LE(kind, 0);
  buf.writeUInt32LE(mode, 4);
  buf.writeUInt32LE(Number(st.uid & 0xffffffffn), 8);
  buf.writeUInt32LE(Number(st.gid & 0xffffffffn), 12);
  buf.writeBigUInt64LE(size, 16);
  buf.writeBigInt64LE(st.mtimeNs, 24);
  buf.writeBigInt64LE(st.atimeNs, 32);
  buf.writeBigInt64LE(st.ctimeNs, 40);
  buf.writeBigInt64LE(st.birthtimeNs, 48);
  buf.writeBigUInt64LE(st.nlink, 56);
  buf.writeBigUInt64LE(st.ino, 64);
  buf.writeBigUInt64LE(st.dev, 72);
  buf.writeBigUInt64LE(st.rdev, 80);
  buf.writeBigInt64LE(blksize, 88);
  buf.writeBigInt64LE(blocks, 96);
  buf.writeBigUInt64LE(validMask, 104);
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

function writeOverlappedExtended(view, ovPtr, errorCode, continued, resultExt, childHandle, ptyHandle) {
  writeOverlapped(view, ovPtr, errorCode, continued, resultExt);
  view.setBigUint64(ovPtr + 24, BigInt(childHandle), true);
  view.setBigUint64(ovPtr + 32, BigInt(ptyHandle), true);
  view.setBigUint64(ovPtr + 40, 0n, true);
  view.setBigUint64(ovPtr + 48, 0n, true);
  view.setBigUint64(ovPtr + 56, 0n, true);
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
    if (item.type === 'file') {
      item.position ??= 0;
      item.fileOperations ??= [];
      item.fileOperationActive ??= false;
    }
    item.references ??= 1;
    const h = this.nextHandle++;
    this.handles.set(h, item);
    return h;
  }

  getHandle(h) {
    return this.handles.get(typeof h === 'bigint' ? h : BigInt(h));
  }

  retainHandle(h) {
    const item = this.getHandle(h);
    if (!item) return null;
    item.references = (item.references ?? 1) + 1;
    return item;
  }

  freeHandle(h) {
    const bh = typeof h === 'bigint' ? h : BigInt(h);
    const item = this.handles.get(bh);
    if (item == null) return;
    this.handles.delete(bh);
    this.releaseHandle(item);
  }

  releaseHandle(item) {
    item.references = (item.references ?? 1) - 1;
    if (item.references > 0) return;
    // Close underlying resource if owned (handle >= 3)
    try {
      if (item.type === 'file')        fs.close(item.fd, () => {});
      else if (item.type === 'socket') {
        item.socket.destroy();
        if (item.leftovers) item.leftovers = null;
      }
      else if (item.type === 'server') item.server.close();
      else if (item.type === 'pipe_r') {
        item.stream.destroy();
        if (item.leftovers) item.leftovers = null;
      }
      else if (item.type === 'pipe_w') item.stream.end();
      else if (item.type === 'pty') {
        item.closed = true;
        item.stream.destroy();
        item.inputStream.end();
        try {
          const status = item.addon.closePty(item.nativeId);
          if (status !== 0) L('[pty] closePty failed:', status);
        } catch (error) {
          L('[pty] closePty threw:', error);
        }
      }
      else if (item.type === 'pty_child') {
        try {
          const status = item.addon.closeChild(item.nativeId);
          if (status !== 0) L('[pty] closeChild failed:', status);
        } catch (error) {
          L('[pty] closeChild threw:', error);
        }
      }
      else if (item.type === 'dirscan' && item._dir) item._dir.close().catch(() => {});
    } catch (_) { /* best effort */ }
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

  completeOpExtended(ovPtr, opId, errorCode, continued, resultExt, childHandle, ptyHandle) {
    if (!this.isOpActive(ovPtr, opId)) return false;
    this.activeOps.delete(ovPtr);
    this.pendingCount--;
    writeOverlappedExtended(this.view, ovPtr, errorCode, continued, resultExt, childHandle, ptyHandle);
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
export function makeBrainImports(hostState, argv, ptyAddonBytes = null) {
  const hs = hostState;
  const loadPtyAddon = createPtyAddonLoader(ptyAddonBytes);

  // Helper: write bytes to guest memory
  function guestWrite(ptr, buf) {
    new Uint8Array(hs.memory.buffer, ptr, buf.length).set(buf);
  }

  function queueFileOperation(handle, operation) {
    handle.fileOperations.push(operation);
    if (handle.fileOperationActive) return;

    const runNext = () => {
      const next = handle.fileOperations[0];
      if (!next) {
        handle.fileOperationActive = false;
        return;
      }

      handle.fileOperationActive = true;
      let completed = false;
      next(() => {
        if (completed) return;
        completed = true;
        handle.fileOperations.shift();
        runNext();
      });
    };

    runNext();
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
    const guestPath = Buffer.from(hs.getGuestSlice(ptr, len)).toString();
    const hostPath = translatePath(guestPath);
    try { process.chdir(hostPath); return 0; }
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
  // Signature is unchanged; positioned reads extend the Overlapped request.
  function read(ovPtr, handle, bufferPtr, bufferLen) {
    const ovPtrNum = Number(ovPtr) >>> 0;
    const view = hs.view;
    const requestFlags = view.getUint32(ovPtrNum, true);
    if ((requestFlags & ~READ_FLAG_POSITIONAL) !== 0) {
      writeOverlapped(view, ovPtrNum, wasiEINVAL, 0, 0);
      return;
    }

    const positioned = (requestFlags & READ_FLAG_POSITIONAL) !== 0;
    let position = null;
    if (positioned) {
      if (ovPtrNum + 32 > view.byteLength) {
        writeOverlapped(view, ovPtrNum, wasiEINVAL, 0, 0);
        return;
      }
      const offset = view.getBigUint64(ovPtrNum + 24, true);
      if (offset > BigInt(Number.MAX_SAFE_INTEGER)) {
        writeOverlapped(view, ovPtrNum, wasiEOVERFLOW, 0, 0);
        return;
      }
      // fs.read's position argument is a JS Number, so reject lossy offsets.
      position = Number(offset);
    }

    const h = hs.getHandle(handle);
    if (!h) { writeOverlapped(view, ovPtrNum, wasiEBADF, 0, 0); return; }
    if (positioned && h.type !== 'file') {
      writeOverlapped(view, ovPtrNum, wasiESPIPE, 0, 0);
      return;
    }

    const bLen = Number(bufferLen) >>> 0;
    const bPtr = Number(bufferPtr) >>> 0;
    if (bPtr + bLen > view.byteLength) {
      writeOverlapped(view, ovPtrNum, wasiEINVAL, 0, 0);
      return;
    }
    const opId = hs.registerOp(ovPtrNum);

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
      queueFileOperation(h, (done) => {
        const filePosition = positioned ? position : h.position;
        fs.read(h.fd, tmp, 0, bLen, filePosition, (err, n) => {
          if (!positioned && n > 0) h.position += n;
          hs.enqueue(() => {
            if (!hs.isOpActive(ovPtr, opId)) return;
            if (err) { hs.completeOp(ovPtr, opId, mapErrno(err), 0, 0); return; }
            guestWrite(bPtr, tmp.slice(0, n));
            hs.completeOp(ovPtr, opId, 0, 0, n);
          });
          done();
        });
      });
      return;
    }

    if (h.type === 'socket' || h.type === 'pipe_r' || h.type === 'pty') {
      const bPtrNum = Number(bufferPtr);
      const bLenNum = Number(bufferLen);

      const waitForPipeData = () => {
        const stream = h.stream;
        const cleanup = () => {
          stream.removeListener('data', onData);
          stream.removeListener('end', onEnd);
          stream.removeListener('error', onError);
          stream.removeListener('close', onClose);
        };
        const onData = (chunk) => {
          cleanup();
          stream.pause();
          h.leftovers = Buffer.from(chunk);
          deliver();
        };
        const onEnd = () => {
          cleanup();
          h.closed = true;
          deliver();
        };
        const onError = (err) => {
          cleanup();
          h.error = err;
          deliver();
        };
        const onClose = () => {
          cleanup();
          h.closed = true;
          deliver();
        };
        stream.once('data', onData);
        stream.once('end', onEnd);
        stream.once('error', onError);
        stream.once('close', onClose);
        stream.resume();
      };

      const deliver = () => {
        h.onData = null;
        if (h.error) {
          if (h.ptyEioIsEof && h.error.code === 'EIO') {
            h.error = null;
            h.closed = true;
            deliver();
            return;
          }
          hs.enqueue(() => {
            if (!hs.isOpActive(ovPtr, opId)) return;
            hs.completeOp(ovPtr, opId, mapErrno(h.error), 0, 0n);
          });
          return;
        }

        if (h.leftovers && h.leftovers.length > 0) {
          const n = Math.min(h.leftovers.length, bLenNum);
          const chunk = h.leftovers.slice(0, n);
          h.leftovers = h.leftovers.length > n ? h.leftovers.slice(n) : null;
          hs.enqueue(() => {
            if (!hs.isOpActive(ovPtr, opId)) return;
            guestWrite(bPtrNum, chunk);
            hs.completeOp(ovPtr, opId, 0, 0, BigInt(n));
          });
          return;
        }

        if (h.closed) {
          hs.enqueue(() => {
            if (!hs.isOpActive(ovPtr, opId)) return;
            hs.completeOp(ovPtr, opId, 0, 0, 0n);
          });
          return;
        }

        h.onData = deliver;
        if (h.type === 'pipe_r' || h.type === 'pty') waitForPipeData();
      };

      deliver();
      return;
    }

    // Unreadable handle type
    hs.activeOps.delete(ovPtr); hs.pendingCount--;
    writeOverlapped(hs.view, ovPtr, wasiEBADF, 0, 0);
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
    if (!h) { writeOverlapped(hs.view, ovPtr, wasiEBADF, 0, 0); return; }

    const opId = hs.registerOp(ovPtr);

    if (h.type === 'file') {
      queueFileOperation(h, (done) => {
        const filePosition = h.append ? null : h.position;
        fs.write(h.fd, dataCopy, 0, dataCopy.length, filePosition, (err, n) => {
          const complete = (writeErr, errno = writeErr ? mapErrno(writeErr) : wasiSuccess) => {
            if (!writeErr && !h.append && n > 0) h.position += n;
            hs.enqueue(() => {
              if (!hs.isOpActive(ovPtr, opId)) return;
              hs.completeOp(ovPtr, opId, errno, 0, errno ? 0 : n);
            });
            done();
          };

          if (err || !h.append) {
            complete(err);
            return;
          }
          fs.fstat(h.fd, { bigint: true }, (statErr, stat) => {
            if (statErr) {
              complete(statErr);
              return;
            }
            if (stat.size > BigInt(Number.MAX_SAFE_INTEGER)) {
              complete(null, wasiEOVERFLOW);
              return;
            }
            h.position = Number(stat.size);
            complete(null);
          });
        });
      });
      return;
    }

    if (h.type === 'socket' || h.type === 'pipe_w' || h.type === 'pty') {
      const stream = h.type === 'pty' ? h.inputStream : (h.socket || h.stream);
      stream.write(dataCopy, (err) => {
        hs.enqueue(() => {
          if (!hs.isOpActive(ovPtr, opId)) return;
          hs.completeOp(ovPtr, opId, err ? mapErrno(err) : 0, 0, err ? 0 : dataCopy.length);
        });
      });
      return;
    }

    hs.activeOps.delete(ovPtr); hs.pendingCount--;
    writeOverlapped(hs.view, ovPtr, wasiEBADF, 0, 0);
  }

  // ── handle_truncate ──────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, handle i64, length i64)
  function handle_truncate(ovPtr, handle, length) {
    const ptr = Number(ovPtr);
    const opId = hs.registerOp(ptr);
    const h = hs.getHandle(handle);
    if (!h || h.type !== 'file') {
      hs.enqueue(() => hs.completeOp(ptr, opId, wasiEBADF, 0, 0));
      return;
    }

    let size;
    if (typeof length === 'bigint') {
      if (length < 0n) {
        hs.enqueue(() => hs.completeOp(ptr, opId, wasiEINVAL, 0, 0));
        return;
      }
      if (length > BigInt(Number.MAX_SAFE_INTEGER)) {
        hs.enqueue(() => hs.completeOp(ptr, opId, wasiEFBIG, 0, 0));
        return;
      }
      size = Number(length);
    } else if (typeof length === 'number' && Number.isSafeInteger(length)) {
      if (length < 0) {
        hs.enqueue(() => hs.completeOp(ptr, opId, wasiEINVAL, 0, 0));
        return;
      }
      size = length;
    } else {
      hs.enqueue(() => hs.completeOp(ptr, opId, wasiEINVAL, 0, 0));
      return;
    }

    fs.ftruncate(h.fd, size, (err) => {
      hs.enqueue(() => {
        if (!hs.isOpActive(ptr, opId)) return;
        hs.completeOp(ptr, opId, err ? mapErrno(err) : 0, 0, 0);
      });
    });
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
        const h = hs.allocHandle({ type: 'file', fd, path: pathStr, append: isAppend });
        hs.completeOp(ovPtr, opId, 0, 0, h);
      });
    });
  }

  // ── seek ────────────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, handle i64, offset i64, whence i32)
  function seek(ovPtr, handle, offset, whence) {
    const ptr = Number(ovPtr) >>> 0;
    const h = hs.getHandle(handle);
    if (!h || h.type !== 'file') {
      writeOverlapped(hs.view, ptr, wasiEBADF, 0, 0);
      return;
    }

    const seekOffset = typeof offset === 'bigint' ? offset : BigInt(offset);
    if (seekOffset > BigInt(Number.MAX_SAFE_INTEGER) || seekOffset < BigInt(Number.MIN_SAFE_INTEGER)) {
      writeOverlapped(hs.view, ptr, wasiEOVERFLOW, 0, 0);
      return;
    }
    const origin = Number(whence);
    if (origin !== 0 && origin !== 1 && origin !== 2) {
      writeOverlapped(hs.view, ptr, wasiEINVAL, 0, 0);
      return;
    }

    const opId = hs.registerOp(ptr);
    queueFileOperation(h, (done) => {
      const complete = (error, position) => {
        hs.enqueue(() => hs.completeOp(ptr, opId, error, 0, position ?? 0));
        done();
      };
      const applyOffset = (base) => {
        const nextPosition = base + seekOffset;
        if (nextPosition < 0n) {
          complete(wasiEINVAL);
          return;
        }
        if (nextPosition > BigInt(Number.MAX_SAFE_INTEGER)) {
          complete(wasiEOVERFLOW);
          return;
        }
        h.position = Number(nextPosition);
        complete(wasiSuccess, nextPosition);
      };

      if (origin === 0) {
        applyOffset(0n);
      } else if (origin === 1) {
        applyOffset(BigInt(h.position));
      } else {
        fs.fstat(h.fd, { bigint: true }, (err, stat) => {
          if (err) {
            complete(mapErrno(err));
            return;
          }
          if (stat.size > BigInt(Number.MAX_SAFE_INTEGER)) {
            complete(wasiEOVERFLOW);
            return;
          }
          applyOffset(stat.size);
        });
      }
    });
  }

  // ── dir_read ─────────────────────────────────────────────────────────────────
  // Encoding per entry: [1 byte kind][N bytes name][0 byte terminator]
  // Returns: resultExt = bytes copied
  function dir_read(ovPtr, handle, bufferPtr, bufferLen) {
    const bLen = Number(bufferLen);
    const bPtr = Number(bufferPtr);
    const h = hs.getHandle(handle);
    if (!h) { writeOverlapped(hs.view, ovPtr, wasiEBADF, 0, 0); return; }

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
      writeOverlapped(hs.view, ovPtr, wasiEBADF, 0, 0); return;
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
    statFn(pathStr, { bigint: true }).then((st) => {
      hs.enqueue(() => {
        if (!hs.isOpActive(ovPtr, opId)) return;
        if (Number(outLen) < 112) { hs.completeOp(ovPtr, opId, wasiERANGE, 0, 112); return; }
        guestWrite(Number(outPtr), marshalStat(st));
        hs.completeOp(ovPtr, opId, 0, 0, 112);
      });
    }).catch((e) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
    });
  }

  // ── path_link ────────────────────────────────────────────────────────────────
  function path_link(ovPtr, sourcePtr, sourceLen, newPathPtr, newPathLen, flags) {
    const sourcePath = translatePath(Buffer.from(hs.getGuestSlice(Number(sourcePtr), Number(sourceLen))).toString());
    const newPath = translatePath(Buffer.from(hs.getGuestSlice(Number(newPathPtr), Number(newPathLen))).toString());
    const flagBits = Number(flags);
    const opId = hs.registerOp(ovPtr);
    if ((flagBits & ~1) !== 0) {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, wasiEINVAL, 0, 0));
      return;
    }

    let sourceOperation = Promise.resolve(sourcePath);
    if ((flagBits & 1) !== 0) {
      sourceOperation = fs.promises.realpath(sourcePath);
    } else if (process.platform === 'win32') {
      sourceOperation = fs.promises.lstat(sourcePath).then((st) => {
        if (st.isSymbolicLink()) {
          const err = new Error('hard links to symbolic links are unsupported on this host');
          err.code = 'ENOTSUP';
          throw err;
        }
        return sourcePath;
      });
    }
    sourceOperation.then((resolvedSource) => fs.promises.link(resolvedSource, newPath)).then(() => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, wasiSuccess, 0, 0));
    }).catch((e) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
    });
  }

  // ── path_symlink ────────────────────────────────────────────────────────────
  function path_symlink(ovPtr, targetPtr, targetLen, linkPathPtr, linkPathLen, targetKind) {
    const target = Buffer.from(hs.getGuestSlice(Number(targetPtr), Number(targetLen))).toString();
    const linkPath = translatePath(Buffer.from(hs.getGuestSlice(Number(linkPathPtr), Number(linkPathLen))).toString());
    const kind = Number(targetKind);
    const opId = hs.registerOp(ovPtr);
    if (kind < 0 || kind > 2 || !Number.isInteger(kind)) {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, wasiEINVAL, 0, 0));
      return;
    }

    let symlinkType = kind === 2 ? 'dir' : 'file';
    let typeOperation = Promise.resolve(symlinkType);
    if (process.platform === 'win32' && kind === 0) {
      let statPath = target;
      if (!path.isAbsolute(statPath)) statPath = path.resolve(path.dirname(linkPath), statPath);
      typeOperation = fs.promises.stat(statPath).then((st) => st.isDirectory() ? 'dir' : 'file').catch((e) => {
        if (e.code === 'ENOENT') return 'file';
        throw e;
      });
    }
    typeOperation.then((type) => fs.promises.symlink(target, linkPath, type)).then(() => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, wasiSuccess, 0, 0));
    }).catch((e) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
    });
  }

  // ── path_readlink ───────────────────────────────────────────────────────────
  function path_readlink(ovPtr, pathPtr, pathLen, outPtr, outLen) {
    const pathStr = translatePath(Buffer.from(hs.getGuestSlice(Number(pathPtr), Number(pathLen))).toString());
    const output = hs.getGuestSlice(Number(outPtr), Number(outLen));
    const opId = hs.registerOp(ovPtr);
    fs.promises.readlink(pathStr, { encoding: 'buffer' }).then((target) => {
      if (target.length > output.length) {
        hs.enqueue(() => hs.completeOp(ovPtr, opId, wasiERANGE, 0, target.length));
        return;
      }
      hs.enqueue(() => {
        if (!hs.isOpActive(ovPtr, opId)) return;
        output.set(target);
        hs.completeOp(ovPtr, opId, wasiSuccess, 0, target.length);
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

  // ── path_set_times ──────────────────────────────────────────────────────────
  // Signature: (ovPtr, pathPtr, pathLen, atimeSec, atimeNsec,
  //             mtimeSec, mtimeNsec, flags)
  function path_set_times(ovPtr, pathPtr, pathLen, atimeSec, atimeNsec, mtimeSec, mtimeNsec, flags) {
    const pathStr = translatePath(Buffer.from(hs.getGuestSlice(Number(pathPtr), Number(pathLen))).toString());
    const opId = hs.registerOp(ovPtr);
    const flagBits = Number(flags);
    const setAtime = (flagBits & 0x01) !== 0;
    const atimeNow = (flagBits & 0x02) !== 0;
    const setMtime = (flagBits & 0x04) !== 0;
    const mtimeNow = (flagBits & 0x08) !== 0;
    const noFollow = (flagBits & 0x10) !== 0;
    const invalid = (flagBits & ~0x1f) !== 0 ||
      (setAtime && atimeNow) || (setMtime && mtimeNow) ||
      (setAtime && (Number(atimeNsec) < 0 || Number(atimeNsec) >= 1_000_000_000)) ||
      (setMtime && (Number(mtimeNsec) < 0 || Number(mtimeNsec) >= 1_000_000_000));
    if (invalid) {
      hs.enqueue(() => hs.completeOp(Number(ovPtr), opId, wasiEINVAL, 0, 0));
      return;
    }

    const hasUpdate = setAtime || atimeNow || setMtime || mtimeNow;
    const statFn = noFollow ? fs.promises.lstat : fs.promises.stat;
    const setFn = noFollow ? fs.promises.lutimes : fs.promises.utimes;
    if (noFollow && hasUpdate && typeof setFn !== 'function') {
      hs.enqueue(() => hs.completeOp(Number(ovPtr), opId, wasiENOTSUP, 0, 0));
      return;
    }
    statFn(pathStr, { bigint: true }).then((st) => {
      if (!hasUpdate) return;
      const now = new Date();
      const toSeconds = (sec, nsec) => Number(sec) + Number(nsec) / 1_000_000_000;
      const atime = atimeNow ? now : setAtime ? toSeconds(atimeSec, atimeNsec) : Number(st.atimeNs) / 1_000_000_000;
      const mtime = mtimeNow ? now : setMtime ? toSeconds(mtimeSec, mtimeNsec) : Number(st.mtimeNs) / 1_000_000_000;
      return setFn(pathStr, atime, mtime);
    }).then(() => {
      hs.enqueue(() => hs.completeOp(Number(ovPtr), opId, wasiSuccess, 0, 0));
    }).catch((e) => {
      hs.enqueue(() => hs.completeOp(Number(ovPtr), opId, mapErrno(e), 0, 0));
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
      const hItem = { type: 'socket', socket, leftovers: null, onData: null, closed: false, error: null };
      const h = hs.allocHandle(hItem);

      socket.on('data', (chunk) => {
        if (!hItem.leftovers) hItem.leftovers = chunk;
        else hItem.leftovers = Buffer.concat([hItem.leftovers, chunk]);
        if (hItem.onData) hItem.onData();
      });
      socket.on('end', () => {
        hItem.closed = true;
        if (hItem.onData) hItem.onData();
      });
      socket.on('error', (err) => {
        hItem.error = err;
        if (hItem.onData) hItem.onData();
      });

      socket.once('connect', () => {
        hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, h));
      });
      socket.once('error', (e) => {
        hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0n));
      });
    } else {
      const server = net.createServer();
      server.once('error', (e) => {
        hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
      });
      server.listen(iPort, addr, () => {
        const h = hs.allocHandle({ type: 'server', server });
        const address = server.address();
        const boundPort = (address && typeof address === 'object' && address.port) ? address.port : iPort;
        hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, boundPort, h));
      });
    }
  }

  // ── net_accept ───────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, listenHandle i64)
  function net_accept(ovPtr, listenHandle) {
    const h = hs.getHandle(listenHandle);
    if (!h || h.type !== 'server') { writeOverlapped(hs.view, ovPtr, wasiEINVAL, 0, 0); return; }
    const opId = hs.registerOp(ovPtr);
    h.server.once('connection', (socket) => {
      const shItem = { type: 'socket', socket, leftovers: null, onData: null, closed: false, error: null };
      socket.on('data', (chunk) => {
        if (!shItem.leftovers) shItem.leftovers = chunk;
        else shItem.leftovers = Buffer.concat([shItem.leftovers, chunk]);
        if (shItem.onData) shItem.onData();
      });
      socket.on('end', () => {
        shItem.closed = true;
        if (shItem.onData) shItem.onData();
      });
      socket.on('error', (err) => {
        shItem.error = err;
        if (shItem.onData) shItem.onData();
      });
      const sh = hs.allocHandle(shItem);
      hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, sh));
    });
    h.server.once('error', (e) => {
      hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0));
    });
  }

  // ── net_lookup ───────────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, namePtr i32, nameLen i32, bufPtr i32, bufLen i32)
  function net_lookup(ovPtr, namePtr, nameLen, bufPtr, bufLen) {
    const name = Buffer.from(hs.getGuestSlice(Number(namePtr), Number(nameLen))).toString();
    const opId = hs.registerOp(ovPtr);
    dns.lookup(name, { all: true }, (err, addresses) => {
      hs.enqueue(() => {
        if (!hs.isOpActive(ovPtr, opId)) return;
        if (err) { hs.completeOp(ovPtr, opId, mapErrno(err), 0, 0); return; }

        const entries = [];
        for (const a of addresses) {
          if (a.family === 4) {
            const parts = a.address.split('.').map(Number);
            entries.push({ af: 4, data: Buffer.from(parts) });
          } else if (a.family === 6) {
            try {
              // Simple IPv6 parser
              let ip = a.address;
              if (ip.includes('::')) {
                const [pre, post] = ip.split('::').map(s => s ? s.split(':') : []);
                const mid = new Array(8 - (pre.length + post.length)).fill('0');
                ip = [...pre, ...mid, ...post].join(':');
              }
              const parts = ip.split(':');
              const buf = Buffer.alloc(16);
              for (let i = 0; i < 8; i++) buf.writeUInt16BE(parseInt(parts[i] || '0', 16), i * 2);
              entries.push({ af: 6, data: buf });
            } catch (_) {}
          }
        }

        const N = entries.length;
        let dataBytes = 0;
        for (const e of entries) dataBytes += e.data.length;
        const totalSize = 4 + 4 + N + dataBytes;
        const payload = Buffer.alloc(totalSize);
        payload.writeUInt32LE(totalSize, 0);
        payload.writeUInt32LE(N, 4);
        let off = 8;
        for (const e of entries) payload[off++] = e.af;
        for (const e of entries) {
          e.data.copy(payload, off);
          off += e.data.length;
        }

        const toWrite = Math.min(Number(bufLen), payload.length);
        if (toWrite > 0) guestWrite(Number(bufPtr), payload.slice(0, toWrite));
        hs.completeOp(ovPtr, opId, 0, 0, BigInt(totalSize));
      });
    });
  }

  // ── net_cert_verify ──────────────────────────────────────────────────────────
  // Signature: (ovPtr i32, namePtr i32, nameLen i32, chainPtr i32, chainLen i32)
  function net_cert_verify(ovPtr, namePtr, nameLen, chainPtr, chainLen) {
    const name = Buffer.from(hs.getGuestSlice(Number(namePtr), Number(nameLen))).toString();
    const chainBuf = Buffer.from(hs.getGuestSlice(Number(chainPtr), Number(chainLen)));
    const opId = hs.registerOp(ovPtr);

    Promise.resolve().then(() => {
      try {
        if (chainBuf.length < 4) throw { code: 'EINVAL' };
        const count = chainBuf.readUInt32LE(0);
        if (count === 0) throw { code: 'EINVAL' };

        const headerSize = 4 + 4 * count;
        if (chainBuf.length < headerSize) throw { code: 'EINVAL' };

        const certs = [];
        let off = headerSize;
        for (let i = 0; i < count; i++) {
          const dlen = chainBuf.readUInt32LE(4 + 4 * i);
          if (off + dlen > chainBuf.length) throw { code: 'EINVAL' };
          const der = chainBuf.slice(off, off + dlen);
          certs.push(new crypto.X509Certificate(der));
          off += dlen;
        }

        const now = new Date();
        const leaf = certs[0];

        // 1. Check hostname
        if (!leaf.checkHost(name)) {
          hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, 0n));
          return;
        }

        // 2. Verify all certs in provided chain are currently valid
        for (const cert of certs) {
          if (now < new Date(cert.validFrom) || now > new Date(cert.validTo)) {
            hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, 0n));
            return;
          }
        }

        // 3. Verify the chain links (each cert signed by the next)
        for (let i = 0; i < certs.length - 1; i++) {
          if (!certs[i].verify(certs[i + 1].publicKey)) {
            hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, 0n));
            return;
          }
        }

        // 4. Verify the anchor (last cert in chain signed by a trusted root)
        const last = certs[certs.length - 1];
        const roots = getRootCerts();
        let verified = false;

        // Check if last is signed by a root
        for (const root of roots) {
          if (last.issuer === root.subject) {
            if (last.verify(root.publicKey)) {
              verified = true;
              break;
            }
          }
        }

        // Or if last IS a root we trust (self-signed)
        if (!verified && last.subject === last.issuer) {
          for (const root of roots) {
            if (last.fingerprint === root.fingerprint) {
              verified = true;
              break;
            }
          }
        }

        hs.enqueue(() => hs.completeOp(ovPtr, opId, 0, 0, verified ? 1n : 0n));
      } catch (e) {
        hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(e), 0, 0n));
      }
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
      hs.completeOp(ovPtr, opId, wasiEINVAL, 0, 0); return;
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

    const ptySpec = stdioSpecs.find(spec => spec === 'pty' || spec.startsWith('pty:'));
    if (ptySpec) {
      let cols = 80;
      let rows = 24;
      if (ptySpec.startsWith('pty:')) {
        const [colsText, rowsText, term] = ptySpec.slice(4).split(',');
        const parsedCols = Number.parseInt(colsText, 10);
        const parsedRows = Number.parseInt(rowsText, 10);
        if (Number.isFinite(parsedCols) && parsedCols > 0) cols = parsedCols;
        if (Number.isFinite(parsedRows) && parsedRows > 0) rows = parsedRows;
        if (term) envObj.TERM = term;
      }

      const startPty = ptyProgram => {
      if (!ptyAddonBytes || ptyAddonBytes.length === 0) {
        hs.completeOp(ovPtr, opId, wasiENOSYS, 0, 0);
        return;
      }

      let addon;
      try {
        addon = loadPtyAddon();
      } catch (error) {
        L('[pty] failed to load native addon:', error);
        hs.completeOp(ovPtr, opId, wasiEIO, 0, 0);
        return;
      }

      const envEntries = Object.keys(envObj)
        .sort((a, b) => {
          const upperA = a.toUpperCase();
          const upperB = b.toUpperCase();
          return upperA < upperB ? -1 : upperA > upperB ? 1 : 0;
        })
        .map(key => `${key}=${envObj[key]}`);
      const nativeConfig = [
        ptyProgram, ...spawnArgs, '',
        ...envEntries, '',
        cwd, '',
        ...stdioSpecs, '',
      ].join('\0');

      let nativeResult;
      try {
        nativeResult = addon.spawn(nativeConfig, cols, rows);
      } catch (error) {
        L('[pty] native spawn failed:', error);
        hs.completeOp(ovPtr, opId, wasiEIO, 0, 0);
        return;
      }
      if (typeof nativeResult !== 'string') {
        hs.completeOp(ovPtr, opId, mapPtyError(Number(nativeResult)), 0, 0);
        return;
      }

      const resultFields = nativeResult.split('|');
      const [idText, pidText, readFdText, writeFdText, inputPath, outputPath] = resultFields;
      const nativeId = Number(idText);
      const pid = Number(pidText);
      const readFd = Number(readFdText);
      const writeFd = Number(writeFdText);
      if (resultFields.length !== 6 || nativeId <= 0 || nativeId > 32 || !Number.isSafeInteger(nativeId) ||
          !Number.isSafeInteger(pid) || !Number.isInteger(readFd) || !Number.isInteger(writeFd) ||
          inputPath == null || outputPath == null) {
        L('[pty] invalid native spawn response:', nativeResult);
        if (Number.isSafeInteger(nativeId) && nativeId > 0) {
          addon.closePty(nativeId);
          addon.closeChild(nativeId);
        }
        hs.completeOp(ovPtr, opId, wasiEIO, 0, 0);
        return;
      }

      let outputStream;
      let inputStream;
      try {
        if (process.platform === 'win32') {
          outputStream = fs.createReadStream(outputPath);
          inputStream = fs.createWriteStream(inputPath);
        } else {
          outputStream = fs.createReadStream(null, { fd: readFd, autoClose: false });
          inputStream = fs.createWriteStream(null, { fd: writeFd, autoClose: false });
        }
      } catch (error) {
        L('[pty] failed to create PTY streams:', error);
        addon.signal(nativeId, 9);
        addon.closePty(nativeId);
        addon.closeChild(nativeId);
        hs.completeOp(ovPtr, opId, mapErrno(error), 0, 0);
        return;
      }

      const pty = {
        type: 'pty',
        addon,
        nativeId,
        pid,
        stream: outputStream,
        inputStream,
        cols,
        rows,
        leftovers: null,
        onData: null,
        closed: false,
        error: null,
        inputError: null,
        ptyEioIsEof: process.platform !== 'win32',
      };
      outputStream.on('end', () => {
        pty.closed = true;
        if (pty.onData) pty.onData();
      });
      outputStream.on('error', error => {
        pty.error = error;
        if (pty.onData) pty.onData();
      });
      inputStream.on('error', error => {
        pty.inputError = error;
        L('[pty] input stream failed:', error);
      });

      const childHandle = hs.allocHandle({
        type: 'pty_child',
        addon,
        nativeId,
        pid,
      });
      const ptyHandle = hs.allocHandle(pty);
      const resultExt = childHandle | (ptyHandle << 32n);
      hs.enqueue(() => hs.completeOpExtended(
        ovPtr, opId, 0, 0, resultExt, childHandle, ptyHandle,
      ));
      };
      if (process.platform === 'win32') {
        startPty(program);
      } else {
        resolvePtyProgram(program, envObj, cwd).then(startPty, error => {
          hs.enqueue(() => hs.completeOp(ovPtr, opId, mapErrno(error), 0, 0));
        });
      }
      return;
    }

    // Set up stdio
    const pipePairs = [];
    const stdioArr  = [];
    const retainedHandles = [];
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
        else if (fhItem && ((i === 0 && fhItem.type === 'pipe_r') ||
            (i > 0 && fhItem.type === 'pipe_w'))) {
          const retained = hs.retainHandle(fh);
          if (retained) {
            retainedHandles.push(retained);
            stdioArr.push(fhItem.stream);
          } else {
            stdioArr.push('ignore');
          }
        }
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
    const releaseRetainedHandles = () => {
      for (const handle of retainedHandles.splice(0)) hs.releaseHandle(handle);
    };
    try {
      cp = child_process.spawn(program, spawnArgs, {
        cwd,
        env:   envObj,
        stdio: stdioArr.map(s => typeof s === 'string' ? s : 'pipe'),
      });
      cp.once('close', releaseRetainedHandles);
      // Wire up passthrough pipes to child stdio streams
      for (let i = 0; i < stdioArr.length; i++) {
        if (typeof stdioArr[i] !== 'string') {
          if (i === 0 && cp.stdin)   stdioArr[i].pipe(cp.stdin);
          else if (cp.stdio[i])      cp.stdio[i].pipe(stdioArr[i]);
        }
      }
    } catch (e) {
      releaseRetainedHandles();
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
    if (!h || (h.type !== 'child' && h.type !== 'pty_child')) {
      writeOverlapped(hs.view, ovPtr, wasiEBADF, 0, 0);
      return;
    }
    const opId = hs.registerOp(ovPtr);
    if (h.type === 'pty_child') {
      const pollChild = () => {
        if (!hs.isOpActive(ovPtr, opId)) return;
        let status;
        try {
          status = h.addon.poll(h.nativeId);
        } catch (error) {
          L('[pty] process poll failed:', error);
          hs.enqueue(() => hs.completeOp(ovPtr, opId, wasiEIO, 0, 0));
          return;
        }
        if (typeof status !== 'bigint') {
          hs.enqueue(() => hs.completeOp(ovPtr, opId, wasiEIO, 0, 0));
          return;
        }
        if (status === 0n) {
          setTimeout(pollChild, 10);
          return;
        }
          if ((status >> 63n) !== 0n) {
          const errorCode = Number(-BigInt.asIntN(64, status));
          hs.enqueue(() => hs.completeOp(ovPtr, opId, mapPtyError(errorCode), 0, 0));
          return;
        }
        if ((status >> 32n) !== 1n) {
          hs.enqueue(() => hs.completeOp(ovPtr, opId, wasiEIO, 0, 0));
          return;
        }
        const exitCode = status & 0xFFFFFFFFn;
        hs.enqueue(() => hs.completeOp(
          ovPtr, opId, 0, 0, (exitCode << 32n) | exitCode,
        ));
      };
      pollChild();
      return;
    }
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
    if (!h) return;
    if (h.type === 'pty_child') {
      const status = h.addon.signal(h.nativeId, Number(signum));
      if (status !== 0) L('[pty] process signal failed:', status);
      return;
    }
    if (h.type !== 'child') return;
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
    const h = hs.getHandle(handle);
    if (h && h.type === 'pty') return (h.cols << 16) | h.rows;
    if ((handle === 1n || handle === 2n) && process.stdout.isTTY) {
      const w = process.stdout.columns || 80;
      const h = process.stdout.rows    || 24;
      return (w << 16) | h;
    }
    return (80 << 16) | 24;
  }

  function tty_set_size(handle, cols, rows) {
    const h = hs.getHandle(handle);
    if (!h) return wasiEBADF;
    if (h.type === 'pty') {
      const status = h.addon.resize(h.nativeId, Number(cols), Number(rows));
      if (status === 0) {
        h.cols = Number(cols);
        h.rows = Number(rows);
        return wasiSuccess;
      }
      return mapPtyError(status);
    }
    return h.type === 'stdio' && h.stream.isTTY ? wasiSuccess : wasiENOTTY;
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
  // 932-byte binary struct — mirrors env_utils.go layout
  function get_platform_info(ptr, maxLen) {
    const structSize = 932;
    if (Number(maxLen) < structSize) return wasiE2BIG;

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
    copySafe(548, os.homedir());
    copySafe(612, os.tmpdir());
    buf[676] = 1;
    buf.writeBigUInt64LE(BigInt(os.totalmem()), 684);
    buf[692] = 2;
    buf.writeBigUInt64LE(BigInt(os.freemem()), 700);

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
    seek,
    handle_truncate,
    handle_close,
    path_open,
    dir_read,
    path_stat,
    path_link,
    path_symlink,
    path_readlink,
    path_chmod,
    path_set_times,
    path_remove,
    path_mkdir,
    path_rename,
    net_open,
    net_accept,
    net_lookup,
    net_cert_verify,
    process_spawn,
    process_pipe,
    process_wait,
    process_signal,
    signal_wait,
    process_exit,
    tty_set_mode,
    tty_get_size,
    tty_set_size,
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
export async function runHost(brainWasm, argv, ptyAddonBytes = null) {
  const hostState = new HostState();
  const imports   = makeBrainImports(hostState, argv, ptyAddonBytes);

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
  const ptyAddonPath = process.env.MOHABBAT_NODE_PTY_ADDON;
  const ptyAddonBytes = ptyAddonPath ? fs.readFileSync(ptyAddonPath) : null;
  runHost(brainWasm, argv, ptyAddonBytes).catch((err) => {
    console.error(err);
    process.exit(1);
  });
}

export { HostState, READ_FLAG_POSITIONAL };
