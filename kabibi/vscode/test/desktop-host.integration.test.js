const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');

const extensionModule = require('../extension.js');
const extensionPath = path.resolve(__dirname, '..');

class MockUri {
  constructor(fsPath) {
    this.fsPath = fsPath;
    this.path = fsPath.replace(/\\/g, '/');
    this.scheme = 'file';
  }

  static joinPath(base, ...segments) {
    return new MockUri(path.join(base.fsPath, ...segments));
  }

  static file(fsPath) {
    return new MockUri(fsPath);
  }
}

class MockEventEmitter {
  constructor() {
    this.listeners = new Set();
    this.event = (listener) => {
      this.listeners.add(listener);
      return { dispose: () => this.listeners.delete(listener) };
    };
  }

  fire(value) {
    for (const listener of this.listeners) listener(value);
  }
}

function createMockVSCode(workspacePath) {
  return {
    UIKind: { Desktop: 1, Web: 2 },
    env: { uiKind: 1 },
    Uri: MockUri,
    EventEmitter: MockEventEmitter,
    TerminalProfile: class { constructor(value) { Object.assign(this, value); } },
    LanguageModelTextPart: class { constructor(value) { this.value = value; } },
    LanguageModelToolResult: class { constructor(value) { this.content = value; } },
    lm: {
      registerTool: () => ({ dispose() {} })
    },
    commands: {
      registerCommand: () => ({ dispose() {} })
    },
    window: {
      registerTerminalProfileProvider: () => ({ dispose() {} }),
      showTextDocument: async () => {}
    },
    workspace: {
      workspaceFolders: [{
        name: 'project',
        uri: new MockUri(workspacePath)
      }],
      fs: {
        readFile: (uri) => fs.readFile(uri.fsPath)
      },
      openTextDocument: async (uri) => ({ uri })
    }
  };
}

test('Desktop extension runs the freshly built checkout vegetable in-process', async (t) => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'kabibi-desktop-integration-'));
  t.after(() => fs.rm(workspace, { recursive: true, force: true }));
  const vscode = createMockVSCode(workspace);
  const context = {
    extensionUri: new MockUri(extensionPath),
    subscriptions: []
  };
  const extension = {};
  extensionModule.registerVscodeExtension(extension, vscode);
  extension.activate(context);

  const originalCwd = process.cwd();
  let output = '';
  let resolveExit;
  const exitPromise = new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`Timed out waiting for Kabibi. Output:\n${output}`)),
      20000
    );
    resolveExit = (code) => {
      clearTimeout(timer);
      resolve(code);
    };
  });
  const pty = extension.createKabibiPseudoterminal(vscode, context, {
    args: ['kabibi', '-c', 'pwd; cd ..; pwd']
  });
  pty.onDidWrite((chunk) => { output += chunk; });
  pty.onDidClose(resolveExit);
  await pty.open({ columns: 100, rows: 30 });

  assert.equal(await exitPromise, 0, output);
  assert.ok(output.includes(workspace), output);
  assert.ok(output.includes(path.dirname(workspace)), output);
  assert.equal(process.cwd(), originalCwd);
});
