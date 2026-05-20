import fs from 'node:fs';
import path from 'node:path';
import { env, argv } from 'node:process';
import crypto from 'node:crypto';

const FLAG_COMPLETED = 1;

class HostState {
  constructor(memory) {
    this.memory = memory;
    this.handles = new Map();
    this.handles.set(0n, { type: 'fd', fd: 0 }); // stdin
    this.handles.set(1n, { type: 'fd', fd: 1 }); // stdout
    this.handles.set(2n, { type: 'fd', fd: 2 }); // stderr
    this.nextHandle = 3n;
    
    this.stats = new Map();
    this.nextStat = 1n;
    
    this.timers = new Map();
  }

  allocHandle(kind) {
    const h = this.nextHandle++;
    this.handles.set(h, kind);
    return h;
  }

  allocStat(info) {
    const h = this.nextStat++;
    this.stats.set(h, info);
    return h;
  }

  getHandle(h) {
    return this.handles.get(h);
  }

  writeOverlapped(ovPtr, error = 0, continued = 0n, resultExt = 0n) {
    const view = new DataView(this.memory.buffer);
    view.setUint32(ovPtr, FLAG_COMPLETED, true);
    view.setUint32(ovPtr + 4, error, true);
    view.setBigUint64(ovPtr + 8, BigInt(continued), true);
    view.setBigUint64(ovPtr + 16, BigInt(resultExt), true);
  }

  getGuestSlice(ptr, len) {
    return new Uint8Array(this.memory.buffer, ptr, len);
  }
}

// Holds the cleanup function for the currently-pending stdin read, or null.
let cancelStdinRead = null;

async function run() {
  const wasmPath = argv[2];
  if (!wasmPath) {
    console.error("Usage: node index.js <path.wasm>");
    process.exit(1);
  }

  const wasmBuffer = fs.readFileSync(wasmPath);
  
  let hostState;

  const importObject = {
    env: {
      get_time: () => BigInt(Date.now()) * 1_000_000n,
      
      get_args: (ptr, len) => {
        const args = argv.slice(2); // host args
        const encoded = args.map(a => Buffer.from(a + '\0'));
        const totalBytes = encoded.reduce((acc, b) => acc + b.length, 0);
        const count = BigInt(args.length);
        
        if (ptr !== 0 && len >= totalBytes) {
          const buf = hostState.getGuestSlice(ptr, totalBytes);
          let off = 0;
          for (const b of encoded) {
            buf.set(b, off);
            off += b.length;
          }
        }
        return (count << 32n) | BigInt(totalBytes);
      },

      get_env: (ptr, len) => {
        const vars = Object.entries(env).map(([k, v]) => Buffer.from(`${k}=${v}\0`));
        const totalBytes = vars.reduce((acc, b) => acc + b.length, 0);
        const count = BigInt(vars.length);

        if (ptr !== 0 && len >= totalBytes) {
          const buf = hostState.getGuestSlice(ptr, totalBytes);
          let off = 0;
          for (const b of vars) {
            buf.set(b, off);
            off += b.length;
          }
        }
        return (count << 32n) | BigInt(totalBytes);
      },

      timer_set: (ovPtr, delayMs) => {
        const id = setTimeout(() => {
          hostState.writeOverlapped(ovPtr, 0, 0n, 0n);
          hostState.timers.delete(ovPtr);
        }, delayMs);
        hostState.timers.set(ovPtr, id);
      },

      timer_cancel: (ovPtr) => {
        const id = hostState.timers.get(ovPtr);
        if (id !== undefined) {
          clearTimeout(id);
          hostState.timers.delete(ovPtr);
        }
      },

      read: (ovPtr, handle, bufferPtr, bufferLen) => {
        const h = hostState.getHandle(handle);
        if (!h) {
          hostState.writeOverlapped(ovPtr, 9); // EBADF
          return;
        }

        if (h.fd === 0) {
          // Use process.stdin stream events instead of fs.read() for stdin.
          // fs.read() submits a blocking ReadFile on a libuv thread-pool thread
          // which keeps the event loop alive and cannot be cancelled cleanly.
          // process.stdin stream events call uv_read_start()/uv_read_stop() so
          // pausing the stream decrements the active handle count and lets the
          // event loop drain naturally when the main future completes.
          const onData = (chunk) => {
            cleanup();
            const view = new Uint8Array(hostState.memory.buffer, bufferPtr, bufferLen);
            const n = Math.min(chunk.length, bufferLen);
            view.set(chunk.subarray(0, n));
            hostState.writeOverlapped(ovPtr, 0, 0n, BigInt(n));
          };
          const onEnd = () => {
            cleanup();
            hostState.writeOverlapped(ovPtr, 0, 0n, 0n); // EOF
          };
          const onError = (err) => {
            cleanup();
            hostState.writeOverlapped(ovPtr, Math.abs(err.errno) || 5, 0n, 0n);
          };
          function cleanup() {
            cancelStdinRead = null;
            process.stdin.off('data', onData);
            process.stdin.off('end', onEnd);
            process.stdin.off('error', onError);
            process.stdin.pause();
          }
          cancelStdinRead = cleanup;
          process.stdin.on('data', onData);
          process.stdin.on('end', onEnd);
          process.stdin.on('error', onError);
          process.stdin.resume();
          return;
        }

        const buf = hostState.getGuestSlice(bufferPtr, bufferLen);
        fs.read(h.fd, buf, 0, bufferLen, null, (err, nRead) => {
          hostState.writeOverlapped(ovPtr, err ? (err.errno || 5) : 0, 0n, BigInt(nRead || 0));
        });
      },

      write: (ovPtr, handle, bufferPtr, bufferLen) => {
        const h = hostState.getHandle(handle);
        if (!h) {
          hostState.writeOverlapped(ovPtr, 9); // EBADF
          return;
        }

        const buf = hostState.getGuestSlice(bufferPtr, bufferLen);

        if (h.fd === 1 || h.fd === 2) {
          // Synchronous write for stdout/stderr — avoids the window where WASM
          // memory could grow and detach the Uint8Array view before the async
          // callback fires.
          try {
            const nWritten = fs.writeSync(h.fd, buf);
            hostState.writeOverlapped(ovPtr, 0, 0n, BigInt(nWritten));
          } catch (err) {
            hostState.writeOverlapped(ovPtr, err.errno || 5, 0n, 0n);
          }
          return;
        }
        // Copy the buffer before the async call so that a WASM memory.grow()
        // between now and the callback cannot detach the view.
        const bufCopy = Buffer.from(buf);
        fs.write(h.fd, bufCopy, 0, bufferLen, null, (err, nWritten) => {
          hostState.writeOverlapped(ovPtr, err ? (err.errno || 5) : 0, 0n, BigInt(nWritten || 0));
        });
      },

      handle_close: (handle) => {
        const h = hostState.getHandle(handle);
        if (h && h.type === 'file') {
          fs.closeSync(h.fd);
          hostState.handles.delete(handle);
        }
      },

      path_open: (ovPtr, pathPtr, pathLen, flags) => {
        const pathBuf = hostState.getGuestSlice(pathPtr, pathLen);
        const filePath = Buffer.from(pathBuf).toString();
        
        // flags: 1=read, 2=write, 4=create, 8=truncate, 16=append, 32=create_new
        let nodeFlags = '';
        if ((flags & 32)) nodeFlags = 'wx+';
        else if ((flags & 4)) nodeFlags = (flags & 8) ? 'w+' : 'a+';
        else if ((flags & 2)) nodeFlags = 'r+';
        else nodeFlags = 'r';

        try {
          const fd = fs.openSync(filePath, nodeFlags);
          const h = hostState.allocHandle({ type: 'file', fd });
          hostState.writeOverlapped(ovPtr, 0, 0n, h);
        } catch (e) {
          hostState.writeOverlapped(ovPtr, e.errno === -4058 ? 2 : (e.errno || 5)); 
        }
      },

      path_stat: (ovPtr, pathPtr, pathLen) => {
        const pathBuf = hostState.getGuestSlice(pathPtr, pathLen);
        const filePath = Buffer.from(pathBuf).toString();
        
        try {
          const stats = fs.statSync(filePath);
          const info = {
            len: BigInt(stats.size),
            is_dir: stats.isDirectory(),
            is_symlink: stats.isSymbolicLink(),
            readonly: !(stats.mode & 0o222),
            mode: stats.mode,
            nlink: BigInt(stats.nlink),
            uid: stats.uid,
            gid: stats.gid,
            inode: BigInt(stats.ino),
            mtime_ns: BigInt(Math.floor(stats.mtimeMs)) * 1_000_000n,
            atime_ns: BigInt(Math.floor(stats.atimeMs)) * 1_000_000n,
            ctime_ns: BigInt(Math.floor(stats.ctimeMs)) * 1_000_000n
          };
          const h = hostState.allocStat(info);
          hostState.writeOverlapped(ovPtr, 0, 0n, h);
        } catch (e) {
          hostState.writeOverlapped(ovPtr, e.errno === -4058 ? 2 : (e.errno || 5));
        }
      },

      stat_len: (h) => hostState.stats.get(h)?.len || 0n,
      stat_is_dir: (h) => hostState.stats.get(h)?.is_dir ? 1 : 0,
      stat_is_file: (h) => hostState.stats.get(h)?.is_dir ? 0 : 1,
      stat_mtime: (h) => hostState.stats.get(h)?.mtime_ns || 0n,
      stat_atime: (h) => hostState.stats.get(h)?.atime_ns || 0n,
      stat_ctime: (h) => hostState.stats.get(h)?.ctime_ns || 0n,
      stat_is_symlink: (h) => hostState.stats.get(h)?.is_symlink ? 1 : 0,
      stat_readonly: (h) => hostState.stats.get(h)?.readonly ? 1 : 0,
      stat_mode: (h) => hostState.stats.get(h)?.mode || 0,
      stat_nlink: (h) => hostState.stats.get(h)?.nlink || 0n,
      stat_uid: (h) => hostState.stats.get(h)?.uid || 0,
      stat_gid: (h) => hostState.stats.get(h)?.gid || 0,
      stat_inode: (h) => hostState.stats.get(h)?.inode || 0n,

      get_random: (ptr, len) => {
        const buf = hostState.getGuestSlice(ptr, len);
        crypto.randomFillSync(buf);
      },

      dir_read: (ovPtr, handle, bufferPtr, bufferLen) => {
        // Stub
        hostState.writeOverlapped(ovPtr, 0, 0n, 0n);
      },

      net_open: (ovPtr) => hostState.writeOverlapped(ovPtr, 38), // ENOSYS
      net_accept: (ovPtr) => hostState.writeOverlapped(ovPtr, 38),
      process_spawn: (ovPtr) => hostState.writeOverlapped(ovPtr, 38),
      process_wait: (ovPtr) => hostState.writeOverlapped(ovPtr, 38),
      process_signal: () => {},
      signal_wait: (ovPtr) => hostState.writeOverlapped(ovPtr, 38),
      tty_set_mode: () => {},
      tty_get_size: () => (80 << 16) | 24,
    }
  };

  const { instance } = await WebAssembly.instantiate(wasmBuffer, importObject);
  const memory = instance.exports.memory;
  hostState = new HostState(memory);

  const runFunc = instance.exports.run;
  const isDoneFunc = instance.exports.is_done;

  while (true) {
    runFunc();
    if (isDoneFunc() === 1) break;
    // Small yield to let any underlying Node magic happen if needed
    await new Promise(resolve => setImmediate(resolve));
  }
  // Cancel any pending stdin read so its handle is no longer active and the
  // event loop can drain naturally.  Any other concurrent async work continues.
  if (cancelStdinRead) cancelStdinRead();
}

run().catch(console.error);
