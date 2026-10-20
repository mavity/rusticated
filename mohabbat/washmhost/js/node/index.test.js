import { test } from 'node:test';
import assert from 'node:assert/strict';
import { HostState, marshalStat } from './index.js';

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

test('writeOverlapped sets FLAG_COMPLETED', () => {
    const mem = new WebAssembly.Memory({ initial: 1 });
    const env = new HostState(mem);
    env.writeOverlapped(0, 0, 0n, 42n);
    const view = new DataView(mem.buffer);
    assert.equal(view.getUint32(0, true), 1); // FLAG_COMPLETED
    assert.equal(view.getUint32(4, true), 0); // error
    assert.equal(view.getBigUint64(16, true), 42n); // resultExt
});

test('allocHandle assigns incrementing descriptors', () => {
    const mem = new WebAssembly.Memory({ initial: 1 });
    const env = new HostState(mem);
    const h1 = env.allocHandle({ type: 'file' });
    const h2 = env.allocHandle({ type: 'pipe' });
    assert.equal(h1, 3n);
    assert.equal(h2, 4n);
});
