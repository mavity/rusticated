import { test } from 'node:test';
import assert from 'node:assert/strict';
import { HostState } from './index.js';

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
