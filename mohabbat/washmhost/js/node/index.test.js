import { test } from 'node:test';
import assert from 'node:assert/strict';
import { HostState, makeBrainImports, marshalStat } from './index.js';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

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
    const env = new HostState();
    env.memory = mem;
    const opId = env.registerOp(0);
    env.completeOp(0, opId, 0, 0, 42n);
    const view = new DataView(mem.buffer);
    assert.equal(view.getUint32(0, true), 1); // FLAG_COMPLETED
    assert.equal(view.getUint32(4, true), 0); // error
    assert.equal(view.getBigUint64(16, true), 42n); // resultExt
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
