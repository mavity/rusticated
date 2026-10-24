// @ts-check
/// <reference types="node" />
/// <reference lib="webworker" />

/**
 * Kabibi Shell — Universal VS Code Web & Desktop Extension.
 *
 * Runs identically in Desktop VS Code, `vscode.dev`, `github.dev`, and OpenVSCode
 * clones using raw JavaScript (zero TypeScript / zero bundler).
 *
 * - Provides the `Kabibi` Terminal Profile (`kabibi.terminalProfile`) and
 *   `"Kabibi: Open Shell"` command (`kabibi.openTerminal`).
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

  /** @type {Promise<any> | null} */
  let runtimeModulesPromise = null;

  /** @type {Promise<ArrayBuffer> | null} */
  let unpackedWasmPromise = null;

  /**
   * Dynamically loads `washmhost.js` and `vfs.js` from the extension directory
   * via `vscode.workspace.fs.readFile` so it works identically in `vscode.dev`
   * (where relative CommonJS `require('./...')` is unavailable over HTTP) and
   * Desktop VS Code.
   *
   * @param {any} vscode
   * @param {import('vscode').Uri} extensionUri
   */
  async function ensureRuntimeModules(vscode, extensionUri) {
    if (globalThis.washmhostExports && globalThis.KabibiVSCodeVFS) {
      return {
        washmhostExports: globalThis.washmhostExports,
        vfsExports: globalThis.KabibiVSCodeVFS
      };
    }

    if (!runtimeModulesPromise) {
      runtimeModulesPromise = (async () => {
        const decoder = new TextDecoder('utf-8');

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

        if (!globalThis.washmhostExports || !globalThis.KabibiVSCodeVFS) {
          throw new Error('Failed to initialize washmhostExports or KabibiVSCodeVFS');
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

    return {
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
          const guestArgs = Array.isArray(options.args) && options.args.length > 0
            ? options.args
            : ['kabibi'];

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
          } catch (_) {}
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
          host.triggerSignal(27); // Go wasip1 SIGWINCH
          host.triggerSignal(28); // Unix/Rust SIGWINCH
        }
      },

      getHost: () => host,
      waitReady: () => readyPromise
    };
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

      context.subscriptions.push(
        vscode.commands.registerCommand(OPEN_TERMINAL_CMD, () => {
          const pty = createKabibiPseudoterminal(vscode, context);
          const terminal = vscode.window.createTerminal({
            name: 'Kabibi',
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
            name: 'Kabibi',
            pty,
            isTransient: false
          });
        }
      };

      context.subscriptions.push(
        vscode.window.registerTerminalProfileProvider(TERMINAL_PROFILE_ID, profileProvider)
      );
    }

    function deactivate() {}

    moduleExports.activate = activate;
    moduleExports.deactivate = deactivate;
    moduleExports.createKabibiPseudoterminal = createKabibiPseudoterminal;
    moduleExports.ensureRuntimeModules = ensureRuntimeModules;
    moduleExports.loadCachedKabibiWasm = loadCachedKabibiWasm;
  }

  if (typeof module !== 'undefined' && module.exports) {
    registerVscodeExtension(module.exports);
  }
  if (typeof globalThis !== 'undefined') {
    globalThis.KabibiVSCodeExtension = {
      registerVscodeExtension,
      createKabibiPseudoterminal,
      ensureRuntimeModules,
      loadCachedKabibiWasm
    };
  }
})();
