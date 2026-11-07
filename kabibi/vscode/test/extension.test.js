const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');

const extension = require('../extension.js');

class MockUri {
  constructor(uriPath, scheme = 'file') {
    this.path = uriPath;
    this.scheme = scheme;
  }

  static joinPath(base, ...segments) {
    return new MockUri(path.posix.join(base.path, ...segments));
  }
}

function createBaseFS() {
  const entries = new Map([
    ['/', []]
  ]);
  return {
    entries,
    async init() {},
    async seed(initial) {
      for (const entry of initial) entries.set(entry.path, entry);
    },
    async getEntry(entryPath) {
      return entries.get(entryPath) || null;
    },
    async putEntry(entry) {
      entries.set(entry.path, entry);
    },
    async removeEntry(entryPath) {
      return entries.delete(entryPath);
    },
    async readdir(dirPath) {
      return (entries.get(dirPath) || []).slice();
    }
  };
}

test('development vegetable resolves from the source checkout layout', async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'kabibi-extension-dev-'));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const extensionPath = path.join(root, 'kabibi', 'vscode');
  const vegetablePath = path.join(root, 'release', 'dist', 'kabibi.bat');
  await fs.mkdir(extensionPath, { recursive: true });
  await fs.mkdir(path.dirname(vegetablePath), { recursive: true });
  await fs.writeFile(path.join(root, 'go.mod'), 'module test\n');
  await fs.writeFile(path.join(extensionPath, 'package.json'), '{}\n');
  await fs.writeFile(vegetablePath, Buffer.from([1, 2, 3]));

  assert.equal(extension.findDevelopmentVegetable(extensionPath), vegetablePath);
  const installedRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'kabibi-installed-'));
  t.after(() => fs.rm(installedRoot, { recursive: true, force: true }));
  const installedExtension = path.join(installedRoot, 'oyin-bo.kabibi-vscode-1.0.0');
  await fs.mkdir(installedExtension, { recursive: true });
  assert.equal(extension.findDevelopmentVegetable(installedExtension), null);
});

test('source checkout selects the development artifact even before it is built', async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'kabibi-unbuilt-dev-'));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const extensionPath = path.join(root, 'kabibi', 'vscode');
  await fs.mkdir(extensionPath, { recursive: true });
  await fs.writeFile(path.join(root, 'go.mod'), 'module test\n');
  await fs.writeFile(path.join(extensionPath, 'package.json'), '{}\n');

  assert.equal(
    extension.findDevelopmentVegetable(extensionPath),
    path.join(root, 'release', 'dist', 'kabibi.bat')
  );
});

test('missing development artifact fails instead of falling back to the packaged copy', async () => {
  let packagedRead = false;
  await assert.rejects(
    extension.loadCachedKabibiWasm(
      { workspace: { fs: { readFile: async () => { packagedRead = true; } } } },
      {},
      { unpackVegetable: async () => 'packaged-wasm' },
      { developmentVegetablePath: path.join(os.tmpdir(), 'kabibi-no-such-development-artifact.bat') }
    )
  );
  assert.equal(packagedRead, false);
});

test('development vegetable is loaded instead of the packaged copy', async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'kabibi-vegetable-'));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const vegetablePath = path.join(root, 'kabibi.bat');
  await fs.writeFile(vegetablePath, Buffer.from([1, 2, 3]));
  let unpacked = null;
  const result = await extension.loadCachedKabibiWasm(
    null,
    null,
    {
      unpackVegetable: async (bytes) => {
        unpacked = [...bytes];
        return 'dev-wasm';
      }
    },
    { developmentVegetablePath: vegetablePath }
  );

  assert.equal(result, 'dev-wasm');
  assert.deepEqual(unpacked, [1, 2, 3]);
});

test('extension.js embeds the browser workspace filesystem mapping', async () => {
  const baseFS = createBaseFS();
  const files = new Map([
    ['/project', { type: 2, size: 0, mtime: 1 }],
    ['/project/hello.txt', { type: 1, size: 5, mtime: 2 }]
  ]);
  const vscode = {
    Uri: MockUri,
    workspace: {
      workspaceFolders: [{
        name: 'project',
        uri: new MockUri('/project', 'vscode-vfs')
      }],
      fs: {
        stat: async (uri) => {
          const entry = files.get(uri.path);
          if (!entry) {
            const error = new Error('FileNotFound');
            error.code = 'FileNotFound';
            throw error;
          }
          return entry;
        },
        readFile: async (uri) => {
          const entry = files.get(uri.path);
          if (!entry) {
            const error = new Error('FileNotFound');
            error.code = 'FileNotFound';
            throw error;
          }
          return entry.content || new TextEncoder().encode('hello');
        },
        writeFile: async (uri, bytes) => {
          files.set(uri.path, {
            type: 1,
            size: bytes.length,
            content: new Uint8Array(bytes),
            mtime: 3
          });
        },
        createDirectory: async (uri) => {
          files.set(uri.path, { type: 2, size: 0, mtime: 3 });
        },
        delete: async (uri) => files.delete(uri.path),
        rename: async (source, destination) => {
          const entry = files.get(source.path);
          if (!entry) {
            const error = new Error('FileNotFound');
            error.code = 'FileNotFound';
            throw error;
          }
          files.set(destination.path, entry);
          files.delete(source.path);
        },
        readDirectory: async (uri) => {
          if (uri.path !== '/project') return [];
          return [['hello.txt', 1]];
        }
      }
    }
  };
  const vfs = new extension.VSCodeMountedFS({ vscode, baseFS });

  assert.equal(vfs.resolveMount('/workspace/hello.txt').uri.path, '/project/hello.txt');
  assert.equal((await vfs.getEntry('/workspace/hello.txt')).content.length, 5);
  assert.deepEqual(
    (await vfs.readdir('/workspace')).map(({ path: entryPath }) => entryPath),
    ['/workspace/hello.txt']
  );
  await vfs.putEntry({
    path: '/workspace/new.txt',
    type: 'file',
    content: new TextEncoder().encode('new')
  });
  assert.equal((await vfs.getEntry('/workspace/new.txt')).size, 3);
});

test('duplicate multi-root folder names receive stable unique mount names', () => {
  const folders = [
    { name: 'repo', uri: new MockUri('/one') },
    { name: 'repo', uri: new MockUri('/two') }
  ];
  assert.deepEqual(
    extension.workspaceFolderEntries(folders).map(({ name }) => name),
    ['repo', 'repo-2']
  );
});
