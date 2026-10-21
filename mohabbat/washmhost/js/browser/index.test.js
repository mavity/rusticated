import { test } from 'node:test';
import assert from 'node:assert/strict';
import washmhost, {
  HostState,
  AsyncIndexedDBFS,
  resolvePath,
  marshalStat,
  STAT_KIND_FILE,
  STAT_KIND_DIR,
  FLAG_COMPLETED,
  wasiSuccess,
  wasiENOENT,
  wasiECANCELED,
  wasiEBADF,
  wasiE2BIG,
  wasiEINVAL,
  wasiEPIPE,
  wasiENOTTY,
  wasiERANGE,
  runEventLoop,
  unpackVegetable
} from './index.js';
import fs from 'node:fs';
import path from 'node:path';

test('Path resolution normalizes relative, root and parent segments', () => {
  assert.equal(resolvePath('/home/user', 'file.txt'), '/home/user/file.txt');
  assert.equal(resolvePath('/home/user', '../etc/hosts'), '/home/etc/hosts');
  assert.equal(resolvePath('/home/user', '/var/log/syslog'), '/var/log/syslog');
  assert.equal(resolvePath('/home/user', '../../../etc/passwd'), '/etc/passwd');
  assert.equal(resolvePath('/', '..'), '/');
});

test('Overlapped struct marshalling and completion flags', () => {
  const mem = new WebAssembly.Memory({ initial: 1 });
  const host = new HostState(mem);

  const ovPtr = 32;
  const opId = host.registerOp(ovPtr);
  assert.equal(host.hasPending(), true);
  assert.equal(host.isOpActive(ovPtr, opId), true);

  host.completeOp(ovPtr, opId, 0, 0, 128);
  assert.equal(host.hasPending(), false);

  const view = new DataView(mem.buffer);
  assert.equal(view.getUint32(ovPtr, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(ovPtr + 4, true), 0); // errorCode = 0
  assert.equal(view.getBigUint64(ovPtr + 8, true), 0n);
  assert.equal(view.getBigUint64(ovPtr + 16, true), 128n); // resultExt = 128
});

test('AbiStat encodes 64-byte descriptor accurately', () => {
  const enc = new TextEncoder();
  const content = enc.encode('hello rustica');
  const fileStat = marshalStat({
    type: 'file',
    size: content.length,
    mode: 0o644,
    mtime: 1700000000000
  });

  assert.equal(fileStat.length, 64);
  const view = new DataView(fileStat.buffer);
  assert.equal(view.getUint32(0, true), STAT_KIND_FILE);
  assert.equal(view.getUint32(4, true), 0o100644); // file mode
  assert.equal(view.getBigUint64(16, true), BigInt(content.length));

  const dirStat = marshalStat({
    type: 'dir',
    mode: 0o755,
    mtime: 1700000000000
  });
  const dirView = new DataView(dirStat.buffer);
  assert.equal(dirView.getUint32(0, true), STAT_KIND_DIR);
  assert.equal(dirView.getUint32(4, true), 0o040755); // dir mode
});

test('Async filesystem storage operations and seed tree', async () => {
  const fs = new AsyncIndexedDBFS();
  await fs.init();
  await fs.seed();

  // Root directory exists
  const root = await fs.getEntry('/');
  assert.ok(root);
  assert.equal(root.type, 'dir');

  // Seeded /etc/hosts exists
  const hosts = await fs.getEntry('/etc/hosts');
  assert.ok(hosts);
  assert.equal(hosts.type, 'file');
  assert.ok(hosts.content.length > 0);

  // Write new file
  const enc = new TextEncoder();
  const testData = enc.encode('browser wasm test content');
  await fs.putEntry({
    path: '/tmp/test.txt',
    type: 'file',
    content: testData,
    size: testData.length,
    mode: 0o644,
    mtime: Date.now()
  });

  const readBack = await fs.getEntry('/tmp/test.txt');
  assert.ok(readBack);
  assert.equal(readBack.size, testData.length);

  // Readdir /tmp
  const tmpEntries = await fs.readdir('/tmp');
  assert.equal(tmpEntries.some(e => e.path === '/tmp/test.txt'), true);

  // Remove file
  const removed = await fs.removeEntry('/tmp/test.txt');
  assert.equal(removed, true);
  const notFound = await fs.getEntry('/tmp/test.txt');
  assert.equal(notFound, null);
});

test('Whitebox ABI interface handles overlapped file read/write correctly', async () => {
  const mem = new WebAssembly.Memory({ initial: 2 });
  const stdoutChunks = [];

  const host = await washmhost({
    test: true,
    memory: mem,
    stdout: (c) => stdoutChunks.push(c)
  });

  const { env, hostState, fs } = host;

  // 1. Write to stdout (fd 1)
  const enc = new TextEncoder();
  const msg = enc.encode('Rustica Browser stdout\n');
  const strPtr = 100;
  new Uint8Array(mem.buffer, strPtr, msg.length).set(msg);

  const ovWrite = 500;
  env.write(ovWrite, 1n, strPtr, msg.length);

  // Drain completions
  hostState.drainQueue();

  const view = new DataView(mem.buffer);
  assert.equal(view.getUint32(ovWrite, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(ovWrite + 4, true), 0);
  assert.equal(view.getBigUint64(ovWrite + 16, true), BigInt(msg.length));
  assert.equal(stdoutChunks.length, 1);

  // 2. Open and read /etc/hostname
  await fs.seed();
  const ovOpen = 600;
  const pathPtr = 200;
  const pathBytes = enc.encode('/etc/hostname');
  new Uint8Array(mem.buffer, pathPtr, pathBytes.length).set(pathBytes);

  env.path_open(ovOpen, pathPtr, pathBytes.length, 0, 0);

  // Allow async IndexedDB / storage lookup
  await new Promise(r => setTimeout(r, 20));
  hostState.drainQueue();

  assert.equal(view.getUint32(ovOpen, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(ovOpen + 4, true), 0);
  const fileHandle = view.getBigUint64(ovOpen + 16, true);
  assert.ok(fileHandle >= 3n);

  // Read from file handle
  const ovRead = 700;
  const readBufPtr = 300;
  env.read(ovRead, fileHandle, readBufPtr, 64);

  hostState.drainQueue();
  assert.equal(view.getUint32(ovRead, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(ovRead + 4, true), 0);
  const nread = Number(view.getBigUint64(ovRead + 16, true));
  assert.ok(nread > 0);

  const readStr = new TextDecoder().decode(new Uint8Array(mem.buffer, readBufPtr, nread));
  assert.equal(readStr, 'rusticated-browser\n');

  // Close handle
  env.handle_close(fileHandle);
  assert.equal(hostState.getHandle(fileHandle), undefined);
});

test('process_pipe creates usable async pipe handles', async () => {
  const mem = new WebAssembly.Memory({ initial: 1 });
  const host = await washmhost({ test: true, memory: mem });
  const { env, hostState } = host;
  const view = new DataView(mem.buffer);

  env.process_pipe(64);
  hostState.drainQueue();
  assert.equal(view.getUint32(64, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(68, true), wasiSuccess);
  const packedHandles = view.getBigUint64(80, true);
  const readHandle = packedHandles & 0xffffffffn;
  const writeHandle = packedHandles >> 32n;
  assert.ok(readHandle >= 3n);
  assert.ok(writeHandle > readHandle);

  const payload = new TextEncoder().encode('pipe-data');
  new Uint8Array(mem.buffer, 256, payload.length).set(payload);
  env.read(96, readHandle, 512, 64);
  assert.equal(hostState.pendingCount, 1);
  env.write(128, writeHandle, 256, payload.length);
  hostState.drainQueue();

  assert.equal(view.getUint32(96, true), FLAG_COMPLETED);
  assert.equal(view.getBigUint64(112, true), BigInt(payload.length));
  assert.equal(view.getUint32(128, true), FLAG_COMPLETED);
  assert.equal(view.getBigUint64(144, true), BigInt(payload.length));
  assert.deepEqual(
    new Uint8Array(mem.buffer, 512, payload.length),
    payload
  );

  env.handle_close(writeHandle);
  env.read(160, readHandle, 600, 64);
  hostState.drainQueue();
  assert.equal(view.getUint32(160, true), FLAG_COMPLETED);
  assert.equal(view.getBigUint64(176, true), 0n);

  env.handle_close(readHandle);
  env.process_pipe(224);
  hostState.drainQueue();
  const secondPackedHandles = view.getBigUint64(240, true);
  const secondReadHandle = secondPackedHandles & 0xffffffffn;
  const secondWriteHandle = secondPackedHandles >> 32n;
  env.handle_close(secondReadHandle);
  env.write(288, secondWriteHandle, 256, payload.length);
  hostState.drainQueue();
  assert.equal(view.getUint32(288, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(292, true), wasiEPIPE);
  env.handle_close(secondWriteHandle);
});

test('dir_read encodes entry type before each NUL-terminated name', async () => {
  const mem = new WebAssembly.Memory({ initial: 1 });
  const host = await washmhost({ test: true, memory: mem });
  const { env, hostState, fs } = host;
  const view = new DataView(mem.buffer);
  await fs.seed();
  await fs.putEntry({
    path: '/home/user/example.txt',
    type: 'file',
    content: new Uint8Array(0),
    size: 0,
    mode: 0o644,
    mtime: Date.now()
  });
  await fs.putEntry({
    path: '/home/user/cache',
    type: 'dir',
    size: 0,
    mode: 0o755,
    mtime: Date.now()
  });

  const pathPtr = 32;
  const path = new TextEncoder().encode('/home/user');
  new Uint8Array(mem.buffer, pathPtr, path.length).set(path);
  env.path_open(64, pathPtr, path.length, 0, 0);
  await new Promise(resolve => setTimeout(resolve, 0));
  hostState.drainQueue();
  const dirHandle = view.getBigUint64(80, true);
  assert.ok(dirHandle >= 3n);

  env.dir_read(96, dirHandle, 256, 128);
  await new Promise(resolve => setTimeout(resolve, 0));
  hostState.drainQueue();

  const bytesRead = Number(view.getBigUint64(112, true));
  const entries = [];
  const data = new Uint8Array(mem.buffer, 256, bytesRead);
  for (let offset = 0; offset < data.length;) {
    const type = data[offset++];
    const end = data.indexOf(0, offset);
    assert.notEqual(end, -1);
    entries.push([type, new TextDecoder().decode(data.subarray(offset, end))]);
    offset = end + 1;
  }
  entries.sort((a, b) => a[1].localeCompare(b[1]));
  assert.deepEqual(entries, [[2, 'cache'], [1, 'example.txt']]);
});

test('Overlapped stdin buffer fulfills pending read on pushStdin', async () => {
  const mem = new WebAssembly.Memory({ initial: 1 });
  const host = await washmhost({ test: true, memory: mem });
  const { env, hostState } = host;

  const ovRead = 800;
  const readBuf = 200;

  // Read from stdin (fd 0) when buffer is empty -> parked in pendingStdinReads
  env.read(ovRead, 0n, readBuf, 10);
  assert.equal(hostState.pendingStdinReads.length, 1);

  // Now user types
  hostState.pushStdin('hello\n');

  // Completion enqueued
  hostState.drainQueue();

  const view = new DataView(mem.buffer);
  assert.equal(view.getUint32(ovRead, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(ovRead + 4, true), 0);
  const nread = Number(view.getBigUint64(ovRead + 16, true));
  assert.equal(nread, 6);

  const str = new TextDecoder().decode(new Uint8Array(mem.buffer, readBuf, nread));
  assert.equal(str, 'hello\n');
});

test('Overlapped timer_set and cancel mechanics', async () => {
  const mem = new WebAssembly.Memory({ initial: 1 });
  const host = await washmhost({ test: true, memory: mem });
  const { env, hostState } = host;

  // 1. Timer fires
  const ovTimer = 1000;
  env.timer_set(ovTimer, 10);
  assert.equal(hostState.hasPending(), true);

  await new Promise(r => setTimeout(r, 25));
  hostState.drainQueue();

  const view = new DataView(mem.buffer);
  assert.equal(view.getUint32(ovTimer, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(ovTimer + 4, true), 0);

  // 2. Timer cancel
  const ovCancel = 1100;
  env.timer_set(ovCancel, 1000);
  assert.equal(hostState.hasPending(), true);

  env.cancel(ovCancel);
  const viewCancel = new DataView(mem.buffer);
  assert.equal(viewCancel.getUint32(ovCancel, true), FLAG_COMPLETED);
  assert.equal(viewCancel.getUint32(ovCancel + 4, true), wasiECANCELED);
});

test('Signal dispatching completes signal_wait', async () => {
  const mem = new WebAssembly.Memory({ initial: 1 });
  const host = await washmhost({ test: true, memory: mem });
  const { env, hostState } = host;

  const ovSig = 1200;
  env.signal_wait(ovSig, 28); // SIGWINCH

  assert.equal(hostState.signalWaiters.has(28), true);

  const completed = hostState.triggerSignal(28);
  assert.equal(completed, 1);

  hostState.drainQueue();
  const view = new DataView(mem.buffer);
  assert.equal(view.getUint32(ovSig, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(ovSig + 4, true), 0);
  assert.equal(view.getBigUint64(ovSig + 16, true), 28n);
});

test('Parked event loop waits for stdin and exits only on explicit process exit', async () => {
  const hostState = new HostState(new WebAssembly.Memory({ initial: 1 }));
  let runCount = 0;
  const exits = [];
  const loop = runEventLoop(hostState, () => {
    runCount++;
    return 0;
  }, code => exits.push(code));

  await new Promise(resolve => setTimeout(resolve, 10));
  assert.equal(exits.length, 0);
  const parkedRunCount = runCount;

  hostState.pushStdin('x');
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.ok(runCount > parkedRunCount);
  assert.equal(exits.length, 0);

  hostState.forcedExitCode = 0;
  hostState.wakeScheduler();
  await loop;
  assert.deepEqual(exits, [0]);
});

test('Parked event loop preserves an explicit exit code when WASM unwinds', async () => {
  const hostState = new HostState(new WebAssembly.Memory({ initial: 1 }));
  const exits = [];
  await runEventLoop(hostState, () => {
    hostState.forcedExitCode = 0;
    throw new WebAssembly.RuntimeError('unreachable');
  }, code => exits.push(code));

  assert.deepEqual(exits, [0]);
});

test('WASM ABI exports metadata and seek imports with expected behavior', async () => {
  const mem = new WebAssembly.Memory({ initial: 1 });
  const host = await washmhost({ test: true, memory: mem });
  const { env, hostState, fs } = host;

  hostState.cwd = '/home/user';
  const cwdLength = Number(env.get_cwd(0, 0));
  assert.equal(cwdLength, new TextEncoder().encode('/home/user').length);
  env.get_cwd(128, cwdLength);
  assert.equal(new TextDecoder().decode(new Uint8Array(mem.buffer, 128, cwdLength)), '/home/user');

  for (const name of [
    'seek',
    'tty_set_size',
    'dylib_close',
    'dylib_alloc',
    'dylib_free',
    'dylib_read_mem',
    'dylib_write_mem',
    'dylib_callback_register',
    'net_accept',
    'net_cert_verify'
  ]) {
    assert.equal(typeof env[name], 'function', `${name} import should be callable`);
  }

  const infoPtr = 128;
  assert.equal(env.get_platform_info(infoPtr, 676), 0);
  assert.equal(env.get_platform_info(infoPtr, 548), wasiE2BIG);
  const infoView = new DataView(mem.buffer);
  assert.equal(infoView.getUint8(infoPtr + 4), '/'.charCodeAt(0));
  assert.equal(infoView.getUint8(infoPtr + 5), ':'.charCodeAt(0));
  assert.equal(infoView.getUint16(infoPtr + 6, true), 0);
  assert.equal(new TextDecoder().decode(new Uint8Array(mem.buffer, infoPtr + 20, 8)), 'browser\0');
  assert.equal(new TextDecoder().decode(new Uint8Array(mem.buffer, infoPtr + 548, 10)), '/home/user');

  await fs.seed();
  const pathPtr = 32;
  const path = new TextEncoder().encode('/etc/hostname');
  new Uint8Array(mem.buffer, pathPtr, path.length).set(path);
  env.path_open(600, pathPtr, path.length, 0, 0);
  await new Promise(r => setTimeout(r, 0));
  hostState.drainQueue();

  const view = new DataView(mem.buffer);
  const fileHandle = view.getBigUint64(616, true);
  assert.ok(fileHandle >= 3n);

  env.seek(700, fileHandle, 7n, 0);
  hostState.drainQueue();
  assert.equal(view.getUint32(700, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(704, true), wasiSuccess);
  assert.equal(view.getBigUint64(716, true), 7n);
});

test('tty_set_size validates handles and delegates standard TTY resizing', async () => {
  const mem = new WebAssembly.Memory({ initial: 1 });
  const calls = [];
  const host = await washmhost({
    test: true,
    memory: mem,
    setTtySize: (cols, rows) => calls.push([cols, rows])
  });
  const { env } = host;

  assert.equal(env.tty_set_size(1n, 120, 40), 0);
  assert.deepEqual(calls, [[120, 40]]);
  assert.equal(env.tty_set_size(99n, 120, 40), wasiEBADF);
  assert.equal(env.tty_set_size(1n, 0, 40), wasiEINVAL);

  const noResizeHost = await washmhost({ test: true, memory: mem });
  assert.equal(noResizeHost.env.tty_set_size(1n, 120, 40), wasiENOTTY);
});

test('Virtual eval dylib evaluates sync, async Promise, handles errors and overflow buffer', async () => {
  const mem = new WebAssembly.Memory({ initial: 2 });
  const host = await washmhost({ test: true, memory: mem });
  const { env, hostState } = host;

  // 1. dylib_open("eval")
  const pathPtr = 100;
  const pathBytes = new TextEncoder().encode('eval');
  new Uint8Array(mem.buffer, pathPtr, pathBytes.length).set(pathBytes);
  env.dylib_open(200, pathPtr, pathBytes.length, 0);
  hostState.drainQueue();

  const view = new DataView(mem.buffer);
  assert.equal(view.getUint32(200, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(204, true), wasiSuccess);
  const libHandle = view.getBigUint64(216, true);
  assert.equal(libHandle, 0xEEEE0001n);

  // 2. dylib_sym(libHandle, "eval")
  const symPtr = 120;
  const symBytes = new TextEncoder().encode('eval');
  new Uint8Array(mem.buffer, symPtr, symBytes.length).set(symBytes);
  env.dylib_sym(240, libHandle, symPtr, symBytes.length);
  hostState.drainQueue();

  assert.equal(view.getUint32(240, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(244, true), wasiSuccess);
  const symHandle = view.getBigUint64(256, true);
  assert.equal(symHandle, 0xEEEE0002n);

  // Helper to execute eval call
  const callEval = async (ovPtr, codeStr, outCap) => {
    const codeBytes = new TextEncoder().encode(codeStr);
    const codePtr = 1000;
    new Uint8Array(mem.buffer, codePtr, codeBytes.length).set(codeBytes);

    const outPtr = 2000;
    const outLenPtr = 3000;
    view.setUint32(outLenPtr, 0, true);

    const argsBase = 4000;
    view.setBigUint64(argsBase + 0, BigInt(codePtr), true);
    view.setBigUint64(argsBase + 8, BigInt(codeBytes.length), true);
    view.setBigUint64(argsBase + 16, BigInt(outPtr), true);
    view.setBigUint64(argsBase + 24, BigInt(outCap), true);
    view.setBigUint64(argsBase + 32, BigInt(outLenPtr), true);

    const resultsPtr = 5000;
    env.dylib_call(ovPtr, symHandle, 0, 0, argsBase, 5, 0n, resultsPtr, 1);
    await new Promise(r => setTimeout(r, 10));
    hostState.drainQueue();

    const err = view.getUint32(ovPtr + 4, true);
    const rc = view.getBigUint64(resultsPtr, true);
    const actualLen = view.getUint32(outLenPtr, true);
    const outText = new TextDecoder().decode(new Uint8Array(mem.buffer, outPtr, Math.min(outCap, actualLen)));
    return { err, rc, actualLen, outText };
  };

  // Test sync evaluation
  const res1 = await callEval(600, '21 + 21', 1024);
  assert.equal(res1.err, wasiSuccess);
  assert.equal(res1.rc, 0n);
  assert.equal(res1.outText, '42');

  // Test async Promise evaluation
  const res2 = await callEval(650, 'Promise.resolve("hello from promise")', 1024);
  assert.equal(res2.err, wasiSuccess);
  assert.equal(res2.rc, 0n);
  assert.equal(res2.outText, 'hello from promise');

  // Test thrown error
  const res3 = await callEval(700, 'throw new Error("boom")', 1024);
  assert.equal(res3.err, wasiSuccess);
  assert.equal(res3.rc, 1n);
  assert.ok(res3.outText.includes('boom'));

  // Test buffer overflow (rc == 2n) and global take overflow
  const longStr = 'x'.repeat(200);
  const res4 = await callEval(750, JSON.stringify(longStr), 10);
  assert.equal(res4.err, wasiSuccess);
  assert.equal(res4.rc, 2n);
  assert.equal(res4.actualLen, 200);
  assert.equal(res4.outText, 'xxxxxxxxxx');
  assert.equal(globalThis.__rustica_take_overflow(), longStr);
});

test('Vegetable unpacker unpacks payload WASM using embedded decompressor', async () => {
  // Use trivial.bat or mohab.bat from repository root
  const vegCandidates = ['trivial.bat', 'mohab.bat', 'demo.bat', 'kabibi.bat'];
  let vegPath = null;
  for (const cand of vegCandidates) {
    const full = path.resolve(process.cwd(), cand);
    if (fs.existsSync(full)) {
      vegPath = full;
      break;
    }
  }

  assert.ok(vegPath, 'At least one vegetable candidate exists in repository');
  const vegData = fs.readFileSync(vegPath);

  const unpackedBuffer = await unpackVegetable(vegData);
  assert.ok(unpackedBuffer instanceof ArrayBuffer);
  assert.ok(unpackedBuffer.byteLength > 0);

  const magic = new Uint8Array(unpackedBuffer, 0, 4);
  assert.deepEqual(Array.from(magic), [0x00, 0x61, 0x73, 0x6D]);

  // Ensure WebAssembly can compile the unpacked module
  const mod = await WebAssembly.compile(unpackedBuffer);
  assert.ok(mod instanceof WebAssembly.Module);
});

test('path_open supports O_TRUNC, O_APPEND, O_EXCL and write slices exact content length', async () => {
  const mem = new WebAssembly.Memory({ initial: 1 });
  const host = await washmhost({ test: true, memory: mem, cwd: '/workspace' });
  const { env, hostState, fs } = host;
  const view = new DataView(mem.buffer);
  const enc = new TextEncoder();

  assert.equal(hostState.cwd, '/workspace');

  await fs.putEntry({
    path: '/workspace',
    type: 'dir',
    mode: 0o755,
    mtime: Date.now()
  });

  // 1. Create file with initial 20-byte content via O_CREAT (64)
  const filePath = enc.encode('notes.txt');
  new Uint8Array(mem.buffer, 32, filePath.length).set(filePath);
  env.path_open(100, 32, filePath.length, 64, 0o644);
  await new Promise(r => setTimeout(r, 0));
  hostState.drainQueue();
  const h1 = view.getBigUint64(116, true);
  assert.ok(h1 >= 3n);

  const initialPayload = enc.encode('0123456789abcdefghij');
  new Uint8Array(mem.buffer, 200, initialPayload.length).set(initialPayload);
  env.write(140, h1, 200, initialPayload.length);
  await new Promise(r => setTimeout(r, 0));
  hostState.drainQueue();
  env.handle_close(h1);

  let stored = await fs.getEntry('/workspace/notes.txt');
  assert.equal(stored.size, 20);
  assert.equal(stored.content.length, 20);

  // 2. O_CREAT | O_EXCL (64 | 128 = 192) on existing file fails with EEXIST (20)
  env.path_open(180, 32, filePath.length, 64 | 128, 0o644);
  await new Promise(r => setTimeout(r, 0));
  hostState.drainQueue();
  assert.equal(view.getUint32(184, true), 20); // wasiEEXIST

  // 3. Reopen with O_TRUNC (512) and write 5 bytes -> no stale tail or buffer-doubling padding
  env.path_open(220, 32, filePath.length, 512, 0o644);
  await new Promise(r => setTimeout(r, 0));
  hostState.drainQueue();
  const h2 = view.getBigUint64(236, true);

  const shortPayload = enc.encode('short');
  new Uint8Array(mem.buffer, 200, shortPayload.length).set(shortPayload);
  env.write(260, h2, 200, shortPayload.length);
  await new Promise(r => setTimeout(r, 0));
  hostState.drainQueue();
  env.handle_close(h2);

  stored = await fs.getEntry('/workspace/notes.txt');
  assert.equal(stored.size, 5);
  assert.equal(stored.content.length, 5);
  assert.equal(new TextDecoder().decode(stored.content), 'short');

  // 4. Reopen with O_APPEND (1024) and write 3 bytes -> appends to end
  env.path_open(300, 32, filePath.length, 1024, 0o644);
  await new Promise(r => setTimeout(r, 0));
  hostState.drainQueue();
  const h3 = view.getBigUint64(316, true);

  const appendPayload = enc.encode('-ok');
  new Uint8Array(mem.buffer, 200, appendPayload.length).set(appendPayload);
  env.write(340, h3, 200, appendPayload.length);
  await new Promise(r => setTimeout(r, 0));
  hostState.drainQueue();
  env.handle_close(h3);

  stored = await fs.getEntry('/workspace/notes.txt');
  assert.equal(stored.size, 8);
  assert.equal(stored.content.length, 8);
  assert.equal(new TextDecoder().decode(stored.content), 'short-ok');
});

test('path_stat prefers fs.statEntry over fs.getEntry when provided', async () => {
  const mem = new WebAssembly.Memory({ initial: 1 });
  let statCalls = 0;
  let getCalls = 0;
  const customFS = {
    async init() {},
    async seed() {},
    async statEntry(p) {
      statCalls++;
      return { path: p, type: 'file', size: 4096, mode: 0o644, mtime: 1700000000000 };
    },
    async getEntry(p) {
      getCalls++;
      return { path: p, type: 'file', content: new Uint8Array(4096), size: 4096, mode: 0o644, mtime: 1700000000000 };
    }
  };

  const host = await washmhost({ test: true, memory: mem, fs: customFS });
  const { env, hostState } = host;
  const view = new DataView(mem.buffer);

  const pBytes = new TextEncoder().encode('/workspace/large.bin');
  new Uint8Array(mem.buffer, 32, pBytes.length).set(pBytes);
  env.path_stat(100, 32, pBytes.length, 0, 200, 64);
  await new Promise(r => setTimeout(r, 0));
  hostState.drainQueue();

  assert.equal(statCalls, 1);
  assert.equal(getCalls, 0);
  assert.equal(view.getUint32(100, true), FLAG_COMPLETED);
  assert.equal(view.getUint32(104, true), wasiSuccess);
  assert.equal(view.getBigUint64(200 + 16, true), 4096n);
  assert.ok(globalThis.washmhostExports && globalThis.washmhostExports.washmhost === washmhost);
});


