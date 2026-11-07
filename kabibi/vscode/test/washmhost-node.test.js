const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');

test('Windows directory stats include execute bits for shell cd', {
  skip: process.platform !== 'win32',
}, async () => {
  const { marshalStat } = await import('../washmhost-node.mjs');
  const root = path.parse(process.cwd()).root;
  const stat = fs.statSync(root, { bigint: true });
  const mode = marshalStat(stat).readUInt32LE(4);

  assert.equal(mode & 0o111, 0o111);
});
