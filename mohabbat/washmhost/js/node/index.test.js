import { test } from 'node:test';
import assert from 'node:assert/strict';
import { HostState, makeBrainImports, mapErrno, marshalStat, READ_FLAG_POSITIONAL, translatePath, WRITE_FLAG_POSITIONAL } from './index.js';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import { EventEmitter } from 'node:events';
import { PassThrough } from 'node:stream';
import tls from 'node:tls';

async function waitForHostResult(host, memory, ovPtr) {
    for (let attempt = 0; attempt < 100 && host.completionQueue.length === 0; attempt++) {
        await new Promise(resolve => setTimeout(resolve, 1));
    }
    host.drainQueue();
    return new DataView(memory.buffer).getUint32(ovPtr + 4, true);
}

function writeString(memory, ptr, value) {
    const bytes = new TextEncoder().encode(value);
    new Uint8Array(memory.buffer, ptr, bytes.length).set(bytes);
    return bytes.length;
}

test('marshalStat encodes the 112-byte Rusticated stat record', () => {
    const stat = marshalStat({
        mode: 0o100755n,
        size: 123n,
        mtimeNs: -1n,
        atimeNs: 2n,
        ctimeNs: 3n,
        birthtimeNs: 4n,
        nlink: 5n,
        ino: 6n,
        dev: 7n,
        rdev: 8n,
        blksize: 4096n,
        blocks: 9n,
        uid: 10n,
        gid: 11n,
        isFile: () => true,
        isDirectory: () => false,
        isSymbolicLink: () => false,
        isCharacterDevice: () => false,
        isBlockDevice: () => false,
        isFIFO: () => false,
        isSocket: () => false,
    });
    const view = new DataView(stat.buffer, stat.byteOffset, stat.byteLength);
    assert.equal(stat.byteLength, 112);
    assert.equal(view.getUint32(0, true), 1);
    assert.equal(view.getBigUint64(16, true), 123n);
    assert.equal(view.getBigInt64(24, true), -1n);
    assert.equal(view.getBigInt64(32, true), 2n);
    assert.equal(view.getBigInt64(40, true), 3n);
    assert.equal(view.getBigInt64(48, true), 4n);
    assert.equal(view.getBigUint64(56, true), 5n);
    assert.equal(view.getBigUint64(64, true), 6n);
    assert.equal(view.getBigUint64(72, true), 7n);
    assert.equal(view.getBigUint64(80, true), 8n);
    assert.equal(view.getBigInt64(88, true), 4096n);
    assert.equal(view.getBigInt64(96, true), 9n);
    assert.equal(view.getBigUint64(104, true) & (1n << 3n), 1n << 3n);
});

test('marshalStat exposes Windows directory execute bits like the Go host', () => {
    const stat = marshalStat({
        mode: 0o040666n,
        size: 0n,
        mtimeNs: 0n,
        atimeNs: 0n,
        ctimeNs: 0n,
        birthtimeNs: 0n,
        nlink: 1n,
        ino: 1n,
        dev: 1n,
        rdev: 0n,
        blksize: 4096n,
        blocks: 0n,
        uid: 0n,
        gid: 0n,
        isFile: () => false,
        isDirectory: () => true,
        isSymbolicLink: () => false,
        isCharacterDevice: () => false,
        isBlockDevice: () => false,
        isFIFO: () => false,
        isSocket: () => false,
    });
    const view = new DataView(stat.buffer, stat.byteOffset, stat.byteLength);
    const executeBits = view.getUint32(4, true) & 0o111;
    assert.equal(executeBits, process.platform === 'win32' ? 0o111 : 0);
});

test('writeOverlapped sets FLAG_COMPLETED', () => {
    const mem = new WebAssembly.Memory({ initial: 1 });
    const env = new HostState();
    env.memory = mem;
    const opId = env.registerOp(0);
    env.completeOp(0, opId, 0, 0, 42n);
    const view = new DataView(mem.buffer);
    assert.equal(view.getUint32(0, true), 1); // FLAG_COMPLETED
    assert.equal(view.getUint32(4, true), 0); // error
    assert.equal(view.getBigUint64(16, true), 42n); // resultExt
});

test('cancel ends a timer operation with ECANCELED and suppresses late completion', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);
    const ovPtr = 32;

    env.timer_set(ovPtr, 10);
    assert.equal(host.hasPending(), true);
    env.cancel(ovPtr);
    assert.equal(host.hasPending(), false);
    assert.equal(view.getUint32(ovPtr, true), 1);
    assert.equal(view.getUint32(ovPtr + 4, true), 11);

    await new Promise(resolve => setTimeout(resolve, 25));
    host.drainQueue();
    assert.equal(view.getUint32(ovPtr, true), 1);
    assert.equal(view.getUint32(ovPtr + 4, true), 11);
});

test('system_info_wait is cancellable', () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);
    const ovPtr = 32;

    env.system_info_wait(ovPtr);
    assert.equal(host.hasPending(), true);
    env.cancel(ovPtr);
    assert.equal(host.hasPending(), false);
    assert.equal(view.getUint32(ovPtr, true), 1);
    assert.equal(view.getUint32(ovPtr + 4, true), 11);
});

test('system_info_wait completes when the system context changes', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);
    const ovPtr = 32;
    const systemContext = { locale: 'en-US', timeZone: 'UTC' };
    const originalDateTimeFormat = Intl.DateTimeFormat;

    Intl.DateTimeFormat = class {
        resolvedOptions() {
            return { ...systemContext };
        }
    };

    try {
        env.system_info_wait(ovPtr);
        systemContext.locale = 'fr-FR';
        for (let attempt = 0; attempt < 100 && host.completionQueue.length === 0; attempt++) {
            await new Promise(resolve => setTimeout(resolve, 10));
        }

        assert.ok(host.completionQueue.length > 0, 'system-info change should enqueue completion');
        host.drainQueue();
        assert.equal(view.getUint32(ovPtr, true), 1);
        assert.equal(view.getUint32(ovPtr + 4, true), 0);
    } finally {
        if (host.hasPending()) host.cancelOp(ovPtr);
        host.drainQueue();
        Intl.DateTimeFormat = originalDateTimeFormat;
    }
});

test('get_platform_info includes the versioned system context', () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const ptr = 128;
    assert.equal(env.get_platform_info(ptr, 1264), 0);
    assert.equal(env.get_platform_info(ptr, 1263), 1);

    const view = new DataView(memory.buffer);
    assert.equal(view.getUint32(ptr + 936, true), 1);
    const decoder = new TextDecoder();
    assert.notEqual(decoder.decode(new Uint8Array(memory.buffer, ptr + 940, 64)).split('\0')[0], '');
});

test('cancel removes listeners for a pending pipe read and marks ECANCELED', () => {
      const memory = new WebAssembly.Memory({ initial: 1 });
      const host = new HostState();
      host.memory = memory;
      const { env } = makeBrainImports(host, []);
      const stream = new PassThrough();
      const handle = host.allocHandle({
          type: 'pipe_r',
          stream,
          leftovers: null,
          onData: null,
          closed: false,
          error: null,
      });
      const view = new DataView(memory.buffer);
      const ovPtr = 32;

      env.read(ovPtr, handle, 256, 4);
      assert.equal(stream.listenerCount('data'), 1);
      env.cancel(ovPtr);

      assert.equal(host.hasPending(), false);
      assert.equal(stream.listenerCount('data'), 0);
      assert.equal(stream.listenerCount('end'), 0);
      assert.equal(stream.listenerCount('error'), 0);
      assert.equal(stream.listenerCount('close'), 0);
      assert.equal(view.getUint32(ovPtr, true), 1);
      assert.equal(view.getUint32(ovPtr + 4, true), 11);
      stream.write(Buffer.from('late'));
      host.drainQueue();
      assert.equal(view.getUint32(ovPtr, true), 1);
      assert.equal(view.getUint32(ovPtr + 4, true), 11);
      assert.deepEqual(Array.from(new Uint8Array(memory.buffer, 256, 4)), [0, 0, 0, 0]);
      stream.destroy();
});

test('cancel removes listeners for a pending network accept', () => {
      const memory = new WebAssembly.Memory({ initial: 1 });
      const host = new HostState();
      host.memory = memory;
      const { env } = makeBrainImports(host, []);
      const server = net.createServer();
      const handle = host.allocHandle({ type: 'server', server });
      const view = new DataView(memory.buffer);
      const ovPtr = 32;

      env.net_accept(ovPtr, handle);
      assert.equal(server.listenerCount('connection'), 1);
      assert.equal(host.getHandle(handle).acceptWaiters.length, 1);
      assert.equal(server.listenerCount('error'), 1);
      env.cancel(ovPtr);

      assert.equal(host.hasPending(), false);
      assert.equal(server.listenerCount('connection'), 1);
      assert.equal(host.getHandle(handle).acceptWaiters.length, 0);
      assert.equal(server.listenerCount('error'), 0);
      host.drainQueue();
      assert.equal(view.getUint32(ovPtr, true), 1);
      assert.equal(view.getUint32(ovPtr + 4, true), 11);
});

test('cancel removes a pending child-process exit listener', () => {
      const memory = new WebAssembly.Memory({ initial: 1 });
      const host = new HostState();
      host.memory = memory;
      const { env } = makeBrainImports(host, []);
      const child = new EventEmitter();
      const handle = host.allocHandle({ type: 'child', cp: child });
      const view = new DataView(memory.buffer);
      const ovPtr = 32;

      env.process_wait(ovPtr, handle);
      assert.equal(child.listenerCount('exit'), 1);
      env.cancel(ovPtr);

      assert.equal(host.hasPending(), false);
      assert.equal(child.listenerCount('exit'), 0);
      child.emit('exit', 0, null);
      host.drainQueue();
      assert.equal(view.getUint32(ovPtr, true), 1);
      assert.equal(view.getUint32(ovPtr + 4, true), 11);
});

test('cancel closes pending network opens without allocating handles or completing later', async () => {
      const memory = new WebAssembly.Memory({ initial: 1 });
      const host = new HostState();
      host.memory = memory;
      const { env } = makeBrainImports(host, []);
      const view = new DataView(memory.buffer);
      const addrPtr = 256;
      const addrLen = writeString(memory, addrPtr, '127.0.0.1');
      const handleCount = host.handles.size;
      const ovConnect = 32;
      const ovListen = 64;

      env.net_open(ovConnect, addrPtr, addrLen, 9, 1);
      env.cancel(ovConnect);
      env.net_open(ovListen, addrPtr, addrLen, 0, 0);
      env.cancel(ovListen);

      await new Promise(resolve => setTimeout(resolve, 25));
      host.drainQueue();
      assert.equal(host.hasPending(), false);
      assert.equal(host.handles.size, handleCount);
      for (const ovPtr of [ovConnect, ovListen]) {
          assert.equal(view.getUint32(ovPtr, true), 1);
          assert.equal(view.getUint32(ovPtr + 4, true), 11);
      }
});

test('UDP net_open sends and receives datagrams through ABI handles', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);
    const addrPtr = 256;
    const addrLen = writeString(memory, addrPtr, '127.0.0.1');

    const listenOv = 32;
    env.net_open(listenOv, addrPtr, addrLen, 0, 1 << 2);
    assert.equal(await waitForHostResult(host, memory, listenOv), 0);
    const port = view.getBigUint64(listenOv + 8, true);
    const listenHandle = view.getBigUint64(listenOv + 16, true);
    assert.ok(port > 0n);
    assert.ok(listenHandle > 0n);

    const connectOv = 64;
    env.net_open(connectOv, addrPtr, addrLen, Number(port), 1 | (1 << 2));
    assert.equal(await waitForHostResult(host, memory, connectOv), 0);
    const clientHandle = view.getBigUint64(connectOv + 16, true);
    assert.ok(clientHandle > 0n);

    const payload = new TextEncoder().encode('udp-payload');
    const payloadPtr = 512;
    new Uint8Array(memory.buffer, payloadPtr, payload.length).set(payload);
    const writeOv = 96;
    env.write(writeOv, clientHandle, payloadPtr, payload.length);
    assert.equal(await waitForHostResult(host, memory, writeOv), 0);
    assert.equal(view.getBigUint64(writeOv + 16, true), BigInt(payload.length));

    const readOv = 128;
    const readPtr = 640;
    env.read(readOv, listenHandle, readPtr, payload.length);
    assert.equal(await waitForHostResult(host, memory, readOv), 0);
    assert.equal(view.getBigUint64(readOv + 16, true), BigInt(payload.length));
    assert.deepEqual(
        Array.from(new Uint8Array(memory.buffer, readPtr, payload.length)),
        Array.from(payload),
    );

    env.handle_close(clientHandle);
    env.handle_close(listenHandle);
});

test('multiple concurrent signal waiters are all completed on signal', () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);

    const ovSig1 = 32;
    const ovSig2 = 64;
    env.signal_wait(ovSig1, 28);
    env.signal_wait(ovSig2, 28);

    assert.equal(host.signalWaiters.has(28), true);
    assert.equal(host.signalWaiters.get(28).size, 2);

    host.deliverSignal(28);
    host.drainQueue();

    assert.equal(view.getUint32(ovSig1, true), 1); // FLAG_COMPLETED
    assert.equal(view.getUint32(ovSig1 + 4, true), 0);
    assert.equal(view.getBigUint64(ovSig1 + 16, true), 28n);

    assert.equal(view.getUint32(ovSig2, true), 1);
    assert.equal(view.getUint32(ovSig2 + 4, true), 0);
    assert.equal(view.getBigUint64(ovSig2 + 16, true), 28n);
});

test('positioned read preserves the handle position and rejects stream handles', async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-pread-'));
    const file = path.join(dir, 'data.txt');
    fs.writeFileSync(file, 'abcdef');
    const fd = fs.openSync(file, 'r');
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const handle = host.allocHandle({ type: 'file', fd });
    const view = new DataView(memory.buffer);

    try {
        view.setUint32(32, READ_FLAG_POSITIONAL, true);
        view.setBigUint64(56, 2n, true);
        env.read(32, handle, 256, 3);
        assert.equal(await waitForHostResult(host, memory, 32), 0);
        assert.equal(view.getBigUint64(48, true), 3n);
        assert.equal(Buffer.from(memory.buffer, 256, 3).toString(), 'cde');

        env.read(96, handle, 300, 3);
        assert.equal(await waitForHostResult(host, memory, 96), 0);
        assert.equal(Buffer.from(memory.buffer, 300, 3).toString(), 'abc');

        const pipeHandle = host.allocHandle({ type: 'pipe_r' });
        view.setUint32(160, READ_FLAG_POSITIONAL, true);
        env.read(160, pipeHandle, 320, 3);
        assert.equal(view.getUint32(164, true), 70); // ESPIPE

        env.read(192, handle, 65534, 4);
        assert.equal(view.getUint32(196, true), 28); // WASI EINVAL

        view.setUint32(224, 1 << 25, true);
        env.read(224, handle, 360, 1);
        assert.equal(view.getUint32(228, true), 28); // WASI EINVAL

        view.setUint32(256, READ_FLAG_POSITIONAL, true);
        view.setBigUint64(280, BigInt(Number.MAX_SAFE_INTEGER) + 1n, true);
        env.read(256, handle, 361, 1);
        assert.equal(view.getUint32(260, true), 61); // WASI EOVERFLOW
    } finally {
        fs.closeSync(fd);
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test('positioned write preserves the handle position and zero-fills gaps', async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-pwrite-'));
    const file = path.join(dir, 'data.txt');
    fs.writeFileSync(file, 'abc');
    const fd = fs.openSync(file, 'r+');
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const handle = host.allocHandle({ type: 'file', fd, path: file });
    const view = new DataView(memory.buffer);

    try {
        env.seek(32, handle, 1n, 0);
        assert.equal(await waitForHostResult(host, memory, 32), 0);
        assert.equal(view.getBigUint64(48, true), 1n);

        view.setUint32(64, WRITE_FLAG_POSITIONAL, true);
        view.setBigUint64(88, 5n, true);
        new Uint8Array(memory.buffer, 256, 1)[0] = 'Z'.charCodeAt(0);
        env.write(64, handle, 256, 1);
        assert.equal(await waitForHostResult(host, memory, 64), 0);
        assert.equal(view.getBigUint64(80, true), 1n);
        assert.deepEqual(fs.readFileSync(file), Buffer.from([0x61, 0x62, 0x63, 0, 0, 0x5a]));

        env.seek(96, handle, 0n, 1);
        assert.equal(await waitForHostResult(host, memory, 96), 0);
        assert.equal(view.getBigUint64(112, true), 1n);
    } finally {
        host.handles.delete(handle);
        fs.closeSync(fd);
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test('seek updates sequential file reads and writes', async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-seek-'));
    const file = path.join(dir, 'data.txt');
    fs.writeFileSync(file, 'abcdef');
    const fd = fs.openSync(file, 'r+');
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const handle = host.allocHandle({ type: 'file', fd, path: file });
    const view = new DataView(memory.buffer);
    let appendFd;
    let appendHandle;

    const callAndWait = async (ovPtr, offset, whence, target = handle) => {
        env.seek(ovPtr, target, offset, whence);
        for (let attempt = 0; attempt < 100 &&
            host.completionQueue.length === 0 &&
            (view.getUint32(ovPtr, true) & 1) === 0; attempt++) {
            await new Promise(resolve => setTimeout(resolve, 1));
        }
        host.drainQueue();
        return {
            errno: view.getUint32(ovPtr + 4, true),
            result: view.getBigUint64(ovPtr + 16, true),
        };
    };

    try {
        assert.deepEqual(await callAndWait(32, 2n, 0), { errno: 0, result: 2n });

        env.read(64, handle, 256, 2);
        assert.equal(await waitForHostResult(host, memory, 64), 0);
        assert.equal(Buffer.from(memory.buffer, 256, 2).toString(), 'cd');

        assert.deepEqual(await callAndWait(96, -1n, 1), { errno: 0, result: 3n });
        new Uint8Array(memory.buffer, 320, 1)[0] = 'X'.charCodeAt(0);
        env.write(128, handle, 320, 1);
        assert.equal(await waitForHostResult(host, memory, 128), 0);
        assert.equal(fs.readFileSync(file, 'utf8'), 'abcXef');
        assert.deepEqual(await callAndWait(160, 0n, 2), { errno: 0, result: 6n });
        assert.deepEqual(await callAndWait(192, 1n, 2), { errno: 0, result: 7n });

        assert.equal((await callAndWait(224, -1n, 0)).errno, 28); // WASI EINVAL
        assert.equal((await callAndWait(256, 0n, 3)).errno, 28); // WASI EINVAL
        assert.equal((await callAndWait(288, BigInt(Number.MAX_SAFE_INTEGER) + 1n, 0)).errno, 61); // WASI EOVERFLOW
        env.seek(320, 999n, 0n, 0);
        assert.equal(view.getUint32(324, true), 8); // WASI EBADF

        appendFd = fs.openSync(file, 'a+');
        appendHandle = host.allocHandle({ type: 'file', fd: appendFd, path: file, append: true });
        assert.deepEqual(await callAndWait(352, 0n, 0, appendHandle), { errno: 0, result: 0n });
        new Uint8Array(memory.buffer, 512, 1)[0] = 'X'.charCodeAt(0);
        env.write(384, appendHandle, 512, 1);
        assert.equal(await waitForHostResult(host, memory, 384), 0);
        assert.equal(fs.readFileSync(file, 'utf8'), 'abcXefX');
        assert.deepEqual(await callAndWait(416, 0n, 1, appendHandle), { errno: 0, result: 7n });
    } finally {
        host.handles.delete(handle);
        if (appendHandle !== undefined) host.handles.delete(appendHandle);
        fs.closeSync(fd);
        if (appendFd !== undefined) fs.closeSync(appendFd);
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test('tty_set_size reports handle and terminal support status', () => {
    const handles = new Map();
    const { env } = makeBrainImports({ getHandle: handle => handles.get(handle) }, []);

    assert.equal(env.tty_set_size(999n, 80, 24), 8); // WASI EBADF
    handles.set(1n, { type: 'stdio', stream: { isTTY: false } });
    assert.equal(env.tty_set_size(1n, 80, 24), 59); // WASI ENOTTY
    handles.set(1n, { type: 'stdio', stream: { isTTY: true } });
    assert.equal(env.tty_set_size(1n, 80, 24), 0); // WASI success
    const fileHandle = 3n;
    handles.set(fileHandle, { type: 'file', fd: -1 });
    assert.equal(env.tty_set_size(fileHandle, 80, 24), 59); // WASI ENOTTY
});

test('process_pipe transfers data from its write handle to its read handle', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);

    env.process_pipe(32);
    const handles = view.getBigUint64(48, true);
    const readHandle = handles & 0xFFFF_FFFFn;
    const writeHandle = handles >> 32n;
    const message = Buffer.from('pipe data');
    new Uint8Array(memory.buffer, 512, message.length).set(message);

    try {
        env.write(64, writeHandle, 512, message.length);
        assert.equal(await waitForHostResult(host, memory, 64), 0);

        env.read(96, readHandle, 256, message.length);
        assert.equal(await waitForHostResult(host, memory, 96), 0);
        assert.equal(Buffer.from(memory.buffer, 256, message.length).toString(), message.toString());

        host.freeHandle(writeHandle);
        env.read(128, readHandle, 320, 1);
        assert.equal(await waitForHostResult(host, memory, 128), 0);
        assert.equal(view.getBigUint64(144, true), 0n);
    } finally {
        host.freeHandle(writeHandle);
        host.freeHandle(readHandle);
    }
});

test('process_pipe preserves buffered data and EOF when the writer closes before reading', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);

    env.process_pipe(32);
    const handles = view.getBigUint64(48, true);
    const readHandle = handles & 0xFFFF_FFFFn;
    const writeHandle = handles >> 32n;
    const message = Buffer.from([0, 0x80, 0xff]);
    new Uint8Array(memory.buffer, 512, message.length).set(message);

    try {
        env.write(64, writeHandle, 512, message.length);
        assert.equal(await waitForHostResult(host, memory, 64), 0);
        host.freeHandle(writeHandle);

        env.read(96, readHandle, 256, message.length);
        assert.equal(await waitForHostResult(host, memory, 96), 0);
        assert.equal(view.getUint32(96, true), 1);
        assert.deepEqual(Buffer.from(memory.buffer, 256, message.length), message);

        env.read(128, readHandle, 320, 1);
        assert.equal(await waitForHostResult(host, memory, 128), 0);
        assert.equal(view.getUint32(128, true), 1);
        assert.equal(view.getBigUint64(144, true), 0n);
    } finally {
        host.freeHandle(writeHandle);
        host.freeHandle(readHandle);
    }
});

test('process_spawn keeps fd-backed pipe stdio alive until the child closes', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);
    const message = 'child stdio pipe works';
    let readHandle;
    let writeHandle;
    let childHandle;

    try {
        env.process_pipe(32);
        const pipeHandles = view.getBigUint64(48, true);
        readHandle = pipeHandles & 0xFFFF_FFFFn;
        writeHandle = pipeHandles >> 32n;

        const config = Buffer.from([
            process.execPath,
            '-e',
            `process.stdout.write(${JSON.stringify(message)})`,
            '',
            '',
            '',
            'inherit',
            `fd:${writeHandle}`,
            'inherit',
            '',
            '',
        ].join('\0'));
        new Uint8Array(memory.buffer, 1024, config.length).set(config);
        env.process_spawn(64, 1024, config.length);
        assert.equal(await waitForHostResult(host, memory, 64), 0);
        childHandle = view.getBigUint64(80, true) & 0xFFFF_FFFFn;

        env.handle_close(writeHandle);
        env.read(96, readHandle, 2048, Buffer.byteLength(message));
        assert.equal(await waitForHostResult(host, memory, 96), 0);
        const readCount = Number(view.getBigUint64(112, true));
        assert.equal(Buffer.from(memory.buffer, 2048, readCount).toString(), message);
    } finally {
        if (writeHandle !== undefined) env.handle_close(writeHandle);
        if (readHandle !== undefined) env.handle_close(readHandle);
        if (childHandle !== undefined) env.handle_close(childHandle);
    }
});

test('process_spawn reports missing executables instead of returning a child handle', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const program = path.join(os.tmpdir(), `mohabbat-missing-${process.pid}-${Date.now()}.exe`);
    const config = Buffer.concat([Buffer.from(program), Buffer.from([0, 0, 0, 0])]);
    new Uint8Array(memory.buffer, 1024, config.length).set(config);

    env.process_spawn(32, 1024, config.length);
    const errno = await waitForHostResult(host, memory, 32);

    assert.notEqual(errno, 0);
    assert.equal(host.handles.size, 3);
});

test('path link ABI creates hard links and reports readlink buffer requirements', async (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-links-'));
    const source = path.join(dir, 'source');
    const hardLink = path.join(dir, 'hard-link');
    const target = 'relative target';
    const symbolicLink = path.join(dir, 'symbolic-link');
    fs.writeFileSync(source, 'shared data');

    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    try {
        const sourceLen = writeString(memory, 128, source);
        const hardLen = writeString(memory, 256, hardLink);
        env.path_link(32, 128, sourceLen, 256, hardLen, 0);
        assert.equal(await waitForHostResult(host, memory, 32), 0);
        assert.equal(fs.statSync(source).ino, fs.statSync(hardLink).ino);
        assert.equal(fs.readFileSync(hardLink, 'utf8'), 'shared data');

        const sourceSymlink = path.join(dir, 'source-link');
        const followedLink = path.join(dir, 'followed-link');
        try {
            fs.symlinkSync('source', sourceSymlink, 'file');
        } catch (err) {
            if (['EPERM', 'EACCES', 'ENOTSUP'].includes(err.code)) {
                t.skip(`cannot create source symlink on this host: ${err.code}`);
                return;
            }
            throw err;
        }
        const sourceLinkLen = writeString(memory, 1024, sourceSymlink);
        const followedLen = writeString(memory, 1400, followedLink);
        env.path_link(192, 1024, sourceLinkLen, 1400, followedLen, 1);
        assert.equal(await waitForHostResult(host, memory, 192), 0);
        assert.equal(fs.statSync(source).ino, fs.statSync(followedLink).ino);

        const targetLen = writeString(memory, 384, target);
        const symbolicLen = writeString(memory, 512, symbolicLink);
        env.path_symlink(64, 384, targetLen, 512, symbolicLen, 0);
        const symlinkResult = await waitForHostResult(host, memory, 64);
        if ([2, 58, 63].includes(symlinkResult)) {
            t.skip(`host filesystem or policy does not permit symbolic links (errno ${symlinkResult})`);
            return;
        }
        assert.equal(symlinkResult, 0);

        const linkLen = writeString(memory, 128, symbolicLink);
        env.path_readlink(96, 128, linkLen, 640, 3);
        assert.equal(await waitForHostResult(host, memory, 96), 68);
        assert.equal(new DataView(memory.buffer).getBigUint64(112, true), BigInt(Buffer.byteLength(target)));

        env.path_readlink(128, 128, linkLen, 640, 64);
        assert.equal(await waitForHostResult(host, memory, 128), 0);
        const readCount = Number(new DataView(memory.buffer).getBigUint64(144, true));
        assert.equal(Buffer.from(memory.buffer, 640, readCount).toString(), target);

        env.path_link(160, 128, linkLen, 256, hardLen, 2);
        assert.equal(await waitForHostResult(host, memory, 160), 28);
    } finally {
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test('path_readlink preserves relative target components', async (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-relative-readlink-'));
    const link = path.join(dir, 'link');
    const target = '../parent/../target';
    try {
        try {
            fs.symlinkSync(target, link, 'file');
        } catch (err) {
            if (['EPERM', 'EACCES', 'ENOTSUP'].includes(err.code)) {
                t.skip(`cannot create symlink on this host: ${err.code}`);
                return;
            }
            throw err;
        }

        const memory = new WebAssembly.Memory({ initial: 1 });
        const host = new HostState();
        host.memory = memory;
        const { env } = makeBrainImports(host, []);
        const pathLen = writeString(memory, 128, link);
        env.path_readlink(32, 128, pathLen, 256, 128);
        assert.equal(await waitForHostResult(host, memory, 32), 0);
        const resultLen = Number(new DataView(memory.buffer).getBigUint64(48, true));
        const actualTarget = Buffer.from(memory.buffer, 256, resultLen).toString().replaceAll('\\', '/');
        assert.equal(actualTarget, target);
    } finally {
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test('path_chmod follows a symlink and changes target permissions', async (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-chmod-link-'));
    const target = path.join(dir, 'target');
    const link = path.join(dir, 'link');
    fs.writeFileSync(target, 'mode');
    try {
        try {
            fs.symlinkSync(target, link, 'file');
        } catch (err) {
            if (['EPERM', 'EACCES', 'ENOTSUP'].includes(err.code)) {
                t.skip(`cannot create symlink on this host: ${err.code}`);
                return;
            }
            throw err;
        }

        const memory = new WebAssembly.Memory({ initial: 1 });
        const host = new HostState();
        host.memory = memory;
        const { env } = makeBrainImports(host, []);
        const pathLen = writeString(memory, 128, link);
        env.path_chmod(32, 128, pathLen, 0o400);
        assert.equal(await waitForHostResult(host, memory, 32), 0);
        assert.equal(fs.statSync(target).mode & 0o222, 0);
    } finally {
        fs.chmodSync(target, 0o600);
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test('allocHandle assigns incrementing descriptors', () => {
    const env = new HostState();
    const h1 = env.allocHandle({ type: 'file' });
    const h2 = env.allocHandle({ type: 'pipe' });
    assert.equal(h1, 3n);
    assert.equal(h2, 4n);
});

test('path_set_times sets requested timestamps and preserves omitted values', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-times-'));
    const file = path.join(dir, 'times.txt');
    fs.writeFileSync(file, 'timestamps');

    const callAndWait = async (ovPtr, flags, atimeSec, atimeNsec, mtimeSec, mtimeNsec) => {
        const encodedPath = new TextEncoder().encode(file);
        new Uint8Array(memory.buffer, 128, encodedPath.length).set(encodedPath);
        env.path_set_times(ovPtr, 128, encodedPath.length, atimeSec, atimeNsec, mtimeSec, mtimeNsec, flags);
        for (let attempt = 0; attempt < 100 && host.completionQueue.length === 0; attempt++) {
            await new Promise(resolve => setTimeout(resolve, 1));
        }
        host.drainQueue();
        return new DataView(memory.buffer).getUint32(ovPtr + 4, true);
    };

    try {
        assert.equal(await callAndWait(32, 0x01 | 0x04, 1_700_000_000n, 123_000_000, 1_700_000_100n, 456_000_000), 0);
        let stat = fs.statSync(file, { bigint: true });
        assert.ok(Math.abs(Number(stat.atimeNs - 1_700_000_000_123_000_000n)) < 10_000_000n);
        assert.ok(Math.abs(Number(stat.mtimeNs - 1_700_000_100_456_000_000n)) < 10_000_000n);

        const priorAtime = stat.atimeNs;
        assert.equal(await callAndWait(64, 0x04, 0n, 0, 1_700_000_200n, 789_000_000), 0);
        stat = fs.statSync(file, { bigint: true });
        assert.ok(Math.abs(Number(stat.atimeNs - priorAtime)) < 10_000_000n);
        assert.ok(Math.abs(Number(stat.mtimeNs - 1_700_000_200_789_000_000n)) < 10_000_000n);

        const beforeNowNs = BigInt(Date.now()) * 1_000_000n;
        assert.equal(await callAndWait(80, 0x02 | 0x08, 0n, 0, 0n, 0), 0);
        stat = fs.statSync(file, { bigint: true });
        assert.ok(Math.abs(Number(stat.atimeNs - beforeNowNs)) < 5_000_000_000);
        assert.ok(Math.abs(Number(stat.mtimeNs - beforeNowNs)) < 5_000_000_000);

        assert.equal(await callAndWait(96, 0x01, 1n, 1_000_000_000, 0n, 0), 28);
    } finally {
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test('path_set_times round-trips pre-epoch values or reports unsupported', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-pre-epoch-'));
    const file = path.join(dir, 'times.txt');
    fs.writeFileSync(file, 'timestamps');
    const seconds = BigInt(Math.floor(Date.UTC(1960, 0, 2, 3, 4, 5) / 1000));

    try {
        const before = fs.statSync(file, { bigint: true });
        const encodedPath = new TextEncoder().encode(file);
        new Uint8Array(memory.buffer, 128, encodedPath.length).set(encodedPath);
        env.path_set_times(32, 128, encodedPath.length, seconds, 0, seconds, 0, 0x01 | 0x04);
        const result = await waitForHostResult(host, memory, 32);
        const stat = fs.statSync(file, { bigint: true });
        if (process.platform === 'win32') {
            assert.equal(result, 58); // WASI ENOTSUP
            assert.equal(stat.mtimeNs, before.mtimeNs);
        } else {
            assert.equal(result, 0);
            assert.equal(stat.mtimeNs, seconds * 1_000_000_000n);
        }
    } finally {
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test('path_set_times uses lutimes for no-follow symlink updates', async (t) => {
    if (typeof fs.promises.lutimes !== 'function') {
        t.skip('Node runtime does not provide lutimes');
        return;
    }
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-lutimes-'));
    const target = path.join(dir, 'target.txt');
    const link = path.join(dir, 'link.txt');
    try {
        fs.writeFileSync(target, 'target');
        try {
            fs.symlinkSync(target, link, 'file');
        } catch (err) {
            if (['EPERM', 'EACCES', 'ENOTSUP'].includes(err.code)) {
                t.skip(`cannot create symlink on this host: ${err.code}`);
                return;
            }
            throw err;
        }

        const memory = new WebAssembly.Memory({ initial: 1 });
        const host = new HostState();
        host.memory = memory;
        const { env } = makeBrainImports(host, []);
        const encodedPath = new TextEncoder().encode(link);
        new Uint8Array(memory.buffer, 128, encodedPath.length).set(encodedPath);
        env.path_set_times(32, 128, encodedPath.length, 0n, 0, 1_700_000_300n, 500_000_000, 0x04 | 0x10);
        for (let attempt = 0; attempt < 100 && host.completionQueue.length === 0; attempt++) {
            await new Promise(resolve => setTimeout(resolve, 1));
        }
        host.drainQueue();

        const errorCode = new DataView(memory.buffer).getUint32(36, true);
        if (errorCode === 58) {
            t.skip('host filesystem does not support no-follow timestamp updates');
            return;
        }
        assert.equal(errorCode, 0);
        const linkStat = fs.lstatSync(link, { bigint: true });
        const targetStat = fs.statSync(target, { bigint: true });
        assert.ok(Math.abs(Number(linkStat.mtimeNs - 1_700_000_300_500_000_000n)) < 10_000_000);
        assert.notEqual(targetStat.mtimeNs, linkStat.mtimeNs);
    } finally {
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test('handle_truncate resizes files and rejects invalid lengths and handles', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState();
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-truncate-'));
    const file = path.join(dir, 'file.bin');
    fs.writeFileSync(file, 'abcdef');
    const fd = fs.openSync(file, 'r+');
    const handle = host.allocHandle({ type: 'file', fd, path: file });
    const view = new DataView(memory.buffer);

    const callAndWait = async (ovPtr, target, length) => {
        env.handle_truncate(ovPtr, target, length);
        for (let attempt = 0; attempt < 100 && host.completionQueue.length === 0; attempt++) {
            await new Promise(resolve => setTimeout(resolve, 1));
        }
        host.drainQueue();
        return view.getUint32(ovPtr + 4, true);
    };

    try {
        const firstByte = Buffer.alloc(1);
        assert.equal(fs.readSync(fd, firstByte, 0, 1, null), 1);
        assert.equal(firstByte.toString(), 'a');

        assert.equal(await callAndWait(32, handle, 3n), 0);
        assert.equal(fs.readFileSync(file, 'utf8'), 'abc');
        const secondByte = Buffer.alloc(1);
        assert.equal(fs.readSync(fd, secondByte, 0, 1, null), 1);
        assert.equal(secondByte.toString(), 'b');

        assert.equal(await callAndWait(64, handle, 7n), 0);
        assert.deepEqual(fs.readFileSync(file), Buffer.from([0x61, 0x62, 0x63, 0, 0, 0, 0]));
        assert.equal(await callAndWait(96, handle, 0n), 0);
        assert.equal(fs.statSync(file).size, 0);
        assert.equal(await callAndWait(128, handle, -1n), 28);
        assert.equal(await callAndWait(160, handle, BigInt(Number.MAX_SAFE_INTEGER) + 1n), 22);
        assert.equal(await callAndWait(192, 999n, 0n), 8);
    } finally {
        host.handles.delete(handle);
        fs.closeSync(fd);
        fs.rmSync(dir, { recursive: true, force: true });
    }
});

test('Windows path translation preserves native drive and UNC paths', () => {
    if (process.platform !== 'win32') return;
    const cwd = 'D:\\workspace';
    assert.equal(translatePath('C:\\Users\\test\\project', cwd), 'C:\\Users\\test\\project');
    assert.equal(translatePath('\\\\server\\share\\project', cwd), '\\\\server\\share\\project');
    assert.equal(translatePath('/C:/Users/test/project', cwd), 'C:\\Users\\test\\project');
});

test('get_cwd returns the host-native path', () => {
    const cwd = path.resolve(os.tmpdir(), 'washmhost-native-cwd');
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState({ cwd, listenProcessSignals: false });
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const ptr = 64;
    const length = Number(env.get_cwd(0, 0));

    env.get_cwd(ptr, length);
    const actual = Buffer.from(memory.buffer, ptr, length).toString();
    assert.equal(actual, cwd);
    if (process.platform === 'win32') assert.match(actual, /^[A-Za-z]:\\/);
});

test('set_cwd rejects missing and non-directory paths without changing cwd', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-set-cwd-'));
    const file = path.join(root, 'file');
    const child = path.join(root, 'child');
    fs.writeFileSync(file, 'not a directory');
    fs.mkdirSync(child);

    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState({ cwd: root, listenProcessSignals: false });
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    try {
        const missingLen = writeString(memory, 128, path.join(root, 'missing'));
        assert.equal(env.set_cwd(128, missingLen), 44); // WASI ENOENT
        assert.equal(host.cwd, root);

        const fileLen = writeString(memory, 128, file);
        assert.equal(env.set_cwd(128, fileLen), 54); // WASI ENOTDIR
        assert.equal(host.cwd, root);

        const childLen = writeString(memory, 128, child);
        assert.equal(env.set_cwd(128, childLen), 0);
        assert.equal(host.cwd, child);
    } finally {
        fs.rmSync(root, { recursive: true, force: true });
    }
});

test('get_env canonicalizes Windows path aliases and omits PWD values', () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState({
        env: {
            Path: '%TOOLS%;C:\\Windows',
            Pathext: '.EXE;.BAT',
            Tools: 'C:\\Tools',
            PWD: 'C:\\wrong',
            OLDPWD: 'C:\\old',
            HOME: 'C:\\Users\\test',
        },
        listenProcessSignals: false,
    });
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const ptr = 64;
    const size = Number(env.get_env(0, 0) & 0xffffffffn);
    const result = env.get_env(ptr, size);
    const count = Number(result >> 32n);
    const variables = Buffer.from(memory.buffer, ptr, size)
        .toString()
        .split('\0')
        .filter(Boolean);

    assert.equal(variables.length, count);
    assert.ok(!variables.some(value => /^(PWD|OLDPWD)=/i.test(value)));
    const pathKey = process.platform === 'win32' ? 'PATH' : 'Path';
    const pathValue = process.platform === 'win32'
        ? `C:\\Tools${path.delimiter}C:\\Windows`
        : 'C:\\Tools;C:\\Windows';
    assert.ok(variables.includes(`${pathKey}=${pathValue}`));
    const pathExtKey = process.platform === 'win32' ? 'PATHEXT' : 'Pathext';
    assert.ok(variables.includes(`${pathExtKey}=.EXE;.BAT`));
    assert.ok(variables.includes('HOME=C:\\Users\\test'));
});

test('mapErrno matches Go host mappings for network and filesystem errors', () => {
    assert.equal(mapErrno({ code: 'ECONNRESET' }), 15);
    assert.equal(mapErrno({ code: 'ETIMEDOUT' }), 73);
    assert.equal(mapErrno({ code: 'EROFS' }), 69);
    assert.equal(mapErrno({ code: 'EAFNOSUPPORT' }), 5);
    assert.equal(mapErrno({ code: 'ENOSYS' }), 52);
    assert.equal(mapErrno({ code: 'EHOSTUNREACH' }), 29);
});

test('get_random handles requests larger than Web Crypto single-call limit', () => {
    const memory = new WebAssembly.Memory({ initial: 2 });
    const host = new HostState({ listenProcessSignals: false });
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    assert.doesNotThrow(() => env.get_random(128, 70_000));
});

test('get_time exposes sub-millisecond precision', () => {
    const host = new HostState({ listenProcessSignals: false });
    const { env } = makeBrainImports(host, []);
    const first = env.get_time();
    const second = env.get_time();
    assert.ok(first > 0n);
    assert.ok(second > first);
});

test('net_accept returns EBADF for missing handles and EINVAL for non-listeners', () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState({ listenProcessSignals: false });
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);

    env.net_accept(32, 999n);
    assert.equal(view.getUint32(36, true), 8);
    env.net_accept(64, 1n);
    assert.equal(view.getUint32(68, true), 28);
});

test('net_accept accepts connections established before the guest starts waiting', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState({ listenProcessSignals: false });
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);
    const addrPtr = 256;
    const addrLen = writeString(memory, addrPtr, '127.0.0.1');
    let serverHandle = 0n;
    let acceptedHandle = 0n;
    let client;

    try {
        env.net_open(32, addrPtr, addrLen, 0, 0);
        assert.equal(await waitForHostResult(host, memory, 32), 0);
        const port = Number(view.getBigUint64(40, true));
        serverHandle = view.getBigUint64(48, true);

        client = net.connect({ host: '127.0.0.1', port });
        await new Promise((resolve, reject) => {
            client.once('connect', resolve);
            client.once('error', reject);
        });
        await new Promise(resolve => setImmediate(resolve));

        env.net_accept(64, serverHandle);
        assert.equal(await waitForHostResult(host, memory, 64), 0);
        acceptedHandle = view.getBigUint64(80, true);
        assert.ok(acceptedHandle > 0n);
    } finally {
        if (client) client.destroy();
        const serverState = serverHandle ? host.getHandle(serverHandle) : null;
        if (serverState && serverState.pendingConnections) {
            for (const socket of serverState.pendingConnections) socket.destroy();
        }
        if (acceptedHandle) env.handle_close(acceptedHandle);
        if (serverHandle) env.handle_close(serverHandle);
    }
});

test('net_lookup rejects empty names and preserves IPv4-mapped IPv6 literals', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState({ listenProcessSignals: false });
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const view = new DataView(memory.buffer);
    const namePtr = 256;
    const resultPtr = 512;

    const mappedName = '::ffff:192.0.2.1';
    const mappedLength = writeString(memory, namePtr, mappedName);
    env.net_lookup(32, namePtr, mappedLength, resultPtr, 64);
    assert.equal(await waitForHostResult(host, memory, 32), 0);

    const count = view.getUint32(resultPtr + 4, true);
    const families = new Uint8Array(memory.buffer, resultPtr + 8, count);
    let addressOffset = resultPtr + 8 + count;
    const mappedBytes = Buffer.from([
        0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 192, 0, 2, 1,
    ]);
    let foundMappedAddress = false;
    for (const family of families) {
        const length = family === 4 ? 4 : 16;
        const address = Buffer.from(memory.buffer, addressOffset, length);
        if (family === 6 && address.equals(mappedBytes)) foundMappedAddress = true;
        addressOffset += length;
    }
    assert.ok(foundMappedAddress, 'lookup should encode the IPv4-mapped IPv6 address');

    env.net_lookup(96, namePtr, 0, resultPtr, 64);
    assert.equal(await waitForHostResult(host, memory, 96), 28); // WASI EINVAL
});

test('net_cert_verify maps malformed later DER entries to EINVAL', async () => {
    const memory = new WebAssembly.Memory({ initial: 1 });
    const host = new HostState({ listenProcessSignals: false });
    host.memory = memory;
    const { env } = makeBrainImports(host, []);
    const rootPem = tls.rootCertificates[0];
    assert.ok(rootPem);
    const rootDer = Buffer.from(
        rootPem.replace(/-----BEGIN CERTIFICATE-----|-----END CERTIFICATE-----|\s/g, ''),
        'base64',
    );
    const chain = Buffer.alloc(12 + rootDer.length);
    chain.writeUInt32LE(2, 0);
    chain.writeUInt32LE(rootDer.length, 4);
    chain.writeUInt32LE(0, 8);
    rootDer.copy(chain, 12);
    const nameLen = writeString(memory, 128, 'example.com');
    new Uint8Array(memory.buffer, 1024, chain.length).set(chain);

    env.net_cert_verify(32, 128, nameLen, 1024, chain.length);
    assert.equal(await waitForHostResult(host, memory, 32), 28);
});

test('path_remove removes an empty directory with flag 0x200', async () => {
    const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'washmhost-rmdir-'));
    const subDir = path.join(tmpDir, 'empty-dir');
    fs.mkdirSync(subDir);
    try {
        const memory = new WebAssembly.Memory({ initial: 1 });
        const host = new HostState({ listenProcessSignals: false });
        host.memory = memory;
        const { env } = makeBrainImports(host, []);
        const len = writeString(memory, 256, subDir);
        env.path_remove(64, 256, len, 0x200);
        const err = await waitForHostResult(host, memory, 64);
        assert.equal(err, 0);
        assert.equal(fs.existsSync(subDir), false);
    } finally {
        fs.rmSync(tmpDir, { recursive: true, force: true });
    }
});
