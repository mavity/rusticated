// @ts-check
/// <reference types="node" />
/// <reference lib="webworker" />

(function () {
  'use strict';

  const OPEN_TERMINAL_CMD = 'kabibi_openTerminal';
  const TERMINAL_PROFILE_ID = 'kabibi_terminalProfile';
  const TERMINAL_SHORT_NAME = '🐚︎ Kb';
  const FILE_TYPE_FILE = 1;
  const FILE_TYPE_DIRECTORY = 2;
  const FILE_TYPE_SYMLINK = 64;

  const TOOL_DEFINITIONS = [
    { name: 'kabibi_exec', operation: 'exec' },
    { name: 'kabibi_term_read', operation: 'term_read' },
    { name: 'kabibi_term_input', operation: 'term_input' },
    { name: 'kabibi_term_kill', operation: 'term_kill' }
  ];

  let runtimeModulesPromise = null;
  let nodeWashmhostPromise = null;
  let unpackedWasmPromise = null;
  let developmentVegetablePath = null;
  let toolBridgePromise = null;
  const activePseudoterminals = new Set();

  function isNodeExtensionHost(vscode) {
    const isWebUI = vscode.UIKind &&
      vscode.env &&
      vscode.env.uiKind === vscode.UIKind.Web;
    return !isWebUI && typeof process !== 'undefined' &&
      process.versions && process.versions.node;
  }

  function extensionShellArgs(options) {
    const args = Array.isArray(options && options.args) && options.args.length > 0
      ? options.args.slice()
      : ['kabibi'];
    if (!args.includes('--disable-ai')) args.push('--disable-ai');
    return args;
  }

  function normalizePath(value) {
    const parts = [];
    for (const segment of String(value || '/').trim().replace(/\\/g, '/').split('/')) {
      if (!segment || segment === '.') continue;
      if (segment === '..') {
        if (parts.length > 0) parts.pop();
      } else {
        parts.push(segment);
      }
    }
    return '/' + parts.join('/');
  }

  function workspaceFolderEntries(folders) {
    const usedNames = new Set();
    return folders.map((folder, index) => {
      const baseName = folder.name || `folder-${index + 1}`;
      let name = baseName;
      let suffix = 2;
      while (usedNames.has(name)) name = `${baseName}-${suffix++}`;
      usedNames.add(name);
      return { folder, name };
    });
  }

  function mapVSCodeFileType(type) {
    if ((type & FILE_TYPE_DIRECTORY) !== 0) return 'dir';
    if ((type & FILE_TYPE_SYMLINK) !== 0) return 'symlink';
    return 'file';
  }

  function isNotFoundError(err) {
    if (!err) return false;
    const code = String(err.code || err.name || '');
    const message = String(err.message || '');
    return code === 'FileNotFound' || code === 'EntryNotFound' ||
      code === 'ENOENT' || code === 'NotFoundError' ||
      message.includes('EntryNotFound') || message.includes('FileNotFound') ||
      message.includes('ENOENT');
  }

  function translateError(err) {
    if (!err) return err;
    if (isNotFoundError(err)) {
      const error = new Error(err.message || 'ENOENT');
      error.code = 'ENOENT';
      return error;
    }
    const code = String(err.code || err.name || '');
    const translated = {
      FileExists: 'EEXIST',
      EntryExists: 'EEXIST',
      FileNotADirectory: 'ENOTDIR',
      EntryNotADirectory: 'ENOTDIR',
      FileIsADirectory: 'EISDIR',
      EntryIsADirectory: 'EISDIR',
      NoPermissions: 'EACCES'
    }[code];
    if (!translated) return err;
    const error = new Error(err.message || translated);
    error.code = translated;
    return error;
  }

  class VSCodeMountedFS {
    constructor(options) {
      if (!options || !options.vscode || !options.baseFS) {
        throw new Error('VSCodeMountedFS requires vscode and baseFS');
      }
      this.vscode = options.vscode;
      this.baseFS = options.baseFS;
      this.mountPoint = normalizePath(options.mountPoint || '/workspace');
      this.modeOverrides = new Map();
    }

    getWorkspaceFolders() {
      const folders = this.vscode.workspace && this.vscode.workspace.workspaceFolders;
      return Array.isArray(folders) ? folders : [];
    }

    getWorkspaceFolderEntries() {
      return workspaceFolderEntries(this.getWorkspaceFolders());
    }

    hasWorkspace() {
      return this.getWorkspaceFolders().length > 0;
    }

    resolveMount(guestPath) {
      const normalized = normalizePath(guestPath);
      const folders = this.getWorkspaceFolders();
      if (folders.length === 0 ||
          (normalized !== this.mountPoint && !normalized.startsWith(this.mountPoint + '/'))) {
        return { kind: 'base', path: normalized };
      }

      const relative = normalized === this.mountPoint
        ? ''
        : normalized.slice(this.mountPoint.length + 1);
      const segments = relative ? relative.split('/').filter(Boolean) : [];
      if (folders.length === 1) {
        const folder = folders[0];
        const uri = segments.length === 0
          ? folder.uri
          : this.vscode.Uri.joinPath(folder.uri, ...segments);
        return { kind: 'vscode', path: normalized, uri, folder, isMountRoot: segments.length === 0 };
      }
      if (segments.length === 0) {
        return { kind: 'multi-root', path: normalized, folders };
      }

      const entry = workspaceFolderEntries(folders).find(({ name }) => name === segments[0]);
      if (!entry) return { kind: 'not-found', path: normalized };
      const rest = segments.slice(1);
      const uri = rest.length === 0
        ? entry.folder.uri
        : this.vscode.Uri.joinPath(entry.folder.uri, ...rest);
      return {
        kind: 'vscode',
        path: normalized,
        uri,
        folder: entry.folder,
        isMountRoot: rest.length === 0
      };
    }

    async init() {
      await this.baseFS.init();
    }

    async seed(extraEntries = []) {
      const now = Date.now();
      const encoder = new TextEncoder();
      const codeHelper =
        '#!/bin/sh\n' +
        'for f in "$@"; do\n' +
        '  case "$f" in /*) p="$f" ;; *) p="$PWD/$f" ;; esac\n' +
        '  printf "globalThis.__kabibiOpenInVSCode && globalThis.__kabibiOpenInVSCode(%s)" "\\"$p\\"" > /dev/eval\n' +
        'done\n';
      const bytes = encoder.encode(codeHelper);
      await this.baseFS.seed([
        { path: this.mountPoint, type: 'dir', mode: 0o755, mtime: now },
        { path: '/bin/code', type: 'file', content: bytes, size: bytes.length, mode: 0o755, mtime: now },
        ...extraEntries
      ]);
      if (!(await this.baseFS.getEntry(this.mountPoint))) {
        await this.baseFS.putEntry({
          path: this.mountPoint,
          type: 'dir',
          mode: 0o755,
          mtime: now
        });
      }
    }

    async statEntry(guestPath) {
      const target = this.resolveMount(guestPath);
      if (target.kind === 'base') return (await this.baseFS.getEntry(target.path)) || null;
      if (target.kind === 'not-found') return null;
      if (target.kind === 'multi-root') {
        return { path: target.path, type: 'dir', size: 0, mode: 0o755, mtime: Date.now() };
      }
      try {
        const stat = await this.vscode.workspace.fs.stat(target.uri);
        const type = mapVSCodeFileType(stat.type);
        return {
          path: target.path,
          type,
          size: Number(stat.size || 0),
          mode: this.modeOverrides.get(target.path) ?? (type === 'dir' ? 0o755 : 0o644),
          mtime: Number(stat.mtime || Date.now())
        };
      } catch (err) {
        if (isNotFoundError(err)) return null;
        throw translateError(err);
      }
    }

    async getEntry(guestPath) {
      const target = this.resolveMount(guestPath);
      if (target.kind === 'base') return this.baseFS.getEntry(target.path);
      if (target.kind === 'not-found') return null;
      if (target.kind === 'multi-root') {
        return { path: target.path, type: 'dir', size: 0, mode: 0o755, mtime: Date.now() };
      }
      try {
        const stat = await this.vscode.workspace.fs.stat(target.uri);
        const type = mapVSCodeFileType(stat.type);
        const mode = this.modeOverrides.get(target.path) ?? (type === 'dir' ? 0o755 : 0o644);
        if (type === 'dir') {
          return {
            path: target.path,
            type,
            size: 0,
            mode,
            mtime: Number(stat.mtime || Date.now())
          };
        }
        const content = new Uint8Array(await this.vscode.workspace.fs.readFile(target.uri));
        return {
          path: target.path,
          type: 'file',
          content,
          size: content.length,
          mode,
          mtime: Number(stat.mtime || Date.now())
        };
      } catch (err) {
        if (isNotFoundError(err)) return null;
        throw translateError(err);
      }
    }

    async putEntry(entry) {
      if (!entry || !entry.path) throw new Error('putEntry requires entry.path');
      const normalized = normalizePath(entry.path);
      const target = this.resolveMount(normalized);
      if (target.kind === 'base') {
        return this.baseFS.putEntry({ ...entry, path: normalized });
      }
      if (target.kind === 'multi-root' || target.kind === 'not-found') {
        const error = new Error('EACCES: cannot write directly to multi-root container');
        error.code = 'EACCES';
        throw error;
      }
      if (typeof entry.mode === 'number') this.modeOverrides.set(normalized, entry.mode);
      try {
        if (entry.type === 'dir') {
          await this.vscode.workspace.fs.createDirectory(target.uri);
          return;
        }
        const raw = entry.content instanceof Uint8Array
          ? entry.content
          : new Uint8Array(entry.content || []);
        const size = typeof entry.size === 'number' && entry.size <= raw.length
          ? entry.size
          : raw.length;
        await this.vscode.workspace.fs.writeFile(target.uri, raw.subarray(0, size));
      } catch (err) {
        throw translateError(err);
      }
    }

    async removeEntry(guestPath) {
      const target = this.resolveMount(guestPath);
      if (target.kind === 'base') return this.baseFS.removeEntry(target.path);
      if (target.kind === 'not-found') return false;
      if (target.kind === 'multi-root' || target.isMountRoot) {
        const error = new Error('EBUSY: cannot remove workspace root');
        error.code = 'EBUSY';
        throw error;
      }
      try {
        await this.vscode.workspace.fs.delete(target.uri, { recursive: false, useTrash: false });
        this.modeOverrides.delete(target.path);
        return true;
      } catch (err) {
        if (isNotFoundError(err)) return false;
        throw translateError(err);
      }
    }

    async renameEntry(sourcePath, destinationPath) {
      const source = this.resolveMount(sourcePath);
      const destination = this.resolveMount(destinationPath);
      if (source.kind === 'vscode' && destination.kind === 'vscode') {
        try {
          await this.vscode.workspace.fs.rename(source.uri, destination.uri, { overwrite: true });
          if (this.modeOverrides.has(source.path)) {
            this.modeOverrides.set(destination.path, this.modeOverrides.get(source.path));
            this.modeOverrides.delete(source.path);
          }
          return true;
        } catch (err) {
          if (isNotFoundError(err)) return false;
          throw translateError(err);
        }
      }
      const entry = await this.getEntry(sourcePath);
      if (!entry) return false;
      entry.path = normalizePath(destinationPath);
      await this.putEntry(entry);
      await this.removeEntry(sourcePath);
      return true;
    }

    async readdir(dirPath) {
      const normalized = normalizePath(dirPath);
      if (normalized === '/') {
        const children = await this.baseFS.readdir('/');
        if (!children.some((child) => child.path === this.mountPoint)) {
          children.push({
            path: this.mountPoint,
            type: 'dir',
            size: 0,
            mode: 0o755,
            mtime: Date.now()
          });
        }
        return children;
      }
      const target = this.resolveMount(normalized);
      if (target.kind === 'base') return this.baseFS.readdir(target.path);
      if (target.kind === 'not-found') {
        const error = new Error('ENOENT');
        error.code = 'ENOENT';
        throw error;
      }
      if (target.kind === 'multi-root') {
        const now = Date.now();
        return workspaceFolderEntries(target.folders).map(({ name }) => ({
          path: `${this.mountPoint}/${name}`,
          type: 'dir',
          size: 0,
          mode: 0o755,
          mtime: now
        }));
      }
      try {
        const pairs = await this.vscode.workspace.fs.readDirectory(target.uri);
        const now = Date.now();
        return pairs.map(([name, fileType]) => {
          const path = `${target.path === '/' ? '' : target.path}/${name}`;
          const type = mapVSCodeFileType(fileType);
          return {
            path,
            type,
            size: 0,
            mode: this.modeOverrides.get(path) ?? (type === 'dir' ? 0o755 : 0o644),
            mtime: now
          };
        });
      } catch (err) {
        throw translateError(err);
      }
    }
  }

  function findDevelopmentVegetable(extensionPath, fsApi, pathApi) {
    if (!extensionPath) return null;
    const fs = fsApi || require('node:fs');
    const path = pathApi || require('node:path');
    const resolvedExtensionPath = path.resolve(extensionPath);
    if (path.basename(resolvedExtensionPath).toLowerCase() !== 'vscode' ||
        path.basename(path.dirname(resolvedExtensionPath)).toLowerCase() !== 'kabibi') {
      return null;
    }
    const repositoryRoot = path.resolve(resolvedExtensionPath, '..', '..');
    if (!fs.existsSync(path.join(repositoryRoot, 'go.mod')) ||
        !fs.existsSync(path.join(resolvedExtensionPath, 'package.json'))) {
      return null;
    }
    return path.join(repositoryRoot, 'release', 'dist', 'kabibi.bat');
  }

  async function ensureRuntimeModules(vscode, extensionUri) {
    if (globalThis.washmhostExports) {
      return { washmhostExports: globalThis.washmhostExports };
    }
    if (!runtimeModulesPromise) {
      runtimeModulesPromise = (async () => {
        const uri = vscode.Uri.joinPath(extensionUri, 'washmhost.js');
        const bytes = await vscode.workspace.fs.readFile(uri);
        (0, eval)(new TextDecoder('utf-8').decode(bytes));
        if (!globalThis.washmhostExports) {
          throw new Error('Failed to initialize washmhostExports');
        }
        return { washmhostExports: globalThis.washmhostExports };
      })().catch((err) => {
        runtimeModulesPromise = null;
        throw err;
      });
    }
    return runtimeModulesPromise;
  }

  async function ensureNodeWashmhost(extensionPath) {
    if (!nodeWashmhostPromise) {
      nodeWashmhostPromise = (async () => {
        const path = require('node:path');
        const { pathToFileURL } = require('node:url');
        const modulePath = developmentVegetablePath
          ? path.resolve(extensionPath, '..', '..', 'mohabbat', 'washmhost', 'js', 'node', 'index.js')
          : path.join(extensionPath, 'washmhost-node.mjs');
        return import(pathToFileURL(modulePath).href);
      })().catch((err) => {
        nodeWashmhostPromise = null;
        throw new Error(`Unable to load Node washmhost module: ${err.message}`);
      });
    }
    return nodeWashmhostPromise;
  }

  async function loadCachedKabibiWasm(vscode, extensionUri, washmhostExports, options = {}) {
    if (!unpackedWasmPromise) {
      unpackedWasmPromise = (async () => {
        const devPath = options.developmentVegetablePath !== undefined
          ? options.developmentVegetablePath
          : developmentVegetablePath;
        let rawBytes;
        if (devPath) {
          rawBytes = await require('node:fs/promises').readFile(devPath);
        } else {
          let lastError = null;
          for (const name of ['kabibi.bat', 'kabibi.wasm']) {
            try {
              rawBytes = await vscode.workspace.fs.readFile(
                vscode.Uri.joinPath(extensionUri, name)
              );
              if (rawBytes && rawBytes.byteLength > 0) break;
            } catch (err) {
              lastError = err;
            }
          }
          if (!rawBytes || rawBytes.byteLength === 0) {
            throw new Error(
              `Unable to read packaged kabibi.bat or kabibi.wasm: ${lastError ? lastError.message : 'not found'}`
            );
          }
        }
        const bytes = rawBytes instanceof Uint8Array ? rawBytes : new Uint8Array(rawBytes);
        if (bytes.length >= 4 &&
            bytes[0] === 0 && bytes[1] === 0x61 && bytes[2] === 0x73 && bytes[3] === 0x6d) {
          return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
        }
        return washmhostExports.unpackVegetable(bytes);
      })().catch((err) => {
        unpackedWasmPromise = null;
        throw err;
      });
    }
    return unpackedWasmPromise;
  }

  function createProtocolState() {
    const pending = new Map();
    let nextRequestId = 1;
    let closed = false;
    let failure = null;
    let resolveReady;
    let rejectReady;
    const ready = new Promise((resolve, reject) => {
      resolveReady = resolve;
      rejectReady = reject;
    });
    return {
      pending,
      ready,
      isClosed: () => closed,
      setReady: () => resolveReady(),
      setFailure: (err) => {
        if (closed) return;
        closed = true;
        failure = err;
        rejectReady(err);
        for (const request of pending.values()) request.reject(err);
        pending.clear();
      },
      getFailure: () => failure,
      nextID: () => `vscode-${nextRequestId++}`
    };
  }

  function consumeProtocolOutput(state) {
    const decoder = new TextDecoder('utf-8');
    let buffer = '';
    return (bytes) => {
      buffer += typeof bytes === 'string' ? bytes : decoder.decode(bytes, { stream: true });
      const lines = buffer.split(/\r?\n/);
      buffer = lines.pop() || '';
      for (const line of lines) {
        if (!line) continue;
        let response;
        try {
          response = JSON.parse(line);
        } catch (err) {
          state.setFailure(new Error(`Kabibi tool server returned invalid JSON: ${err.message}`));
          continue;
        }
        if (response.type === 'ready') {
          state.setReady();
          continue;
        }
        const request = state.pending.get(response.request_id);
        if (!request) {
          state.setFailure(new Error(
            `Kabibi tool server replied to unknown request ${response.request_id || '(missing ID)'}`
          ));
          continue;
        }
        state.pending.delete(response.request_id);
        if (response.error) request.reject(new Error(response.error));
        else request.resolve(response.result);
      }
    };
  }

  function workspaceCwd(vscode) {
    const folders = vscode.workspace.workspaceFolders || [];
    return folders.length > 0 && folders[0].uri.fsPath
      ? folders[0].uri.fsPath
      : require('node:os').homedir();
  }

  async function createNodeWashmhost(vscode, context, options) {
    const fs = require('node:fs');
    const path = require('node:path');
    const { washmhostExports } = await ensureRuntimeModules(vscode, context.extensionUri);
    const [nodeModule, wasm] = await Promise.all([
      ensureNodeWashmhost(context.extensionUri.fsPath),
      loadCachedKabibiWasm(vscode, context.extensionUri, washmhostExports)
    ]);
    const addonPath = path.join(
      context.extensionUri.fsPath,
      `nodepty-${process.platform}-${process.arch}.node`
    );
    if (!fs.existsSync(addonPath)) {
      throw new Error(`Node PTY addon is missing: ${addonPath}`);
    }
    const host = nodeModule.createNodeWashmhost({
      args: options.args,
      cwd: workspaceCwd(vscode),
      env: {
        ...process.env,
        TERM: 'xterm-256color',
        COLORTERM: 'truecolor',
        HOME: process.env.HOME || require('node:os').homedir()
      },
      stdin: options.stdin,
      stdout: options.stdout,
      stderr: options.stderr,
      terminalSize: options.terminalSize,
      terminalHandles: options.terminalHandles,
      ptyAddon: require(addonPath)
    });
    return { host, wasm };
  }

  function configureNodeTerminalImports(host, getDimensions, setDimensions) {
    const defaultIsatty = host.env.fd_isatty;
    const defaultGetSize = host.env.tty_get_size;
    const defaultSetSize = host.env.tty_set_size;
    const defaultSetMode = host.env.tty_set_mode;
    host.env.process_exit = (code) => host.exitGuest(code);
    host.env.fd_isatty = (fd) =>
      Number(fd) >= 0 && Number(fd) <= 2 ? 1 : defaultIsatty(fd);
    host.env.tty_get_size = (handle) => {
      if (Number(handle) >= 0 && Number(handle) <= 2) {
        const size = getDimensions();
        return (size.cols << 16) | size.rows;
      }
      return defaultGetSize(handle);
    };
    host.env.tty_set_size = (handle, cols, rows) => {
      if (Number(handle) >= 0 && Number(handle) <= 2) {
        setDimensions(cols, rows);
        return 0;
      }
      return defaultSetSize(handle, cols, rows);
    };
    host.env.tty_set_mode = (handle, mode) => {
      if (BigInt(handle) >= 0n && BigInt(handle) <= 2n) return;
      return defaultSetMode(handle, mode);
    };
  }

  function registerNodeEditorBridge(vscode, cwd) {
    const path = require('node:path');
    globalThis.__kabibiOpenInVSCode = async (guestPath) => {
      try {
        let target = String(guestPath).replace(/\\/g, '/');
        if (/^\/[A-Za-z]:\//.test(target)) target = target.slice(1);
        if (!path.isAbsolute(target)) target = path.resolve(cwd, target);
        const document = await vscode.workspace.openTextDocument(vscode.Uri.file(target));
        await vscode.window.showTextDocument(document, { preview: false });
        return 'ok';
      } catch (err) {
        return String(err && err.message ? err.message : err);
      }
    };
  }

  function registerBrowserEditorBridge(vscode, vfs) {
    globalThis.__kabibiOpenInVSCode = async (guestPath) => {
      try {
        const resolved = vfs.resolveMount(guestPath);
        if (resolved.kind !== 'vscode' || !resolved.uri) return 'not-in-workspace';
        const document = await vscode.workspace.openTextDocument(resolved.uri);
        await vscode.window.showTextDocument(document, { preview: false });
        return 'ok';
      } catch (err) {
        return String(err && err.message ? err.message : err);
      }
    };
  }

  async function createNodeTerminalHost(vscode, context, emitBytes, onExit, cols, rows, args) {
    const { PassThrough, Writable } = require('node:stream');
    const input = new PassThrough();
    const output = new Writable({
      write(chunk, _encoding, callback) {
        try {
          emitBytes(Buffer.from(chunk));
          callback();
        } catch (err) {
          callback(err);
        }
      }
    });
    let terminalCols = cols;
    let terminalRows = rows;
    const runtime = await createNodeWashmhost(vscode, context, {
      args,
      stdin: input,
      stdout: output,
      stderr: output,
      terminalSize: { cols, rows },
      terminalHandles: [0n, 1n, 2n]
    });
    registerNodeEditorBridge(vscode, runtime.host.cwd);
    configureNodeTerminalImports(runtime.host, () => ({
      cols: terminalCols,
      rows: terminalRows
    }), (newCols, newRows) => {
      terminalCols = newCols;
      terminalRows = newRows;
    });
    const runPromise = runtime.host.run(runtime.wasm);
    runPromise.then(
      (code) => onExit(typeof code === 'number' ? code : 0),
      (err) => {
        emitBytes(`\r\n[Kabibi host error: ${err.message}]\r\n`);
        onExit(1);
      }
    );
    return {
      pushStdin: (data) => input.write(data),
      triggerSignal: (signum) => runtime.host.signal(signum),
      setTtySize: (newCols, newRows) => {
        terminalCols = newCols;
        terminalRows = newRows;
        runtime.host.resize(newCols, newRows);
      },
      terminate: () => runtime.host.terminate(0)
    };
  }

  async function createBrowserToolBridge(vscode, context, state) {
    const { washmhostExports } = await ensureRuntimeModules(vscode, context.extensionUri);
    const vfs = new VSCodeMountedFS({
      vscode,
      baseFS: new washmhostExports.AsyncIndexedDBFS('KabibiVSCodeToolFS')
    });
    const wasm = await loadCachedKabibiWasm(vscode, context.extensionUri, washmhostExports);
    const consume = consumeProtocolOutput(state);
    const host = await washmhostExports.washmhost({
      wasm,
      fs: vfs,
      cwd: vfs.hasWorkspace() ? '/workspace' : '/home/user',
      args: ['kabibi', '--disable-ai', '--vscode-tool-server'],
      env: {
        TERM: 'xterm-256color',
        COLORTERM: 'truecolor',
        PATH: '/bin',
        HOME: '/home/user',
        PWD: vfs.hasWorkspace() ? '/workspace' : '/home/user'
      },
      stdout: consume,
      stderr: (bytes) => console.error('[Kabibi tool server]', bytes),
      onExit: (code) => {
        if (!state.isClosed()) {
          state.setFailure(new Error(`Kabibi tool server exited with code ${code}`));
        }
      }
    });
    return {
      state,
      send: (message) => host.pushStdin(`${JSON.stringify(message)}\n`),
      close: () => host.terminate(0)
    };
  }

  async function createNodeToolBridge(vscode, context, state) {
    const { PassThrough, Writable } = require('node:stream');
    const input = new PassThrough();
    const consume = consumeProtocolOutput(state);
    const stdout = new Writable({
      write(chunk, _encoding, callback) {
        consume(chunk);
        callback();
      }
    });
    const stderr = new Writable({
      write(chunk, _encoding, callback) {
        console.error('[Kabibi tool server]', Buffer.from(chunk).toString('utf8').trimEnd());
        callback();
      }
    });
    const runtime = await createNodeWashmhost(vscode, context, {
      args: ['kabibi', '--disable-ai', '--vscode-tool-server'],
      stdin: input,
      stdout,
      stderr
    });
    registerNodeEditorBridge(vscode, runtime.host.cwd);
    const runPromise = runtime.host.run(runtime.wasm);
    runPromise.then(
      (code) => {
        if (!state.isClosed()) {
          state.setFailure(new Error(`Kabibi tool server exited with code ${code}`));
        }
      },
      (err) => state.setFailure(new Error(`Kabibi tool server failed: ${err.message}`))
    );
    return {
      state,
      send: (message) => input.write(`${JSON.stringify(message)}\n`),
      close: async () => {
        runtime.host.terminate(0);
        await runPromise.catch(() => {});
      }
    };
  }

  async function getToolBridge(vscode, context) {
    if (!toolBridgePromise) {
      const state = createProtocolState();
      toolBridgePromise = (async () => {
        const bridge = isNodeExtensionHost(vscode)
          ? await createNodeToolBridge(vscode, context, state)
          : await createBrowserToolBridge(vscode, context, state);
        await state.ready;
        return bridge;
      })().catch((err) => {
        toolBridgePromise = null;
        throw err;
      });
    }
    return toolBridgePromise;
  }

  async function invokeTool(vscode, context, operation, input, token) {
    if (token && token.isCancellationRequested) {
      throw new Error('Kabibi tool invocation cancelled');
    }
    const bridge = await getToolBridge(vscode, context);
    if (bridge.state.isClosed()) {
      throw bridge.state.getFailure() || new Error('Kabibi tool server is closed');
    }
    const requestId = bridge.state.nextID();
    const response = new Promise((resolve, reject) => {
      bridge.state.pending.set(requestId, { resolve, reject });
    });
    const cancellation = token && token.onCancellationRequested(() => {
      try {
        bridge.send({ type: 'cancel', request_id: requestId });
      } catch (err) {
        const pending = bridge.state.pending.get(requestId);
        bridge.state.pending.delete(requestId);
        if (pending) {
          pending.reject(new Error(`Unable to cancel Kabibi tool invocation: ${err.message}`));
        }
      }
    });
    try {
      bridge.send({ type: 'invoke', request_id: requestId, name: operation, arguments: input });
      if (token && token.isCancellationRequested) {
        bridge.send({ type: 'cancel', request_id: requestId });
      }
      const result = await response;
      return new vscode.LanguageModelToolResult([
        new vscode.LanguageModelTextPart(JSON.stringify(result))
      ]);
    } catch (err) {
      bridge.state.pending.delete(requestId);
      throw err;
    } finally {
      if (cancellation) cancellation.dispose();
    }
  }

  function registerLanguageModelTools(vscode, context) {
    const registerTool = vscode.lm && (
      vscode.lm.registerTool || vscode.lm.registerLanguageModelTool
    );
    if (typeof registerTool !== 'function') {
      throw new Error('Kabibi requires the VS Code Language Model Tools API');
    }
    for (const definition of TOOL_DEFINITIONS) {
      const tool = {
        prepareInvocation: (options) => {
          const input = options.input || {};
          let message;
          if (definition.operation === 'exec') {
            message = `Run this command: ${input.command || '(empty command)'}`;
            if (input.explanation) message += `\n\nReason: ${input.explanation}`;
          } else {
            message = `${definition.operation} for Kabibi background job ${input.id}`;
          }
          return {
            invocationMessage: `Kabibi: ${definition.operation}`,
            confirmationMessages: {
              title: 'Run Kabibi terminal tool?',
              message
            }
          };
        },
        invoke: (options, token) => invokeTool(
          vscode,
          context,
          definition.operation,
          options.input || {},
          token
        )
      };
      context.subscriptions.push(registerTool.call(vscode.lm, definition.name, tool));
    }
  }

  function createKabibiPseudoterminal(vscode, context, options = {}) {
    const writeEmitter = new vscode.EventEmitter();
    const closeEmitter = new vscode.EventEmitter();
    const decoder = new TextDecoder('utf-8');
    let cols = 80;
    let rows = 24;
    let host = null;
    let closed = false;
    const earlyInputQueue = [];
    let resolveReady;
    let rejectReady;
    const readyPromise = new Promise((resolve, reject) => {
      resolveReady = resolve;
      rejectReady = reject;
    });
    const emitBytes = (bytes) => {
      if (closed) return;
      const text = typeof bytes === 'string'
        ? bytes
        : decoder.decode(bytes, { stream: true });
      if (text) writeEmitter.fire(text);
    };
    const finish = (code) => {
      if (closed) return;
      closed = true;
      closeEmitter.fire(code);
      activePseudoterminals.delete(pseudoterminal);
    };
    const pseudoterminal = {
      onDidWrite: writeEmitter.event,
      onDidClose: closeEmitter.event,
      open: async (dimensions) => {
        if (dimensions) {
          if (dimensions.columns > 0) cols = dimensions.columns;
          if (dimensions.rows > 0) rows = dimensions.rows;
        }
        try {
          const { washmhostExports } = await ensureRuntimeModules(vscode, context.extensionUri);
          const wasm = await loadCachedKabibiWasm(vscode, context.extensionUri, washmhostExports);
          const args = extensionShellArgs(options);
          if (isNodeExtensionHost(vscode)) {
            host = await createNodeTerminalHost(
              vscode,
              context,
              emitBytes,
              finish,
              cols,
              rows,
              args
            );
          } else {
            const vfs = options.vfs || new VSCodeMountedFS({
              vscode,
              baseFS: new washmhostExports.AsyncIndexedDBFS('KabibiVSCodeFS')
            });
            registerBrowserEditorBridge(vscode, vfs);
            const cwd = vfs.hasWorkspace() ? '/workspace' : '/home/user';
            host = await washmhostExports.washmhost({
              wasm,
              fs: vfs,
              cwd,
              args,
              env: {
                TERM: 'xterm-256color',
                COLORTERM: 'truecolor',
                PATH: '/bin',
                HOME: '/home/user',
                PWD: cwd
              },
              stdout: emitBytes,
              stderr: emitBytes,
              getTtySize: () => ({ cols, rows }),
              setTtySize: (newCols, newRows) => {
                cols = newCols;
                rows = newRows;
              },
              onExit: finish
            });
          }
          while (earlyInputQueue.length > 0) host.pushStdin(earlyInputQueue.shift());
          resolveReady(host);
        } catch (err) {
          emitBytes(`\r\n[Kabibi boot error: ${err.message || err}]\r\n`);
          rejectReady(err);
          finish(1);
        }
      },
      close: () => {
        if (closed) return;
        if (host) {
          try {
            host.triggerSignal(15);
            host.terminate(0);
          } catch (err) {
            console.error('Unable to close Kabibi terminal host:', err);
          }
        }
        finish(0);
      },
      handleInput: (data) => {
        if (closed) return;
        if (host) host.pushStdin(data);
        else earlyInputQueue.push(data);
      },
      setDimensions: (dimensions) => {
        if (!dimensions) return;
        if (dimensions.columns > 0) cols = dimensions.columns;
        if (dimensions.rows > 0) rows = dimensions.rows;
        if (host) {
          if (host.setTtySize) host.setTtySize(cols, rows);
          else {
            host.triggerSignal(27);
            host.triggerSignal(28);
          }
        }
      },
      getHost: () => host,
      waitReady: () => readyPromise
    };
    activePseudoterminals.add(pseudoterminal);
    return pseudoterminal;
  }

  function registerVscodeExtension(moduleExports, injectedVscode) {
    function activate(context) {
      const vscode = injectedVscode || require('vscode');
      if (isNodeExtensionHost(vscode)) {
        developmentVegetablePath = findDevelopmentVegetable(context.extensionUri.fsPath);
      }
      registerLanguageModelTools(vscode, context);
      context.subscriptions.push(
        vscode.commands.registerCommand(OPEN_TERMINAL_CMD, () => {
          const pty = createKabibiPseudoterminal(vscode, context);
          const terminal = vscode.window.createTerminal({ name: TERMINAL_SHORT_NAME, pty });
          terminal.show();
          return { terminal, pty };
        })
      );
      const provider = {
        provideTerminalProfile: () => {
          const pty = createKabibiPseudoterminal(vscode, context);
          return new vscode.TerminalProfile({
            name: TERMINAL_SHORT_NAME,
            pty,
            isTransient: false
          });
        }
      };
      context.subscriptions.push(
        vscode.window.registerTerminalProfileProvider(TERMINAL_PROFILE_ID, provider)
      );
    }

    async function deactivate() {
      for (const pty of [...activePseudoterminals]) pty.close();
      if (toolBridgePromise) {
        try {
          const bridge = await toolBridgePromise;
          await bridge.close();
        } catch (err) {
          console.error('Unable to close Kabibi tool server:', err);
        }
        toolBridgePromise = null;
      }
    }

    Object.assign(moduleExports, {
      registerVscodeExtension,
      activate,
      deactivate,
      createKabibiPseudoterminal,
      extensionShellArgs,
      findDevelopmentVegetable,
      loadCachedKabibiWasm,
      ensureRuntimeModules,
      registerLanguageModelTools,
      VSCodeMountedFS,
      workspaceFolderEntries
    });
  }

  const exportsObject = {
    registerVscodeExtension,
    createKabibiPseudoterminal,
    extensionShellArgs,
    findDevelopmentVegetable,
    loadCachedKabibiWasm,
    ensureRuntimeModules,
    registerLanguageModelTools,
    VSCodeMountedFS,
    normalizePath,
    workspaceFolderEntries,
    configureNodeTerminalImports
  };
  if (typeof module !== 'undefined' && module.exports) {
    registerVscodeExtension(module.exports);
  }
  if (typeof globalThis !== 'undefined') {
    globalThis.KabibiVSCodeExtension = exportsObject;
  }
})();
