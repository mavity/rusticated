// @ts-check
/// <reference types="node" />
/// <reference lib="webworker" />

/**
 * Kabibi Shell — Universal VS Code Web & Desktop Extension.
 *
 * Runs identically in Desktop VS Code, `vscode.dev`, `github.dev`, and OpenVSCode
 * clones using raw JavaScript (zero TypeScript / zero bundler).
 *
 * - Provides the `🐚︎ Kabibi` Terminal Profile (`kabibi.terminalProfile`) and
 *   `"🐚︎ Kabibi: Open Shell"` command (`kabibi.openTerminal`).
 * - Mounts the active VS Code workspace folder(s) at `/workspace` via
 *   `VSCodeMountedFS` (`vscode.workspace.fs` + `AsyncIndexedDBFS`).
 * - Unpacks the packaged `kabibi.bat` vegetable (or `kabibi.wasm`) in memory
 *   once and caches the unpacked WASM payload across terminal instances.
 */
(function () {
  'use strict';

  const EXTENSION_ID = 'kabibi';
  const OPEN_TERMINAL_CMD = 'kabibi.openTerminal';
  const TERMINAL_PROFILE_ID = 'kabibi.terminalProfile';
  const TERMINAL_SHORT_NAME = '🐚︎ Kb';

  function isNodeExtensionHost(vscode) {
    const isWebUI = vscode.UIKind &&
      vscode.env &&
      vscode.env.uiKind === vscode.UIKind.Web;
    return !isWebUI && typeof process !== 'undefined' && process.versions && process.versions.node;
  }

  function extensionShellArgs(options) {
    const args = Array.isArray(options && options.args) && options.args.length > 0
      ? options.args.slice()
      : ['kabibi'];
    if (!args.includes('--disable-ai')) args.push('--disable-ai');
    return args;
  }

  const TOOL_DEFINITIONS = [
    { name: 'kabibi_exec', operation: 'exec' },
    { name: 'kabibi_term_read', operation: 'term_read' },
    { name: 'kabibi_term_input', operation: 'term_input' },
    { name: 'kabibi_term_kill', operation: 'term_kill' }
  ];

  /** @type {Promise<any> | null} */
  let runtimeModulesPromise = null;

  /** @type {Promise<ArrayBuffer> | null} */
  let unpackedWasmPromise = null;

  /** @type {Set<any>} */
  const activePseudoterminals = new Set();
  /** @type {Promise<any> | null} */
  let toolBridgePromise = null;

  /**
   * Dynamically loads the washmhost mount adapter, `washmhost.js`, and `vfs.js`
   * from the extension directory via `vscode.workspace.fs.readFile` so it works
   * identically in `vscode.dev` and Desktop VS Code.
   *
   * @param {any} vscode
   * @param {import('vscode').Uri} extensionUri
   */
  async function ensureRuntimeModules(vscode, extensionUri) {
    if (globalThis.washmhostExports &&
        globalThis.WashmhostBrowserMountFS &&
        globalThis.KabibiVSCodeVFS) {
      return {
        washmhostExports: globalThis.washmhostExports,
        vfsExports: globalThis.KabibiVSCodeVFS
      };
    }

    if (!runtimeModulesPromise) {
      runtimeModulesPromise = (async () => {
        const decoder = new TextDecoder('utf-8');

        if (!globalThis.WashmhostBrowserMountFS) {
          const mountFSUri = vscode.Uri.joinPath(extensionUri, 'mount_fs.js');
          const mountFSBytes = await vscode.workspace.fs.readFile(mountFSUri);
          const mountFSCode = decoder.decode(mountFSBytes);
          (0, eval)(mountFSCode);
        }

        if (!globalThis.washmhostExports) {
          const whUri = vscode.Uri.joinPath(extensionUri, 'washmhost.js');
          const whBytes = await vscode.workspace.fs.readFile(whUri);
          const whCode = decoder.decode(whBytes);
          (0, eval)(whCode);
        }

        if (!globalThis.KabibiVSCodeVFS) {
          const vfsUri = vscode.Uri.joinPath(extensionUri, 'vfs.js');
          const vfsBytes = await vscode.workspace.fs.readFile(vfsUri);
          const vfsCode = decoder.decode(vfsBytes);
          (0, eval)(vfsCode);
        }

        if (!globalThis.washmhostExports ||
            !globalThis.WashmhostBrowserMountFS ||
            !globalThis.KabibiVSCodeVFS) {
          throw new Error('Failed to initialize washmhostExports, mount filesystem, or KabibiVSCodeVFS');
        }

        return {
          washmhostExports: globalThis.washmhostExports,
          vfsExports: globalThis.KabibiVSCodeVFS
        };
      })().catch((err) => {
        runtimeModulesPromise = null;
        throw err;
      });
    }

    return runtimeModulesPromise;
  }

  /**
   * Loads and unpacks `kabibi.bat` (or `kabibi.wasm`) from the extension bundle,
   * caching the resulting raw WASM `ArrayBuffer` for subsequent terminals.
   *
   * @param {any} vscode
   * @param {import('vscode').Uri} extensionUri
   * @param {any} washmhostExports
   * @returns {Promise<ArrayBuffer>}
   */
  async function loadCachedKabibiWasm(vscode, extensionUri, washmhostExports) {
    if (!unpackedWasmPromise) {
      unpackedWasmPromise = (async () => {
        const candidates = ['kabibi.bat', 'kabibi.wasm'];
        let rawBytes = null;
        let lastErr = null;

        for (const name of candidates) {
          try {
            const uri = vscode.Uri.joinPath(extensionUri, name);
            rawBytes = await vscode.workspace.fs.readFile(uri);
            if (rawBytes && rawBytes.byteLength > 0) break;
          } catch (err) {
            lastErr = err;
          }
        }

        if (!rawBytes || rawBytes.byteLength === 0) {
          throw new Error(
            `Unable to read packaged kabibi.bat or kabibi.wasm from extension: ${lastErr ? lastErr.message : 'not found'}`
          );
        }

        const u8 = rawBytes instanceof Uint8Array
          ? rawBytes
          : new Uint8Array(rawBytes);

        const isRawWasm =
          u8.byteLength >= 4 &&
          u8[0] === 0x00 &&
          u8[1] === 0x61 &&
          u8[2] === 0x73 &&
          u8[3] === 0x6d;

        if (isRawWasm) {
          return u8.buffer.slice(u8.byteOffset, u8.byteOffset + u8.byteLength);
        }

        return await washmhostExports.unpackVegetable(u8);
      })().catch((err) => {
        unpackedWasmPromise = null;
        throw err;
      });
    }

    return unpackedWasmPromise;
  }

  function parseToolProtocolLine(line, pending, fail) {
    let response;
    try {
      response = JSON.parse(line);
    } catch (err) {
      fail(new Error(`Kabibi tool server returned invalid JSON: ${err.message}`));
      return;
    }
    if (response.type === 'ready') return;
    const request = pending.get(response.request_id);
    if (!request) {
      fail(new Error(`Kabibi tool server replied to unknown request ${response.request_id || '(missing ID)'}`));
      return;
    }
    pending.delete(response.request_id);
    if (response.error) request.reject(new Error(response.error));
    else request.resolve(response.result);
  }

  function createProtocolState() {
    const pending = new Map();
    let nextRequestID = 1;
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
      nextID: () => `vscode-${nextRequestID++}`
    };
  }

  async function createBrowserToolBridge(vscode, context, state) {
    const { washmhostExports, vfsExports } = await ensureRuntimeModules(
      vscode,
      context.extensionUri
    );
    const vfs = new vfsExports.VSCodeMountedFS({
      vscode,
      baseFS: new washmhostExports.AsyncIndexedDBFS('KabibiVSCodeToolFS'),
      mountPoint: '/workspace'
    });
    const wasm = await loadCachedKabibiWasm(vscode, context.extensionUri, washmhostExports);
    const decoder = new TextDecoder('utf-8');
    let outputBuffer = '';
    let host = null;

    const consume = (bytes) => {
      outputBuffer += typeof bytes === 'string'
        ? bytes
        : decoder.decode(bytes, { stream: true });
      const lines = outputBuffer.split(/\r?\n/);
      outputBuffer = lines.pop() || '';
      for (const line of lines) {
        if (!line) continue;
        try {
          const response = JSON.parse(line);
          if (response.type === 'ready') state.setReady();
          else parseToolProtocolLine(line, state.pending, state.setFailure);
        } catch (err) {
          state.setFailure(new Error(`Kabibi tool server response failed: ${err.message}`));
        }
      }
    };

    host = await washmhostExports.washmhost({
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
      stderr: (bytes) => console.error(
        '[Kabibi tool server]',
        typeof bytes === 'string' ? bytes : decoder.decode(bytes, { stream: true })
      ),
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

  async function createNodeWorkspaceMount(vscode, context, vfs) {
    const entries = vfs.getWorkspaceFolderEntries();
    if (entries.length === 0) return null;

    const fs = require('node:fs/promises');
    const path = require('node:path');
    const os = require('node:os');
    const storagePath = context.globalStorageUri && context.globalStorageUri.fsPath
      ? context.globalStorageUri.fsPath
      : os.tmpdir();
    await fs.mkdir(storagePath, { recursive: true });
    const parent = await fs.mkdtemp(path.join(storagePath, 'kabibi-workspace-'));
    const mountPath = path.join(parent, 'workspace');
    const roots = [];
    try {
      if (entries.length === 1) {
        const hostPath = entries[0].folder.uri.fsPath;
        await fs.symlink(hostPath, mountPath, process.platform === 'win32' ? 'junction' : 'dir');
        roots.push({ host: hostPath, guest: '/workspace' });
        roots.push({ host: mountPath, guest: '/workspace' });
      } else {
        await fs.mkdir(mountPath);
        roots.push({ host: mountPath, guest: '/workspace' });
        for (const entry of entries) {
          const hostPath = entry.folder.uri.fsPath;
          await fs.symlink(
            hostPath,
            path.join(mountPath, entry.name),
            process.platform === 'win32' ? 'junction' : 'dir'
          );
          roots.push({ host: hostPath, guest: `/workspace/${entry.name}` });
        }
      }
      return {
        mountPath,
        roots,
        dispose: async () => {
          await fs.rm(parent, { recursive: true, force: true });
        }
      };
    } catch (err) {
      await fs.rm(parent, { recursive: true, force: true });
      throw new Error(`Unable to mount VS Code workspace for Kabibi: ${err.message}`);
    }
  }

  async function prepareNodeHostRuntime(vscode, context, purpose) {
    const fs = require('node:fs/promises');
    const path = require('node:path');
    const os = require('node:os');
    const { washmhostExports, vfsExports } = await ensureRuntimeModules(vscode, context.extensionUri);
    const vfs = new vfsExports.VSCodeMountedFS({
      vscode,
      baseFS: { init: async () => {}, seed: async () => {}, getEntry: async () => null },
      mountPoint: '/workspace'
    });
    const workspaceMount = await createNodeWorkspaceMount(vscode, context, vfs);
    const storagePath = context.globalStorageUri && context.globalStorageUri.fsPath
      ? context.globalStorageUri.fsPath
      : os.tmpdir();
    const wasmPath = path.join(storagePath, `kabibi-${purpose}.wasm`);

    const extensionPath = context.extensionUri.fsPath;
    const hostPath = path.join(extensionPath, 'washmhost-node.mjs');
    const addonName = `nodepty-${process.platform}-${process.arch}.node`;
    const addonPath = path.join(extensionPath, addonName);
    try {
      await fs.mkdir(storagePath, { recursive: true });
      const wasm = await loadCachedKabibiWasm(vscode, context.extensionUri, washmhostExports);
      await fs.writeFile(wasmPath, new Uint8Array(wasm));
      await fs.access(hostPath);
      await fs.access(addonPath);
    } catch (err) {
      if (workspaceMount) await workspaceMount.dispose();
      await fs.rm(wasmPath, { force: true });
      throw new Error(`Unable to prepare Kabibi Node runtime: ${err.message}`);
    }

    const childEnv = { ...process.env, MOHABBAT_NODE_PTY_ADDON: addonPath };
    if (workspaceMount) {
      childEnv.MOHABBAT_WORKSPACE_MOUNT = workspaceMount.mountPath;
      childEnv.MOHABBAT_WORKSPACE_ROOTS = JSON.stringify(workspaceMount.roots);
    }
    return {
      fs,
      hostPath,
      wasmPath,
      childEnv,
      cwd: workspaceMount ? workspaceMount.mountPath : os.homedir(),
      dispose: async () => {
        if (workspaceMount) await workspaceMount.dispose();
        await fs.rm(wasmPath, { force: true });
      }
    };
  }

  async function createNodeToolBridge(vscode, context, state) {
    const { spawn } = require('node:child_process');
    const runtime = await prepareNodeHostRuntime(vscode, context, 'tool-server');
    const child = spawn(
      process.execPath,
      [runtime.hostPath, runtime.wasmPath, '--disable-ai', '--vscode-tool-server'],
      {
        cwd: runtime.cwd,
        env: runtime.childEnv,
        stdio: ['pipe', 'pipe', 'pipe']
      }
    );
    let outputBuffer = '';
    let stderrBuffer = '';
    let forceKillTimer = null;
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', (chunk) => {
      outputBuffer += chunk;
      const lines = outputBuffer.split(/\r?\n/);
      outputBuffer = lines.pop() || '';
      for (const line of lines) {
        if (!line) continue;
        try {
          const response = JSON.parse(line);
          if (response.type === 'ready') state.setReady();
          else parseToolProtocolLine(line, state.pending, state.setFailure);
        } catch (err) {
          state.setFailure(new Error(`Kabibi tool server response failed: ${err.message}`));
        }
      }
    });
    child.stderr.on('data', (chunk) => {
      stderrBuffer = (stderrBuffer + chunk).slice(-4096);
      console.error('[Kabibi tool server]', chunk.trimEnd());
    });
    child.stdin.on('error', (err) => {
      state.setFailure(new Error(`Kabibi tool server input failed: ${err.message}`));
    });
    child.on('error', (err) => state.setFailure(new Error(`Unable to start Kabibi tool server: ${err.message}`)));
    child.on('close', (code, signal) => {
      if (forceKillTimer) clearTimeout(forceKillTimer);
      const detail = stderrBuffer ? `: ${stderrBuffer}` : '';
      state.setFailure(new Error(`Kabibi tool server exited (${signal || code})${detail}`));
      runtime.dispose().catch((err) => console.error(err));
    });

    return {
      state,
      send: (message) => child.stdin.write(`${JSON.stringify(message)}\n`),
      close: async () => {
        if (child.exitCode === null && child.signalCode === null) {
          const exited = new Promise((resolve) => child.once('close', resolve));
          child.stdin.end();
          forceKillTimer = setTimeout(() => child.kill('SIGKILL'), 3000);
          await exited;
        }
        await runtime.dispose();
      }
    };
  }

  async function createNodeTerminalHost(vscode, context, emitBytes, onExit, cols, rows, guestArgs) {
    const { spawn } = require('node:child_process');
    const runtime = await prepareNodeHostRuntime(vscode, context, 'terminal');
    const child = spawn(
      process.execPath,
      [runtime.hostPath, runtime.wasmPath, ...guestArgs],
      {
        cwd: runtime.cwd,
        env: {
          ...runtime.childEnv,
          MOHABBAT_PSEUDO_TTY: '1',
          MOHABBAT_PSEUDO_TTY_COLS: String(cols),
          MOHABBAT_PSEUDO_TTY_ROWS: String(rows)
        },
        stdio: ['pipe', 'pipe', 'pipe', 'ipc']
      }
    );
    let forceKillTimer = null;
    child.stdout.on('data', emitBytes);
    child.stderr.on('data', emitBytes);
    child.stdin.on('error', (err) => console.error('Kabibi terminal input failed:', err));
    child.on('spawn', () => child.send({ type: 'resize', cols, rows }));
    child.on('close', (code) => {
      if (forceKillTimer) clearTimeout(forceKillTimer);
      runtime.dispose().catch((err) => console.error(err));
      onExit(typeof code === 'number' ? code : 1);
    });

    return new Promise((resolve, reject) => {
      child.once('spawn', () => resolve({
        pushStdin: (data) => child.stdin.write(data),
        triggerSignal: (signum) => child.send({ type: 'signal', signum }),
        setTtySize: (cols, rows) => child.send({ type: 'resize', cols, rows }),
        terminate: () => {
          child.stdin.end();
          forceKillTimer = setTimeout(() => child.kill('SIGKILL'), 1500);
        }
      }));
      child.once('error', (err) => {
        runtime.dispose().catch((disposeErr) => console.error(disposeErr));
        reject(new Error(`Unable to start Kabibi Node host: ${err.message}`));
      });
    });
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
    if (token && token.isCancellationRequested) throw new Error('Kabibi tool invocation cancelled');
    const bridge = await getToolBridge(vscode, context);
    if (bridge.state.isClosed()) {
      throw bridge.state.getFailure() || new Error('Kabibi tool server is closed');
    }
    const requestID = bridge.state.nextID();
    const response = new Promise((resolve, reject) => {
      bridge.state.pending.set(requestID, { resolve, reject });
    });
    const cancellation = token && token.onCancellationRequested(() => {
      try {
        bridge.send({ type: 'cancel', request_id: requestID });
      } catch (err) {
        const request = bridge.state.pending.get(requestID);
        bridge.state.pending.delete(requestID);
        if (request) request.reject(new Error(`Unable to cancel Kabibi tool invocation: ${err.message}`));
      }
    });
    try {
      bridge.send({ type: 'invoke', request_id: requestID, name: operation, arguments: input });
      if (token && token.isCancellationRequested) {
        bridge.send({ type: 'cancel', request_id: requestID });
      }
      const result = await response;
      return new vscode.LanguageModelToolResult([
        new vscode.LanguageModelTextPart(JSON.stringify(result))
      ]);
    } catch (err) {
      bridge.state.pending.delete(requestID);
      throw err;
    } finally {
      if (cancellation) cancellation.dispose();
    }
  }

  /**
   * Installs a global helper (`globalThis.__kabibiOpenInVSCode`) used by `/bin/code`
   * inside the Kabibi guest to open workspace or virtual files in VS Code's editor.
   *
   * @param {any} vscode
   * @param {any} vfs
   */
  function registerGlobalEditorBridge(vscode, vfs) {
    globalThis.__kabibiOpenInVSCode = async (guestPath) => {
      try {
        const resolved = vfs.resolveMount(guestPath);
        if (resolved.kind === 'vscode' && resolved.uri) {
          const doc = await vscode.workspace.openTextDocument(resolved.uri);
          await vscode.window.showTextDocument(doc, { preview: false });
          return 'ok';
        }
        return 'not-in-workspace';
      } catch (err) {
        return String(err && err.message ? err.message : err);
      }
    };
  }

  /**
   * Creates a VS Code `Pseudoterminal` backed by `washmhost` and `VSCodeMountedFS`.
   *
   * @param {any} vscode
   * @param {import('vscode').ExtensionContext} context
   * @param {object} [options]
   * @param {any} [options.vfs]
   * @param {string[]} [options.args]
   * @returns {import('vscode').Pseudoterminal & { getHost: () => any, waitReady: () => Promise<any> }}
   */
  function createKabibiPseudoterminal(vscode, context, options = {}) {
    const writeEmitter = new vscode.EventEmitter();
    const closeEmitter = new vscode.EventEmitter();
    const decoder = new TextDecoder('utf-8');

    let cols = 80;
    let rows = 24;
    let host = null;
    let closed = false;
    /** @type {string[]} */
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
      if (text.length > 0) {
        writeEmitter.fire(text);
      }
    };

    const pseudoterminal = {
      onDidWrite: writeEmitter.event,
      onDidClose: closeEmitter.event,

      open: async (initialDimensions) => {
        if (initialDimensions) {
          if (initialDimensions.columns > 0) cols = initialDimensions.columns;
          if (initialDimensions.rows > 0) rows = initialDimensions.rows;
        }

        try {
          const { washmhostExports, vfsExports } = await ensureRuntimeModules(
            vscode,
            context.extensionUri
          );

          const vfs = options.vfs || new vfsExports.VSCodeMountedFS({
            vscode,
            baseFS: new washmhostExports.AsyncIndexedDBFS('KabibiVSCodeFS'),
            mountPoint: '/workspace'
          });

          registerGlobalEditorBridge(vscode, vfs);

          const wasmBuffer = await loadCachedKabibiWasm(
            vscode,
            context.extensionUri,
            washmhostExports
          );

          const initialCwd = vfs.hasWorkspace() ? '/workspace' : '/home/user';
          const guestArgs = extensionShellArgs(options);
          if (isNodeExtensionHost(vscode)) {
            host = await createNodeTerminalHost(
              vscode,
              context,
              emitBytes,
              (code) => {
                if (!closed) {
                  closed = true;
                  closeEmitter.fire(code);
                }
              },
              cols,
              rows,
              guestArgs
            );
            while (earlyInputQueue.length > 0) {
              host.pushStdin(earlyInputQueue.shift());
            }
            if (resolveReady) resolveReady(host);
            return;
          }
          host = await washmhostExports.washmhost({
            wasm: wasmBuffer,
            fs: vfs,
            cwd: initialCwd,
            args: guestArgs,
            env: {
              TERM: 'xterm-256color',
              COLORTERM: 'truecolor',
              PATH: '/bin',
              HOME: '/home/user',
              PWD: initialCwd
            },
            stdout: emitBytes,
            stderr: emitBytes,
            getTtySize: () => ({ cols, rows }),
            setTtySize: (newCols, newRows) => {
              cols = newCols;
              rows = newRows;
            },
            onExit: (code) => {
              if (!closed) {
                closed = true;
                closeEmitter.fire(typeof code === 'number' ? code : 0);
              }
            }
          });

          while (earlyInputQueue.length > 0) {
            host.pushStdin(earlyInputQueue.shift());
          }

          if (resolveReady) resolveReady(host);
        } catch (err) {
          const msg = err && err.message ? err.message : String(err);
          writeEmitter.fire(`\r\n[Kabibi boot error: ${msg}]\r\n`);
          if (rejectReady) rejectReady(err);
          if (!closed) {
            closed = true;
            closeEmitter.fire(1);
          }
        }
      },

      close: () => {
        if (closed) return;
        closed = true;
        if (host) {
          try {
            host.triggerSignal(15); // SIGTERM
            host.terminate(0);
          } catch (err) {
            console.error('Unable to close Kabibi terminal host:', err);
          }
        }
      },

      handleInput: (data) => {
        if (closed) return;
        if (host) {
          host.pushStdin(data);
        } else {
          earlyInputQueue.push(data);
        }
      },

      setDimensions: (dimensions) => {
        if (!dimensions) return;
        if (dimensions.columns > 0) cols = dimensions.columns;
        if (dimensions.rows > 0) rows = dimensions.rows;
        if (host) {
          if (host.setTtySize) {
            host.setTtySize(cols, rows);
          } else {
            host.triggerSignal(27); // Go wasip1 SIGWINCH
            host.triggerSignal(28); // Unix/Rust SIGWINCH
          }
        }
      },

      getHost: () => host,
      waitReady: () => readyPromise
    };
    activePseudoterminals.add(pseudoterminal);
    closeEmitter.event(() => activePseudoterminals.delete(pseudoterminal));
    return pseudoterminal;
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

  /**
   * Registers the Kabibi extension commands and terminal profile provider.
   *
   * @param {any} moduleExports
   * @param {any} [injectedVscode]
   */
  function registerVscodeExtension(moduleExports, injectedVscode) {
    /**
     * @param {import('vscode').ExtensionContext} context
     */
    function activate(context) {
      const vscode = injectedVscode || require('vscode');
      registerLanguageModelTools(vscode, context);

      context.subscriptions.push(
        vscode.commands.registerCommand(OPEN_TERMINAL_CMD, () => {
          const pty = createKabibiPseudoterminal(vscode, context);
          const terminal = vscode.window.createTerminal({
            name: TERMINAL_SHORT_NAME,
            pty
          });
          terminal.show();
          return { terminal, pty };
        })
      );

      const profileProvider = {
        provideTerminalProfile: (_token) => {
          const pty = createKabibiPseudoterminal(vscode, context);
          return new vscode.TerminalProfile({
            name: TERMINAL_SHORT_NAME,
            pty,
            isTransient: false
          });
        }
      };

      context.subscriptions.push(
        vscode.window.registerTerminalProfileProvider(TERMINAL_PROFILE_ID, profileProvider)
      );
    }

    async function deactivate() {
      for (const pseudoterminal of activePseudoterminals) pseudoterminal.close();
      activePseudoterminals.clear();
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

    moduleExports.activate = activate;
    moduleExports.deactivate = deactivate;
    moduleExports.createKabibiPseudoterminal = createKabibiPseudoterminal;
    moduleExports.createNodeWorkspaceMount = createNodeWorkspaceMount;
    moduleExports.extensionShellArgs = extensionShellArgs;
    moduleExports.ensureRuntimeModules = ensureRuntimeModules;
    moduleExports.loadCachedKabibiWasm = loadCachedKabibiWasm;
    moduleExports.registerLanguageModelTools = registerLanguageModelTools;
  }

  if (typeof module !== 'undefined' && module.exports) {
    registerVscodeExtension(module.exports);
  }
  if (typeof globalThis !== 'undefined') {
    globalThis.KabibiVSCodeExtension = {
      registerVscodeExtension,
      createKabibiPseudoterminal,
      createNodeWorkspaceMount,
      extensionShellArgs,
      registerLanguageModelTools,
      ensureRuntimeModules,
      loadCachedKabibiWasm
    };
  }
})();
