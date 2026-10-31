(function (global) {
  'use strict';

  const CONVENTIONAL_PARENT_ID = 'kabibi-terminal';
  const XTERM_CSS_URL = 'https://cdn.jsdelivr.net/npm/@xterm/xterm@6.0.0/css/xterm.css';
  const XTERM_ESM_URL = 'https://cdn.jsdelivr.net/npm/@xterm/xterm@6.0.0/+esm';
  const FIT_ADDON_ESM_URL = 'https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.11.0/+esm';
  const UNICODE_GRAPHEMES_ADDON_ESM_URL = 'https://cdn.jsdelivr.net/npm/@xterm/addon-unicode-graphemes@0.4.0/+esm';
  const WEBGL_ADDON_ESM_URL = 'https://cdn.jsdelivr.net/npm/@xterm/addon-webgl@0.19.0/+esm';
  const DEFAULT_FONT_FAMILY = 'Consolas, "Cascadia Mono", "Fira Code", Menlo, "Apple Color Emoji", "Segoe UI Emoji", "Noto Color Emoji", monospace';

  function isElement(value) {
    return value !== null && typeof value === 'object' && value.nodeType === 1;
  }

  function isSuperDemoEnabled(globalObj = global, options = {}) {
    if (options && typeof options.mode === 'string') {
      return options.mode === 'super-demo';
    }
    if (options && typeof options.superDemo === 'boolean') {
      return options.superDemo;
    }
    return Boolean(
      globalObj && (
        globalObj.__super_demo__ ||
        globalObj.__kabibiBrowserMode === 'super-demo'
      )
    );
  }

  function isWebGL2Supported(document, options = {}) {
    if (options && typeof options.webgl2Supported === 'boolean') {
      return options.webgl2Supported;
    }
    if (!document || typeof document.createElement !== 'function') {
      return false;
    }
    try {
      const probeCanvas = document.createElement('canvas');
      if (!probeCanvas || typeof probeCanvas.getContext !== 'function') {
        return false;
      }
      const gl = probeCanvas.getContext('webgl2');
      if (!gl) return false;
      if (typeof gl.getExtension === 'function' && typeof gl.getParameter === 'function') {
        const rendererInfo = gl.getExtension('WEBGL_debug_renderer_info');
        if (rendererInfo) {
          const rendererName = String(gl.getParameter(rendererInfo.UNMASKED_RENDERER_WEBGL) || '');
          if (/swiftshader|software rasterizer|llvmpipe/i.test(rendererName)) {
            return false;
          }
        }
      }
      return true;
    } catch (_) {
      return false;
    }
  }

  async function ensureBrowserStyles(document, options = {}) {
    if (!document || typeof document.createElement !== 'function') return;
    const styleTarget = document.head || document.body;
    if (!styleTarget || typeof styleTarget.appendChild !== 'function') return;

    const query = typeof document.querySelector === 'function'
      ? (selector) => document.querySelector(selector)
      : () => null;

    if (!query('style[data-kabibi-base-style="true"]')) {
      const style = document.createElement('style');
      if (typeof style.setAttribute === 'function') {
        style.setAttribute('data-kabibi-base-style', 'true');
      }
      style.textContent = [
        'html, body { width: 100%; height: 100%; margin: 0; overflow: hidden; background: #07090e; }',
        '.xterm { height: 100%; }'
      ].join('\n');
      styleTarget.appendChild(style);
    }

    const cssUrl = options.xtermCssUrl || XTERM_CSS_URL;
    const existingLink = query('link[data-kabibi-xterm-css="true"]') ||
      query(`link[href="${cssUrl}"]`);
    if (existingLink) return;

    const link = document.createElement('link');
    link.rel = 'stylesheet';
    link.href = cssUrl;
    if (typeof link.setAttribute === 'function') {
      link.setAttribute('data-kabibi-xterm-css', 'true');
    }

    const shouldWait = options.waitForStylesheet !== false &&
      Boolean(document.head) &&
      typeof link.addEventListener === 'function';

    if (!shouldWait) {
      styleTarget.appendChild(link);
      return;
    }

    await new Promise((resolve) => {
      let settled = false;
      const done = () => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        resolve();
      };
      const timer = setTimeout(done, 1500);
      link.addEventListener('load', done);
      link.addEventListener('error', done);
      styleTarget.appendChild(link);
    });
  }

  async function loadBrowserDependencies(options = {}) {
    const importer = typeof options.importModule === 'function'
      ? options.importModule
      : (url) => import(url);

    let Terminal = options.Terminal || global.Terminal;
    let FitAddon = options.FitAddon || global.FitAddon;
    let UnicodeGraphemesAddon = options.UnicodeGraphemesAddon || global.UnicodeGraphemesAddon;
    if (typeof Terminal !== 'function' || !FitAddon || !UnicodeGraphemesAddon) {
      const [xtermMod, fitMod, unicodeMod] = await Promise.all([
        typeof Terminal === 'function' ? { Terminal } : importer(options.xtermUrl || XTERM_ESM_URL),
        FitAddon ? { FitAddon } : importer(options.fitAddonUrl || FIT_ADDON_ESM_URL),
        UnicodeGraphemesAddon
          ? { UnicodeGraphemesAddon }
          : Promise.resolve()
              .then(() => importer(options.unicodeGraphemesAddonUrl || UNICODE_GRAPHEMES_ADDON_ESM_URL))
              .catch(() => null)
      ]);
      Terminal = Terminal || (xtermMod && (xtermMod.Terminal || xtermMod.default));
      FitAddon = FitAddon || (fitMod && (fitMod.FitAddon || fitMod.default || fitMod));
      UnicodeGraphemesAddon = UnicodeGraphemesAddon ||
        (unicodeMod && (unicodeMod.UnicodeGraphemesAddon || unicodeMod.default || unicodeMod));
    }

    const result = {
      Terminal,
      FitAddon,
      UnicodeGraphemesAddon: UnicodeGraphemesAddon || null,
      WebglAddon: null
    };

    if (options.superDemo) {
      const existingDeps = options.superDemoDependencies || global.KabibiSuperDemoDependencies || {};
      let WebglAddon = existingDeps.WebglAddon || options.WebglAddon || global.WebglAddon;
      if (!WebglAddon) {
        const webglMod = await importer(options.webglAddonUrl || WEBGL_ADDON_ESM_URL);
        WebglAddon = webglMod && (webglMod.WebglAddon || webglMod.default || webglMod);
      }
      result.WebglAddon = WebglAddon;
    }

    return result;
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

  function observeVisualViewport(host, window) {
    const viewport = window.visualViewport;
    const update = () => {
      const width = Number(viewport && viewport.width) || Number(window.innerWidth) || 0;
      const height = Number(viewport && viewport.height) || Number(window.innerHeight) || 0;
      const left = Number(viewport && viewport.offsetLeft) || 0;
      const top = Number(viewport && viewport.offsetTop) || 0;
      if (width > 0) host.style.width = `${width}px`;
      if (height > 0) host.style.height = `${height}px`;
      host.style.inset = 'auto';
      host.style.left = `${left}px`;
      host.style.top = `${top}px`;
      host.style.right = 'auto';
      host.style.bottom = 'auto';
    };
    const listen = (target, event) => {
      if (target && typeof target.addEventListener === 'function') {
        target.addEventListener(event, update, { passive: true });
      }
    };
    const unlisten = (target, event) => {
      if (target && typeof target.removeEventListener === 'function') {
        target.removeEventListener(event, update);
      }
    };

    update();
    listen(window, 'resize');
    listen(window, 'orientationchange');
    listen(viewport, 'resize');
    listen(viewport, 'scroll');
    return () => {
      unlisten(window, 'resize');
      unlisten(window, 'orientationchange');
      unlisten(viewport, 'resize');
      unlisten(viewport, 'scroll');
    };
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
    const UnicodeGraphemesAddon = options.UnicodeGraphemesAddon || global.UnicodeGraphemesAddon;
    const fitConstructor = FitAddon && (FitAddon.FitAddon || FitAddon);
    const unicodeGraphemesConstructor = UnicodeGraphemesAddon &&
      (UnicodeGraphemesAddon.UnicodeGraphemesAddon || UnicodeGraphemesAddon);
    if (typeof Terminal !== 'function') {
      throw new Error('Kabibi browser mount requires xterm.js Terminal');
    }
    if (typeof fitConstructor !== 'function') {
      throw new Error('Kabibi browser mount requires xterm.js FitAddon');
    }
    return {
      Terminal,
      FitAddon: fitConstructor,
      UnicodeGraphemesAddon: typeof unicodeGraphemesConstructor === 'function'
        ? unicodeGraphemesConstructor
        : null
    };
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

  function clipboardFor(window) {
    return (window && window.navigator && window.navigator.clipboard) ||
      (global.navigator && global.navigator.clipboard) ||
      null;
  }

  function reportClipboardError(action, error) {
    if (global.console && typeof global.console.error === 'function') {
      global.console.error(`Kabibi clipboard ${action} failed:`, error);
    }
  }

  function installTerminalContextMenu(term, container, document, window, superDemo) {
    let menu = null;
    const closeMenu = () => {
      if (menu) menu.remove();
      menu = null;
    };
    const onContextMenu = (event) => {
      event.preventDefault();
      if (typeof event.stopPropagation === 'function') event.stopPropagation();
      closeMenu();

      const clipboard = clipboardFor(window);
      const selection = typeof term.getSelection === 'function' ? term.getSelection() : '';
      const nextMenu = document.createElement('div');
      menu = nextMenu;
      nextMenu.setAttribute('role', 'menu');
      Object.assign(nextMenu.style, {
        position: 'fixed',
        zIndex: '2147483647',
        minWidth: '144px',
        padding: '4px',
        border: '1px solid #526477',
        borderRadius: '5px',
        background: '#101923',
        boxShadow: '0 4px 16px rgba(0, 0, 0, 0.55)',
        color: '#d7e4ef',
        font: '13px system-ui, sans-serif'
      });
      const maxLeft = Math.max(0, (window.innerWidth || 0) - 160);
      const maxTop = Math.max(0, (window.innerHeight || 0) - 116);
      nextMenu.style.left = `${Math.max(0, Math.min(Number(event.clientX) || 0, maxLeft))}px`;
      nextMenu.style.top = `${Math.max(0, Math.min(Number(event.clientY) || 0, maxTop))}px`;

      const addItem = (label, disabled, action) => {
        const button = document.createElement('button');
        button.type = 'button';
        button.textContent = label;
        button.disabled = disabled;
        button.setAttribute('role', 'menuitem');
        Object.assign(button.style, {
          display: 'block',
          width: '100%',
          padding: '7px 10px',
          border: '0',
          borderRadius: '3px',
          background: 'transparent',
          color: 'inherit',
          font: 'inherit',
          textAlign: 'left',
          cursor: disabled ? 'default' : 'pointer',
          opacity: disabled ? '0.5' : '1'
        });
        button.addEventListener('click', (clickEvent) => {
          clickEvent.preventDefault();
          if (disabled) return;
          Promise.resolve()
            .then(action)
            .then(() => {
              closeMenu();
              term.focus();
            })
            .catch((error) => reportClipboardError(label.toLowerCase(), error));
        });
        nextMenu.appendChild(button);
      };

      addItem('Copy', !selection || !clipboard || typeof clipboard.writeText !== 'function', () =>
        clipboard.writeText(selection));
      addItem('Paste', !clipboard || typeof clipboard.readText !== 'function', () =>
        clipboard.readText().then((text) => {
          if (text) term.paste(text);
        }));
      addItem('Select all', false, () => {
        if (typeof term.selectAll === 'function') term.selectAll();
      });
      (document.body || container).appendChild(nextMenu);
    };
    const onOutsidePointerDown = (event) => {
      if (!menu) return;
      if (typeof menu.contains === 'function' && menu.contains(event.target)) return;
      closeMenu();
    };
    const onEscape = (event) => {
      if (event.key === 'Escape') closeMenu();
    };

    container.addEventListener('contextmenu', onContextMenu);
    if (typeof document.addEventListener === 'function') {
      document.addEventListener('pointerdown', onOutsidePointerDown, true);
    }
    if (typeof window.addEventListener === 'function') window.addEventListener('keydown', onEscape, true);

    if (typeof term.attachCustomKeyEventHandler === 'function') {
      term.attachCustomKeyEventHandler((event) => {
        if (superDemo && typeof superDemo.handleKeyEvent === 'function' &&
            !superDemo.handleKeyEvent(event)) {
          return false;
        }
        if (event.type !== 'keydown' || !(event.ctrlKey || event.metaKey) || event.altKey ||
            String(event.key || '').toLowerCase() !== 'v') {
          return true;
        }
        const clipboard = clipboardFor(window);
        if (!clipboard || typeof clipboard.readText !== 'function') return true;

        event.preventDefault();
        if (typeof event.stopPropagation === 'function') event.stopPropagation();
        clipboard.readText()
          .then((text) => {
            if (text) term.paste(text);
          })
          .catch((error) => reportClipboardError('paste', error));
        return false;
      });
    }

    return () => {
      closeMenu();
      container.removeEventListener('contextmenu', onContextMenu);
      if (typeof document.removeEventListener === 'function') {
        document.removeEventListener('pointerdown', onOutsidePointerDown, true);
      }
      if (typeof window.removeEventListener === 'function') window.removeEventListener('keydown', onEscape, true);
    };
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
    const cleanupViewport = createdHost ? observeVisualViewport(parent, window) : () => {};

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
      customGlyphs: true,
      rescaleOverlappingGlyphs: true,
      rightClickSelectsWord: true,
      fontFamily: options.fontFamily || DEFAULT_FONT_FAMILY,
      theme: {
        background: '#07090e',
        ...(options.theme || {})
      },
      ...(options.terminalOptions || {})
    });
    const unicodeGraphemesAddon = dependencies.UnicodeGraphemesAddon
      ? new dependencies.UnicodeGraphemesAddon()
      : null;
    const fitAddon = new dependencies.FitAddon();
    try {
      if (unicodeGraphemesAddon) {
        term.loadAddon(unicodeGraphemesAddon);
        if (term.unicode) {
          term.unicode.activeVersion = '15-graphemes';
        }
      }
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
      cleanupViewport();
      if (createdHost) parent.remove();
      throw error;
    }

    const mode = options.mode === 'super-demo' ? 'super-demo' : 'default';
    let superDemo = null;
    if (mode === 'super-demo') {
      if (!global.KabibiBrowserSuperDemo) {
        root.remove();
        cleanupViewport();
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
        cleanupViewport();
        if (createdHost) parent.remove();
        term.dispose();
        throw error;
      }
    }

    const terminalContextMenuDispose = installTerminalContextMenu(term, terminalContainer, document, window, superDemo);
    let host = options.host || null;
    let startingHost = typeof options.wasm !== 'undefined' && options.wasm !== null;
    const pendingInput = [];
    let disposed = false;
    let disposing = false;
    let disposePromise = null;
    let powerOffPromise = null;
    let hostExited = false;
    const resizeState = { cols: -1, rows: -1 };
    const originalTitle = document.title;
    let appliedTitle = null;
    let startupOutput = '';
    const startupOutputDecoder = typeof global.TextDecoder === 'function'
      ? new global.TextDecoder()
      : null;
    const updateTitleFromStartupOutput = (bytes) => {
      if (mode !== 'super-demo' || appliedTitle !== null) return;
      const text = typeof bytes === 'string'
        ? bytes
        : startupOutputDecoder
          ? startupOutputDecoder.decode(bytes, { stream: true })
          : '';
      if (!text) return;
      startupOutput += text.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '');
      const banner = /K\s*A\s*B\s*I\s*B\s*I\s*:\s*wearable all-weather shell\s+(v[^\s\x1b]+)[\t \r\n]/.exec(startupOutput);
      if (!banner) {
        if (startupOutput.length > 1024) startupOutput = startupOutput.slice(-1024);
        return;
      }
      appliedTitle = `Kabibi ${banner[1]}`;
      document.title = appliedTitle;
      startupOutput = '';
    };
    const forwardStdout = (bytes) => {
      if (!disposed && !disposing) term.write(bytes);
      updateTitleFromStartupOutput(bytes);
      if (options.onStdout) options.onStdout(bytes);
    };
    const startPowerOff = () => {
      hostExited = true;
      if (!superDemo || typeof superDemo.powerOff !== 'function') return Promise.resolve();
      if (!powerOffPromise) powerOffPromise = Promise.resolve(superDemo.powerOff());
      return powerOffPromise;
    };
    const notifyHostExit = () => {
      startPowerOff().catch((error) => {
        console.error('Kabibi CRT power-off animation failed:', error);
      });
    };
    const inputSubscription = term.onData((data) => {
      if (disposing) return;
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

    const disposeImmediately = () => {
      if (disposed) return;
      disposed = true;
      disposing = true;
      if (appliedTitle !== null && document.title === appliedTitle) {
        document.title = originalTitle;
      }
      cleanupViewport();
      window.removeEventListener('resize', fit);
      if (resizeObserver) resizeObserver.disconnect();
      if (inputSubscription) inputSubscription.dispose();
      if (resizeSubscription) resizeSubscription.dispose();
      terminalContextMenuDispose();
      if (superDemo) superDemo.dispose();
      if (host && options.stopHostOnDispose !== false && typeof host.terminate === 'function') {
        host.terminate(0);
      }
      term.dispose();
      root.remove();
      if (createdHost) parent.remove();
    };
    const dispose = () => {
      if (disposed) return Promise.resolve();
      if (disposePromise) return disposePromise;
      disposing = true;
      if (!superDemo || typeof superDemo.powerOff !== 'function' ||
          (typeof superDemo.isFallback === 'function' && superDemo.isFallback())) {
        disposeImmediately();
        disposePromise = Promise.resolve();
        return disposePromise;
      }
      disposePromise = startPowerOff().then(
        () => disposeImmediately(),
        (error) => {
          disposeImmediately();
          throw error;
        }
      );
      return disposePromise;
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

    if (host && !startingHost && typeof host.attachTerminal === 'function') {
      host.attachTerminal({
        stdout: forwardStdout,
        stderr: (bytes) => {
          if (!disposed && !disposing) term.write(bytes);
          if (options.onStderr) options.onStderr(bytes);
        },
        getTtySize: () => ({ cols: term.cols, rows: term.rows }),
        setTtySize: (cols, rows) => term.resize(cols, rows),
        onExit: (code) => {
          if (!disposed && !disposing && options.showExitStatus !== false) {
            term.write(`\r\n[Process exited with code ${code}]\r\n`);
          }
          notifyHostExit();
          if (options.onExit) options.onExit(code);
        }
      });
      reportResize(term, host, options.onResize, resizeState);
    }

    if (startingHost) {
      const hostFactory = options.washmhost ||
        (global.washmhostExports && global.washmhostExports.washmhost);
      if (typeof hostFactory !== 'function') {
        disposeImmediately();
        throw new Error('Kabibi WASM startup requires washmhostExports.washmhost');
      }

      try {
        host = await hostFactory({
          ...(options.hostOptions || {}),
          wasm: options.wasm,
          stdout: forwardStdout,
          stderr: (bytes) => {
            if (!disposed && !disposing) term.write(bytes);
            if (options.onStderr) options.onStderr(bytes);
          },
          getTtySize: () => ({ cols: term.cols, rows: term.rows }),
          setTtySize: (cols, rows) => term.resize(cols, rows),
          onExit: (code) => {
            if (!disposed && !disposing && options.showExitStatus !== false) {
              term.write(`\r\n[Process exited with code ${code}]\r\n`);
            }
            notifyHostExit();
            if (options.onExit) options.onExit(code);
          }
        });
        if (!host || typeof host.pushStdin !== 'function' ||
            typeof host.triggerSignal !== 'function') {
          throw new Error('washmhost returned an incomplete terminal host');
        }
      } catch (error) {
        startingHost = false;
        disposeImmediately();
        throw error;
      }
      startingHost = false;
      for (const data of pendingInput.splice(0)) host.pushStdin(data);
      reportResize(term, host, options.onResize, resizeState);
    }

    if (!hostExited && superDemo && typeof superDemo.powerOn === 'function') {
      superDemo.powerOn();
    }

    const activeMode = superDemo && typeof superDemo.isFallback === 'function' && superDemo.isFallback()
      ? 'default'
      : mode;

    return {
      term,
      fitAddon,
      unicodeGraphemesAddon,
      host,
      parent,
      root,
      terminalContainer,
      createdHost,
      mode: activeMode,
      requestedMode: mode,
      superDemo,
      dispose
    };
  }

  async function bootFromGuest(options = {}) {
    const document = options.document || global.document;
    const window = options.window || global.window;
    if (!document || !window || !document.body) {
      return null;
    }

    const host = options.host ||
      (global.washmhostExports && typeof global.washmhostExports.getActiveHost === 'function'
        ? global.washmhostExports.getActiveHost()
        : null);
    if (!host) {
      return null;
    }
    if (!options.force && typeof host.hasTerminal === 'function' && host.hasTerminal()) {
      return null;
    }

    await ensureBrowserStyles(document, options);

    const superDemoRequested = isSuperDemoEnabled(global, options);
    const webgl2Available = superDemoRequested &&
      Boolean(global.KabibiBrowserSuperDemo) &&
      isWebGL2Supported(document, options);

    let deps;
    let useSuperDemo = webgl2Available;
    if (useSuperDemo) {
      try {
        deps = await loadBrowserDependencies({ ...options, superDemo: true });
        if (!deps.WebglAddon) {
          useSuperDemo = false;
        }
      } catch (error) {
        console.warn('Kabibi super-demo dependency load failed; falling back to flat xterm.js:', error);
        useSuperDemo = false;
        deps = await loadBrowserDependencies({ ...options, superDemo: false });
      }
    } else {
      deps = await loadBrowserDependencies({ ...options, superDemo: false });
    }

    const search = window.location && typeof window.location.search === 'string'
      ? window.location.search
      : '';
    const params = typeof URLSearchParams === 'function' ? new URLSearchParams(search) : null;
    const flat = options.flat === true || Boolean(params && params.get('flat') === '1');

    if (global.__kabibiMountedBrowser && typeof global.__kabibiMountedBrowser.dispose === 'function') {
      await global.__kabibiMountedBrowser.dispose();
    }

    const mounted = await mount({
      ...options,
      document,
      window,
      host,
      Terminal: deps.Terminal,
      FitAddon: deps.FitAddon,
      UnicodeGraphemesAddon: deps.UnicodeGraphemesAddon,
      mode: useSuperDemo ? 'super-demo' : 'default',
      superDemoDependencies: useSuperDemo
        ? {
            ...(options.superDemoDependencies || {}),
            WebglAddon: deps.WebglAddon,
            flat,
            document
          }
        : undefined
    });

    global.__kabibiMountedBrowser = mounted;
    return mounted;
  }

  function downloadLoadedArtifact() {
    const host = global.washmhostExports &&
      typeof global.washmhostExports.getActiveHost === 'function'
      ? global.washmhostExports.getActiveHost()
      : null;
    if (!host || typeof host.getLoadedWasmURL !== 'function') {
      throw new Error('Kabibi browser artifact URL is unavailable');
    }
    const url = host.getLoadedWasmURL();
    if (typeof url !== 'string' || url.length === 0) {
      throw new Error('Kabibi browser artifact was not loaded from a URL');
    }
    if (!global.document || typeof global.document.createElement !== 'function' || !global.document.body) {
      throw new Error('Kabibi browser download requires document.body');
    }
    const anchor = global.document.createElement('a');
    anchor.href = url;
    anchor.download = 'kabibi.bat';
    anchor.style.display = 'none';
    global.document.body.appendChild(anchor);
    try {
      if (typeof anchor.click !== 'function') {
        throw new Error('Kabibi browser download link cannot be clicked');
      }
      anchor.click();
    } finally {
      anchor.remove();
    }
    return url;
  }

  global.KabibiBrowser = Object.freeze({
    mount,
    bootFromGuest,
    resolveParent,
    isSuperDemoEnabled,
    isWebGL2Supported,
    ensureBrowserStyles,
    loadBrowserDependencies,
    downloadLoadedArtifact,
    conventionalParentId: CONVENTIONAL_PARENT_ID
  });
})(globalThis);