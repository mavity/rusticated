// @ts-check
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const ext = require('../extension.js');
const vfsMod = require('../vfs.js');

/**
 * Builds an in-memory mock of the `vscode` namespace (`Uri`, `EventEmitter`,
 * `TerminalProfile`, `workspace.fs`, `workspace.workspaceFolders`, `window`, `commands`).
 */
function createMockVSCode(options = {}) {
  const extRoot = path.resolve(__dirname, '..');
  const wsFiles = new Map(); // uri.path -> { type, content: Uint8Array, mtime }
  const openedDocs = [];
  const registeredCommands = new Map();
  const registeredProfiles = new Map();
  const createdTerminals = [];

  class MockUri {
    constructor(scheme, authority, uriPath) {
      this.scheme = scheme || 'vscode-vfs';
      this.authority = authority || 'workspace';
      this.path = vfsMod.normalizePath(uriPath || '/');
    }
    toString() {
      return `${this.scheme}://${this.authority}${this.path}`;
    }
    static parse(str) {
      const m = /^([a-z0-9+.-]+):\/\/([^/]*)(.*)$/i.exec(str);
      if (!m) return new MockUri('vscode-vfs', 'workspace', str);
      return new MockUri(m[1], m[2], m[3]);
    }
    static file(fsPath) {
      return new MockUri('file', '', fsPath.replace(/\\/g, '/'));
    }
    static joinPath(base, ...segments) {
      const joined = [base.path, ...segments].join('/');
      return new MockUri(base.scheme, base.authority, joined);
    }
  }

  class MockEventEmitter {
    constructor() {
      this.listeners = [];
      this.event = (listener) => {
        this.listeners.push(listener);
        return {
          dispose: () => {
            this.listeners = this.listeners.filter((l) => l !== listener);
          }
        };
      };
    }
    fire(data) {
      for (const l of [...this.listeners]) {
        l(data);
      }
    }
  }

  class MockTerminalProfile {
    constructor(opts) {
      Object.assign(this, opts);
    }
  }

  const extensionUri = new MockUri('vscode-extension', 'kabibi', '/ext');

  const ensureParentDirs = (normPath) => {
    const parts = normPath.split('/').filter(Boolean);
    let cur = '';
    for (let i = 0; i < parts.length - 1; i++) {
      cur += '/' + parts[i];
      if (!wsFiles.has(cur)) {
        wsFiles.set(cur, {
          type: vfsMod.FILE_TYPE_DIRECTORY,
          content: new Uint8Array(0),
          mtime: Date.now()
        });
      }
    }
  };

  // Seed default workspace root(s)
  const workspaceFolders = options.workspaceFolders !== undefined
    ? options.workspaceFolders
    : [{ name: 'project', uri: new MockUri('vscode-vfs', 'workspace', '/project'), index: 0 }];

  for (const folder of workspaceFolders) {
    wsFiles.set(folder.uri.path, {
      type: vfsMod.FILE_TYPE_DIRECTORY,
      content: new Uint8Array(0),
      mtime: Date.now()
    });
  }

  if (options.initialWorkspaceFiles) {
    const enc = new TextEncoder();
    for (const [p, content] of Object.entries(options.initialWorkspaceFiles)) {
      const norm = vfsMod.normalizePath(p);
      ensureParentDirs(norm);
      const bytes = typeof content === 'string' ? enc.encode(content) : new Uint8Array(content);
      wsFiles.set(norm, {
        type: vfsMod.FILE_TYPE_FILE,
        content: bytes,
        mtime: Date.now()
      });
    }
  }

  let readFileCalls = 0;
  let statCalls = 0;

  const vscode = {
    Uri: MockUri,
    EventEmitter: MockEventEmitter,
    TerminalProfile: MockTerminalProfile,
    FileType: {
      Unknown: vfsMod.FILE_TYPE_UNKNOWN,
      File: vfsMod.FILE_TYPE_FILE,
      Directory: vfsMod.FILE_TYPE_DIRECTORY,
      SymbolicLink: vfsMod.FILE_TYPE_SYMLINK
    },
    commands: {
      registerCommand: (id, handler) => {
        registeredCommands.set(id, handler);
        return { dispose: () => registeredCommands.delete(id) };
      }
    },
    window: {
      createTerminal: (opts) => {
        const term = { ...opts, shown: false, show() { this.shown = true; } };
        createdTerminals.push(term);
        return term;
      },
      registerTerminalProfileProvider: (id, provider) => {
        registeredProfiles.set(id, provider);
        return { dispose: () => registeredProfiles.delete(id) };
      },
      showTextDocument: async (doc) => {
        openedDocs.push(doc.uri.path);
        return doc;
      }
    },
    workspace: {
      workspaceFolders,
      openTextDocument: async (uri) => ({ uri }),
      fs: {
        stat: async (uri) => {
          statCalls++;
          const norm = vfsMod.normalizePath(uri.path);
          const item = wsFiles.get(norm);
          if (!item) {
            const err = new Error(`EntryNotFound (${norm})`);
            err.code = 'FileNotFound';
            throw err;
          }
          return {
            type: item.type,
            ctime: item.mtime,
            mtime: item.mtime,
            size: item.content ? item.content.byteLength : 0
          };
        },
        readFile: async (uri) => {
          // Support reading extension assets (`washmhost.js`, `vfs.js`, `kabibi.bat`) from disk
          if (uri.scheme === 'vscode-extension' && uri.path.startsWith('/ext/')) {
            const rel = uri.path.slice('/ext/'.length);
            const diskPath = path.join(extRoot, rel);
            if (!fs.existsSync(diskPath)) {
              const err = new Error(`FileNotFound: ${diskPath}`);
              err.code = 'FileNotFound';
              throw err;
            }
            return new Uint8Array(fs.readFileSync(diskPath));
          }

          readFileCalls++;
          const norm = vfsMod.normalizePath(uri.path);
          const item = wsFiles.get(norm);
          if (!item) {
            const err = new Error(`EntryNotFound (${norm})`);
            err.code = 'FileNotFound';
            throw err;
          }
          if (item.type === vfsMod.FILE_TYPE_DIRECTORY) {
            const err = new Error(`FileIsADirectory (${norm})`);
            err.code = 'FileIsADirectory';
            throw err;
          }
          return new Uint8Array(item.content);
        },
        writeFile: async (uri, content) => {
          const norm = vfsMod.normalizePath(uri.path);
          ensureParentDirs(norm);
          wsFiles.set(norm, {
            type: vfsMod.FILE_TYPE_FILE,
            content: new Uint8Array(content),
            mtime: Date.now()
          });
        },
        createDirectory: async (uri) => {
          const norm = vfsMod.normalizePath(uri.path);
          ensureParentDirs(norm);
          wsFiles.set(norm, {
            type: vfsMod.FILE_TYPE_DIRECTORY,
            content: new Uint8Array(0),
            mtime: Date.now()
          });
        },
        delete: async (uri) => {
          const norm = vfsMod.normalizePath(uri.path);
          if (!wsFiles.has(norm)) {
            const err = new Error(`EntryNotFound (${norm})`);
            err.code = 'FileNotFound';
            throw err;
          }
          wsFiles.delete(norm);
        },
        rename: async (srcUri, dstUri) => {
          const s = vfsMod.normalizePath(srcUri.path);
          const d = vfsMod.normalizePath(dstUri.path);
          const item = wsFiles.get(s);
          if (!item) {
            const err = new Error(`EntryNotFound (${s})`);
            err.code = 'FileNotFound';
            throw err;
          }
          ensureParentDirs(d);
          wsFiles.set(d, item);
          wsFiles.delete(s);
        },
        readDirectory: async (uri) => {
          const norm = vfsMod.normalizePath(uri.path);
          const dirItem = wsFiles.get(norm);
          if (!dirItem || dirItem.type !== vfsMod.FILE_TYPE_DIRECTORY) {
            const err = new Error(`EntryNotFound (${norm})`);
            err.code = 'FileNotFound';
            throw err;
          }
          const prefix = norm === '/' ? '/' : norm + '/';
          const out = [];
          for (const [k, v] of wsFiles.entries()) {
            if (k !== norm && k.startsWith(prefix)) {
              const rest = k.slice(prefix.length);
              if (rest && !rest.includes('/')) {
                out.push([rest, v.type]);
              }
            }
          }
          return out;
        }
      }
    },
    _debug: {
      extensionUri,
      wsFiles,
      openedDocs,
      registeredCommands,
      registeredProfiles,
      createdTerminals,
      getStats: () => ({ statCalls, readFileCalls }),
      resetStats: () => { statCalls = 0; readFileCalls = 0; }
    }
  };

  return vscode;
}

test('VSCodeMountedFS routes /workspace to vscode.workspace.fs and other paths to AsyncIndexedDBFS', async () => {
  const vscode = createMockVSCode({
    initialWorkspaceFiles: {
      '/project/README.md': '# Hello from VS Code Workspace\n',
      '/project/src/index.js': 'console.log(42);\n'
    }
  });

  const { washmhostExports, vfsExports } = await ext.ensureRuntimeModules(
    vscode,
    vscode._debug.extensionUri
  );

  const baseFS = new washmhostExports.AsyncIndexedDBFS('TestBaseFS');
  const vfs = new vfsExports.VSCodeMountedFS({
    vscode,
    baseFS,
    mountPoint: '/workspace'
  });

  await vfs.init();
  await vfs.seed();

  // 1. Root readdir includes /workspace alongside /bin, /etc, /home, /tmp
  const rootEntries = await vfs.readdir('/');
  const rootPaths = rootEntries.map((e) => e.path);
  assert.ok(rootPaths.includes('/workspace'), 'root should list /workspace');
  assert.ok(rootPaths.includes('/etc'), 'root should list /etc');
  assert.ok(rootPaths.includes('/bin'), 'root should list /bin');

  // 2. /bin/code helper script was seeded
  const codeBin = await vfs.getEntry('/bin/code');
  assert.ok(codeBin && codeBin.type === 'file');

  // 3. statEntry on /workspace/README.md uses vscode.workspace.fs.stat WITHOUT readFile
  vscode._debug.resetStats();
  const statReadme = await vfs.statEntry('/workspace/README.md');
  assert.ok(statReadme);
  assert.equal(statReadme.type, 'file');
  assert.equal(statReadme.size, '# Hello from VS Code Workspace\n'.length);
  assert.equal(vscode._debug.getStats().statCalls, 1);
  assert.equal(vscode._debug.getStats().readFileCalls, 0);

  // 4. getEntry reads file contents
  const fullReadme = await vfs.getEntry('/workspace/README.md');
  assert.ok(fullReadme);
  assert.equal(new TextDecoder().decode(fullReadme.content), '# Hello from VS Code Workspace\n');
  assert.equal(vscode._debug.getStats().readFileCalls, 1);

  // 5. readdir on /workspace lists README.md and src
  const wsChildren = await vfs.readdir('/workspace');
  const wsNames = wsChildren.map((c) => c.path).sort();
  assert.deepEqual(wsNames, ['/workspace/README.md', '/workspace/src']);

  // 6. Writing a file in /workspace updates the underlying VS Code workspace
  const enc = new TextEncoder();
  const newBytes = enc.encode('created inside kabibi\n');
  await vfs.putEntry({
    path: '/workspace/newfile.txt',
    type: 'file',
    content: newBytes,
    size: newBytes.length,
    mode: 0o644,
    mtime: Date.now()
  });

  assert.equal(
    new TextDecoder().decode(vscode._debug.wsFiles.get('/project/newfile.txt').content),
    'created inside kabibi\n'
  );

  // 7. Rename inside /workspace uses vscode.workspace.fs.rename
  const renamed = await vfs.renameEntry('/workspace/newfile.txt', '/workspace/renamed.txt');
  assert.equal(renamed, true);
  assert.equal(vscode._debug.wsFiles.has('/project/newfile.txt'), false);
  assert.equal(vscode._debug.wsFiles.has('/project/renamed.txt'), true);

  // 8. Cross-mount move from /tmp (IndexedDB/baseFS) to /workspace (vscode.workspace.fs)
  const tmpBytes = enc.encode('from tmp to workspace');
  await vfs.putEntry({
    path: '/tmp/staged.txt',
    type: 'file',
    content: tmpBytes,
    size: tmpBytes.length,
    mode: 0o644,
    mtime: Date.now()
  });
  await vfs.renameEntry('/tmp/staged.txt', '/workspace/from-tmp.txt');
  assert.equal(await vfs.getEntry('/tmp/staged.txt'), null);
  assert.equal(
    new TextDecoder().decode(vscode._debug.wsFiles.get('/project/from-tmp.txt').content),
    'from tmp to workspace'
  );

  // 9. Remove file from /workspace
  const removed = await vfs.removeEntry('/workspace/renamed.txt');
  assert.equal(removed, true);
  assert.equal(await vfs.statEntry('/workspace/renamed.txt'), null);
});

test('VSCodeMountedFS supports multi-root workspaces under /workspace/<folder.name>', async () => {
  const vscode = createMockVSCode({});
  const Uri = vscode.Uri;
  const multiVscode = createMockVSCode({
    workspaceFolders: [
      { name: 'frontend', uri: new Uri('vscode-vfs', 'workspace', '/repos/fe'), index: 0 },
      { name: 'backend', uri: new Uri('vscode-vfs', 'workspace', '/repos/be'), index: 1 }
    ],
    initialWorkspaceFiles: {
      '/repos/fe/app.js': 'export default "fe";\n',
      '/repos/be/server.go': 'package main\n'
    }
  });

  const { washmhostExports, vfsExports } = await ext.ensureRuntimeModules(
    multiVscode,
    multiVscode._debug.extensionUri
  );

  const vfs = new vfsExports.VSCodeMountedFS({
    vscode: multiVscode,
    baseFS: new washmhostExports.AsyncIndexedDBFS('TestMultiRootFS'),
    mountPoint: '/workspace'
  });
  await vfs.init();
  await vfs.seed();

  const folders = await vfs.readdir('/workspace');
  assert.deepEqual(
    folders.map((f) => [f.path, f.type]),
    [
      ['/workspace/frontend', 'dir'],
      ['/workspace/backend', 'dir']
    ]
  );

  const feApp = await vfs.getEntry('/workspace/frontend/app.js');
  assert.ok(feApp);
  assert.equal(new TextDecoder().decode(feApp.content), 'export default "fe";\n');

  const beServer = await vfs.getEntry('/workspace/backend/server.go');
  assert.ok(beServer);
  assert.equal(new TextDecoder().decode(beServer.content), 'package main\n');
});

test('KabibiPseudoterminal boots packaged kabibi.bat vegetable and interacts with /workspace', async () => {
  const vscode = createMockVSCode({
    initialWorkspaceFiles: {
      '/project/hello.txt': 'Hello from mounted VSCode VFS!\n'
    }
  });

  const context = {
    extensionUri: vscode._debug.extensionUri,
    subscriptions: []
  };

  // Register extension commands and profile provider
  const addonExports = {};
  globalThis.KabibiVSCodeExtension.registerVscodeExtension(addonExports, vscode);
  addonExports.activate(context);

  assert.equal(vscode._debug.registeredCommands.has('kabibi.openTerminal'), true);
  assert.equal(vscode._debug.registeredProfiles.has('kabibi.terminalProfile'), true);

  // Run one-shot command `-c "cat /workspace/hello.txt && echo done > /workspace/out.txt"`
  // via createKabibiPseudoterminal to verify full guest -> host -> vscode.workspace.fs round-trip!
  let terminalOutput = '';
  let exitCode = null;
  let resolveExit;
  let timeoutId;
  const exitPromise = new Promise((resolve, reject) => {
    resolveExit = (code) => {
      clearTimeout(timeoutId);
      resolve(code);
    };
    timeoutId = setTimeout(
      () => reject(new Error(`Timeout waiting for Kabibi exit. Output so far:\n${terminalOutput}`)),
      15000
    );
  });

  const pty = ext.createKabibiPseudoterminal(vscode, context, {
    args: ['kabibi', '-c', 'cat /workspace/hello.txt; echo written-by-kabibi > /workspace/out.txt']
  });

  pty.onDidWrite((chunk) => {
    terminalOutput += chunk;
  });
  pty.onDidClose((code) => {
    exitCode = code;
    resolveExit(code);
  });

  await pty.open({ columns: 100, rows: 30 });
  await exitPromise;

  assert.equal(exitCode, 0, `Expected exit code 0, got ${exitCode}. Output:\n${terminalOutput}`);
  assert.ok(
    terminalOutput.includes('Hello from mounted VSCode VFS!'),
    `Expected output to contain workspace file contents, got:\n${terminalOutput}`
  );

  const writtenFile = vscode._debug.wsFiles.get('/project/out.txt');
  assert.ok(writtenFile, 'Expected /workspace/out.txt to be written to VS Code workspace');
  assert.equal(
    new TextDecoder().decode(writtenFile.content).trim(),
    'written-by-kabibi'
  );
});

test('KabibiPseudoterminal interactive TUI renders startup banner, executes shell commands, and exits cleanly', async () => {
  const vscode = createMockVSCode({
    initialWorkspaceFiles: {
      '/project/greeting.txt': 'Interactive TUI workspace test!\n'
    }
  });

  const context = {
    extensionUri: vscode._debug.extensionUri,
    subscriptions: []
  };

  let terminalOutput = '';
  let exitCode = null;
  let resolveExit;
  let timeoutId;
  const exitPromise = new Promise((resolve, reject) => {
    resolveExit = (code) => {
      clearTimeout(timeoutId);
      resolve(code);
    };
    timeoutId = setTimeout(
      () => reject(new Error(`Timeout waiting for interactive Kabibi exit. Output:\n${terminalOutput}`)),
      15000
    );
  });

  const pty = ext.createKabibiPseudoterminal(vscode, context);
  let sentCommands = false;

  pty.onDidWrite((chunk) => {
    terminalOutput += chunk;
    if (!sentCommands && terminalOutput.includes('K A B I B I')) {
      sentCommands = true;
      // Resize terminal to verify SIGWINCH (27 + 28) handling
      pty.setDimensions({ columns: 120, rows: 40 });
      // Send a shell command that reads /workspace/greeting.txt and writes /workspace/tui-done.txt, then exits
      setTimeout(() => {
        pty.handleInput('cp /workspace/greeting.txt /workspace/tui-done.txt\r');
        setTimeout(() => {
          pty.handleInput('exit\r');
        }, 150);
      }, 50);
    }
  });

  pty.onDidClose((code) => {
    exitCode = code;
    resolveExit(code);
  });

  await pty.open({ columns: 80, rows: 24 });
  await exitPromise;

  assert.equal(exitCode, 0);
  assert.ok(sentCommands, 'Interactive Kabibi TUI should render startup banner');
  const copiedFile = vscode._debug.wsFiles.get('/project/tui-done.txt');
  assert.ok(copiedFile, 'Interactive command should have copied /workspace/greeting.txt to /workspace/tui-done.txt');
  assert.equal(
    new TextDecoder().decode(copiedFile.content),
    'Interactive TUI workspace test!\n'
  );
});

