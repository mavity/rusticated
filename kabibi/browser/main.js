(function (global) {
  'use strict';

  const CONVENTIONAL_PARENT_ID = 'kabibi-terminal';

  function isElement(value) {
    return value !== null && typeof value === 'object' && value.nodeType === 1;
  }

  function resolveParent(document, options = {}) {
    if (isElement(options.parent)) return options.parent;

    if (typeof options.parentId === 'string' && options.parentId.trim() !== '') {
      const explicitParent = document.getElementById(options.parentId.trim());
      if (explicitParent) return explicitParent;
    }

    return document.getElementById(options.conventionalParentId || CONVENTIONAL_PARENT_ID);
  }

  function createHost(document, parentId = CONVENTIONAL_PARENT_ID) {
    if (!document.body) {
      throw new Error('Kabibi browser mount requires document.body to be available');
    }

    const host = document.createElement('div');
    host.id = parentId;
    host.dataset.kabibiOwned = 'true';
    Object.assign(host.style, {
      position: 'fixed',
      inset: '0',
      width: '100vw',
      height: '100vh',
      overflow: 'hidden',
      zIndex: '0',
      background: '#07090e'
    });
    document.body.appendChild(host);
    return host;
  }

  function createTerminalRoot(document, parent) {
    const root = document.createElement('div');
    root.className = 'kabibi-terminal-root';
    Object.assign(root.style, {
      position: 'relative',
      width: '100%',
      height: '100%',
      minWidth: '0',
      minHeight: '0',
      overflow: 'hidden'
    });
    parent.appendChild(root);
    return root;
  }

  function resolveDependencies(options) {
    const Terminal = options.Terminal || global.Terminal;
    const FitAddon = options.FitAddon || global.FitAddon;
    const fitConstructor = FitAddon && (FitAddon.FitAddon || FitAddon);
    if (typeof Terminal !== 'function') {
      throw new Error('Kabibi browser mount requires xterm.js Terminal');
    }
    if (typeof fitConstructor !== 'function') {
      throw new Error('Kabibi browser mount requires xterm.js FitAddon');
    }
    return { Terminal, FitAddon: fitConstructor };
  }

  function reportResize(term, host, callback, previous) {
    const cols = term.cols;
    const rows = term.rows;
    if (cols === previous.cols && rows === previous.rows) return;

    previous.cols = cols;
    previous.rows = rows;
    if (host) {
      host.triggerSignal(27);
      host.triggerSignal(28);
    }
    if (callback) callback({ cols, rows });
  }

  async function mount(options = {}) {
    const document = options.document || global.document;
    const window = options.window || global.window;
    if (!document || !window) {
      throw new Error('Kabibi browser mount requires a browser document and window');
    }

    const dependencies = resolveDependencies(options);
    let parent = resolveParent(document, options);
    const createdHost = !parent;
    if (!parent) {
      parent = createHost(document, options.createdParentId || CONVENTIONAL_PARENT_ID);
    }

    const root = createTerminalRoot(document, parent);
    const terminalContainer = document.createElement('div');
    terminalContainer.className = 'kabibi-xterm-container';
    Object.assign(terminalContainer.style, {
      width: '100%',
      height: '100%'
    });
    root.appendChild(terminalContainer);

    const term = new dependencies.Terminal({
      cursorBlink: true,
      allowProposedApi: true,
      fontFamily: options.fontFamily || 'Consolas, "Cascadia Mono", "Fira Code", Menlo, monospace',
      theme: {
        background: '#07090e',
        ...(options.theme || {})
      },
      ...(options.terminalOptions || {})
    });
    const fitAddon = new dependencies.FitAddon();
    try {
      term.loadAddon(fitAddon);
      term.open(terminalContainer);
      Object.assign(term.element.style, {
        width: '100%',
        height: '100%'
      });
      const initialBounds = root.getBoundingClientRect();
      if (initialBounds.width > 0 && initialBounds.height > 0) fitAddon.fit();
    } catch (error) {
      term.dispose();
      root.remove();
      if (createdHost) parent.remove();
      throw error;
    }

    const mode = options.mode === 'super-demo' ? 'super-demo' : 'default';
    let superDemo = null;
    if (mode === 'super-demo') {
      if (!global.KabibiBrowserSuperDemo) {
        root.remove();
        if (createdHost) parent.remove();
        term.dispose();
        throw new Error('Super-demo mode was requested but its JavaScript entrypoint is not loaded');
      }
      try {
        superDemo = global.KabibiBrowserSuperDemo.start({
          terminal: term,
          fitAddon,
          root,
          terminalContainer,
          dependencies: options.superDemoDependencies || global.KabibiSuperDemoDependencies || {}
        });
      } catch (error) {
        root.remove();
        if (createdHost) parent.remove();
        term.dispose();
        throw error;
      }
    }

    let host = options.host || null;
    let startingHost = typeof options.wasm !== 'undefined' && options.wasm !== null;
    const pendingInput = [];
    let disposed = false;
    const resizeState = { cols: -1, rows: -1 };

    const inputSubscription = term.onData((data) => {
      if (host) {
        host.pushStdin(data);
      } else if (startingHost) {
        pendingInput.push(data);
      } else if (typeof options.onData === 'function') {
        options.onData(data);
      }
    });
    const resizeSubscription = term.onResize(() => {
      reportResize(term, host, options.onResize, resizeState);
    });

    const dispose = () => {
      if (disposed) return;
      disposed = true;
      window.removeEventListener('resize', fit);
      if (resizeObserver) resizeObserver.disconnect();
      if (inputSubscription) inputSubscription.dispose();
      if (resizeSubscription) resizeSubscription.dispose();
      if (superDemo) superDemo.dispose();
      if (host && options.stopHostOnDispose !== false && typeof host.terminate === 'function') {
        host.terminate(0);
      }
      term.dispose();
      root.remove();
      if (createdHost) parent.remove();
    };

    const fit = () => {
      if (disposed) return;
      const bounds = root.getBoundingClientRect();
      if (bounds.width <= 0 || bounds.height <= 0) return;
      const fixedSuperDemoGrid = superDemo &&
        typeof superDemo.fitTerminal === 'function' &&
        superDemo.fitTerminal();
      if (!fixedSuperDemoGrid) fitAddon.fit();
      reportResize(term, host, options.onResize, resizeState);
      if (superDemo) superDemo.resize();
    };

    const resizeObserver = typeof window.ResizeObserver === 'function'
      ? new window.ResizeObserver(fit)
      : null;
    if (resizeObserver) resizeObserver.observe(parent);
    window.addEventListener('resize', fit);
    fit();
    term.focus();

    if (startingHost) {
      const hostFactory = options.washmhost ||
        (global.washmhostExports && global.washmhostExports.washmhost);
      if (typeof hostFactory !== 'function') {
        dispose();
        throw new Error('Kabibi WASM startup requires washmhostExports.washmhost');
      }

      try {
        host = await hostFactory({
          ...(options.hostOptions || {}),
          wasm: options.wasm,
          stdout: (bytes) => {
            if (!disposed) term.write(bytes);
            if (options.onStdout) options.onStdout(bytes);
          },
          stderr: (bytes) => {
            if (!disposed) term.write(bytes);
            if (options.onStderr) options.onStderr(bytes);
          },
          getTtySize: () => ({ cols: term.cols, rows: term.rows }),
          setTtySize: (cols, rows) => term.resize(cols, rows),
          onExit: (code) => {
            if (!disposed && options.showExitStatus !== false) {
              term.write(`\r\n[Process exited with code ${code}]\r\n`);
            }
            if (options.onExit) options.onExit(code);
          }
        });
        if (!host || typeof host.pushStdin !== 'function' ||
            typeof host.triggerSignal !== 'function') {
          throw new Error('washmhost returned an incomplete terminal host');
        }
      } catch (error) {
        startingHost = false;
        dispose();
        throw error;
      }
      startingHost = false;
      for (const data of pendingInput.splice(0)) host.pushStdin(data);
      reportResize(term, host, options.onResize, resizeState);
    }

    return {
      term,
      fitAddon,
      host,
      parent,
      root,
      terminalContainer,
      createdHost,
      mode,
      dispose
    };
  }

  global.KabibiBrowser = Object.freeze({
    mount,
    resolveParent,
    conventionalParentId: CONVENTIONAL_PARENT_ID
  });
})(globalThis);