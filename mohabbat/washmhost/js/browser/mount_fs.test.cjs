'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

function namedError(name) {
  const error = new Error(name);
  error.name = name;
  return error;
}

class FakeFileHandle {
  constructor(name, content = new Uint8Array()) {
    this.kind = 'file';
    this.name = name;
    this.content = new Uint8Array(content);
  }

  async getFile() {
    const bytes = new Uint8Array(this.content);
    return {
      size: bytes.length,
      lastModified: 1,
      arrayBuffer: async () => bytes.buffer
    };
  }

  async createWritable() {
    let bytes = new Uint8Array();
    return {
      write: async (value) => { bytes = new Uint8Array(value); },
      close: async () => { this.content = bytes; }
    };
  }
}

class FakeDirectoryHandle {
  constructor(name = '') {
    this.kind = 'directory';
    this.name = name;
    this.children = new Map();
  }

  async getDirectoryHandle(name, options = {}) {
    let child = this.children.get(name);
    if (!child && options.create) {
      child = new FakeDirectoryHandle(name);
      this.children.set(name, child);
    }
    if (!child) throw namedError('NotFoundError');
    if (child.kind !== 'directory') throw namedError('TypeMismatchError');
    return child;
  }

  async getFileHandle(name, options = {}) {
    let child = this.children.get(name);
    if (!child && options.create) {
      child = new FakeFileHandle(name);
      this.children.set(name, child);
    }
    if (!child) throw namedError('NotFoundError');
    if (child.kind !== 'file') throw namedError('TypeMismatchError');
    return child;
  }

  async removeEntry(name) {
    const child = this.children.get(name);
    if (!child) throw namedError('NotFoundError');
    if (child.kind === 'directory' && child.children.size > 0) {
      throw namedError('InvalidModificationError');
    }
    this.children.delete(name);
  }

  async *entries() {
    yield* this.children.entries();
  }
}

class FakeBaseFS {
  constructor(entries = []) {
    this.entries = new Map([
      ['/', { path: '/', type: 'dir', mode: 0o755 }]
    ]);
    for (const entry of entries) this.entries.set(entry.path, entry);
  }

  async init() {}

  async seed(entries = []) {
    for (const entry of entries) {
      if (!this.entries.has(entry.path)) this.entries.set(entry.path, entry);
    }
  }

  async statEntry(path) {
    const entry = this.entries.get(path);
    return entry ? { ...entry } : null;
  }

  async getEntry(path) {
    const entry = this.entries.get(path);
    return entry ? { ...entry } : null;
  }

  async putEntry(entry) {
    this.entries.set(entry.path, { ...entry });
  }

  async removeEntry(path) {
    return this.entries.delete(path);
  }

  async readdir(path) {
    const prefix = path === '/' ? '/' : `${path}/`;
    return Array.from(this.entries.values()).filter((entry) => {
      if (entry.path === path || !entry.path.startsWith(prefix)) return false;
      return !entry.path.slice(prefix.length).includes('/');
    }).map((entry) => ({ ...entry }));
  }
}

function createMountFactory() {
  const context = vm.createContext({});
  const source = fs.readFileSync(path.join(__dirname, 'mount_fs.js'), 'utf8');
  vm.runInContext(source, context);
  return context.WashmhostBrowserMountFS;
}

test('browser mount filesystem routes common operations through the selected directory', async () => {
  const base = new FakeBaseFS([
    { path: '/home', type: 'dir', mode: 0o755 },
    { path: '/home/user', type: 'dir', mode: 0o755 },
    { path: '/home/user/alias', type: 'symlink', target: '/mnt/drive' }
  ]);
  const directory = new FakeDirectoryHandle('drive');
  directory.children.set('readme.txt', new FakeFileHandle('readme.txt', new TextEncoder().encode('hello')));
  directory.children.set(' spaced ', new FakeFileHandle(' spaced ', new TextEncoder().encode('space')));
  const mountFS = createMountFactory().create(base);

  await mountFS.mountAt(directory, '/mnt/drive');
  assert.equal((await mountFS.statEntry('/mnt')).type, 'dir');
  assert.deepEqual(
    Array.from(await mountFS.readdir('/mnt'), (entry) => entry.path),
    ['/mnt/drive']
  );
  assert.equal((await mountFS.statEntry('/mnt/drive/readme.txt')).size, 5);
  assert.equal(new TextDecoder().decode((await mountFS.getEntry('/mnt/drive/readme.txt')).content), 'hello');
  assert.equal(new TextDecoder().decode((await mountFS.getEntry('/mnt/drive/ spaced ')).content), 'space');

  await mountFS.putEntry({ path: '/mnt/drive/new', type: 'dir' });
  await mountFS.putEntry({
    path: '/mnt/drive/new/file.bin',
    type: 'file',
    content: new Uint8Array([0, 1, 255]),
    size: 3
  });
  assert.deepEqual(
    Array.from((await mountFS.getEntry('/mnt/drive/new/file.bin')).content),
    [0, 1, 255]
  );
  const createdFile = await mountFS.getEntry('/mnt/drive/new/file.bin');
  await mountFS.chmodEntry({ ...createdFile, mode: 0o600 });
  assert.equal((await mountFS.statEntry('/mnt/drive/new/file.bin')).mode, 0o600);
  await assert.rejects(mountFS.setEntryTimes(createdFile), { code: 'ENOTSUP' });
  assert.equal((await mountFS.resolveEntryPath('/home/user/alias/readme.txt')).path, '/mnt/drive/readme.txt');

  assert.equal(await mountFS.renameEntry('/mnt/drive/new/file.bin', '/home/user/copied.bin'), true);
  assert.deepEqual(
    Array.from((await base.getEntry('/home/user/copied.bin')).content),
    [0, 1, 255]
  );
  assert.equal(await mountFS.getEntry('/mnt/drive/new/file.bin'), null);
  await mountFS.putEntry({ path: '/mnt/drive/new/deep', type: 'dir' });
  await mountFS.putEntry({
    path: '/mnt/drive/new/deep/nested.txt',
    type: 'file',
    content: new TextEncoder().encode('nested'),
    size: 6
  });
  assert.equal(await mountFS.renameEntry('/mnt/drive/new', '/home/user/copied-dir'), true);
  assert.equal(
    new TextDecoder().decode((await base.getEntry('/home/user/copied-dir/deep/nested.txt')).content),
    'nested'
  );
  assert.equal(await mountFS.getEntry('/mnt/drive/new'), null);
  assert.equal(await mountFS.removeEntry('/mnt/drive/readme.txt'), true);
  assert.equal(await mountFS.getEntry('/mnt/drive/readme.txt'), null);
});

test('browser mount filesystem rejects unsafe and conflicting mount points', async () => {
  const base = new FakeBaseFS([
    { path: '/occupied', type: 'dir', mode: 0o755 },
    { path: '/occupied/file', type: 'file', content: new Uint8Array([1]) }
  ]);
  const mountFS = createMountFactory().create(base);
  const directory = new FakeDirectoryHandle('drive');

  await assert.rejects(mountFS.mountAt(directory, '/'), { code: 'EINVAL' });
  await assert.rejects(mountFS.mountAt(directory, '/dev/eval'), { code: 'EINVAL' });
  await assert.rejects(mountFS.mountAt(directory, '/occupied'), { code: 'EEXIST' });
  assert.equal(await mountFS.mountAt(directory, '/mnt/drive'), '/mnt/drive');
  await assert.rejects(mountFS.mountAt(new FakeDirectoryHandle('nested'), '/mnt/drive/nested'), { code: 'EBUSY' });
  await assert.rejects(mountFS.mountAt(new FakeDirectoryHandle('parent'), '/mnt'), { code: 'EBUSY' });
});

test('browser mount filesystem translates revoked directory permissions explicitly', async () => {
  const mountFS = createMountFactory().create(new FakeBaseFS());
  const directory = new FakeDirectoryHandle('restricted');
  directory.getFileHandle = async () => { throw namedError('NotAllowedError'); };

  await mountFS.mountAt(directory, '/mnt/restricted');
  await assert.rejects(mountFS.getEntry('/mnt/restricted/private.txt'), { code: 'EACCES' });
});
