'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

class FakeElement {
  constructor(tagName) {
    this.nodeType = 1;
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.parentElement = null;
    this.style = {};
    this.dataset = {};
    this.attributes = {};
    this.className = '';
    this.id = '';
    this.clientWidth = 800;
    this.clientHeight = 600;
    this.listeners = new Map();
  }

  appendChild(child) {
    child.remove();
    this.children.push(child);
    child.parentElement = this;
    return child;
  }

  remove() {
    if (!this.parentElement) return;
    const siblings = this.parentElement.children;
    const index = siblings.indexOf(this);
    if (index >= 0) siblings.splice(index, 1);
    this.parentElement = null;
  }

  click() {
    if (typeof this.onclick === 'function') this.onclick();
    const handler = this.listeners.get('click');
    if (handler) handler({ preventDefault() {} });
  }

  setAttribute(name, value) {
    this.attributes[name] = value;
  }

  getAttribute(name) {
    return this.attributes[name] || null;
  }

  addEventListener(name, callback) {
    this.listeners.set(name, callback);
  }

  removeEventListener(name) {
    this.listeners.delete(name);
  }

  contains(target) {
    if (target === this) return true;
    return this.children.some(child => child.contains(target));
  }

  getContext() {
    return null;
  }

  getBoundingClientRect() {
    return { width: this.clientWidth, height: this.clientHeight, left: 0, top: 0 };
  }
}

class FakeDocument {
  constructor() {
    this.body = new FakeElement('body');
    this.listeners = new Map();
    this.iconLink = null;
    this.title = '';
  }

  querySelector(selector) {
    return selector === 'link[rel="icon"][type="image/svg+xml"]' ? this.iconLink : null;
  }

  createElement(tagName) {
    return new FakeElement(tagName);
  }

  getElementById(id) {
    const find = (element) => {
      if (element.id === id) return element;
      for (const child of element.children) {
        const result = find(child);
        if (result) return result;
      }
      return null;
    };
    return find(this.body);
  }

  addEventListener(name, callback) {
    this.listeners.set(name, callback);
  }

  removeEventListener(name) {
    this.listeners.delete(name);
  }
}

class FakeResizeObserver {
  constructor(callback) {
    this.callback = callback;
  }

  observe() {}
  disconnect() {}
}

class FakeTerminal {
  constructor(options) {
    this.options = options;
    this.cols = 80;
    this.rows = 24;
    this.element = null;
    this.dataListeners = [];
    this.resizeListeners = [];
    this.loadedAddons = [];
    this.disposed = false;
    this.writes = [];
    this.inputs = [];
    this.selection = 'selected text';
    this.customKeyEventHandler = null;
    this.unicode = {
      activeVersion: '6',
      versions: ['6'],
      register(provider) {
        if (provider && provider.version && !this.versions.includes(provider.version)) {
          this.versions.push(provider.version);
        }
      }
    };
  }

  open(element) {
    this.element = element;
  }

  loadAddon(addon) {
    this.loadedAddons.push(addon);
    if (addon && typeof addon.activate === 'function') {
      addon.activate(this);
    }
  }

  onData(callback) {
    this.dataListeners.push(callback);
    return { dispose: () => this.dataListeners.splice(this.dataListeners.indexOf(callback), 1) };
  }

  onResize(callback) {
    this.resizeListeners.push(callback);
    return { dispose: () => this.resizeListeners.splice(this.resizeListeners.indexOf(callback), 1) };
  }

  attachCustomKeyEventHandler(callback) {
    this.customKeyEventHandler = callback;
  }

  getSelection() { return this.selection; }
  hasSelection() { return Boolean(this.selection); }
  selectAll() { this.selection = 'all terminal text'; }
  paste(text) { this.emitData(text); }
  emitData(data) {
    for (const callback of this.dataListeners) callback(data);
  }

  resize(cols, rows) {
    this.cols = cols;
    this.rows = rows;
    for (const callback of this.resizeListeners) callback({ cols, rows });
  }

  focus() {}
  write(data) { this.writes.push(data); }
  input(data) { this.inputs.push(data); }
  dispose() { this.disposed = true; }
}

class FakeFitAddon {
  fit() {}
}

function createHarness() {
  const document = new FakeDocument();
  const window = {
    ResizeObserver: FakeResizeObserver,
    addEventListener() {},
    removeEventListener() {}
  };
  const context = vm.createContext({
    document,
    window,
    console,
    TextDecoder,
    addEventListener() {},
    removeEventListener() {},
    setTimeout,
    clearTimeout
  });
  const source = fs.readFileSync(path.join(__dirname, 'main.js'), 'utf8');
  vm.runInContext(source, context);
  return { document, window, context, api: context.KabibiBrowser };
}

test('parent resolution prefers explicit element, explicit ID, then convention', () => {
  const { document, api } = createHarness();
  const conventional = document.createElement('div');
  conventional.id = api.conventionalParentId;
  document.body.appendChild(conventional);
  const byId = document.createElement('div');
  byId.id = 'by-id';
  document.body.appendChild(byId);
  const explicit = document.createElement('div');

  assert.equal(api.resolveParent(document, { parent: explicit, parentId: 'by-id' }), explicit);
  assert.equal(api.resolveParent(document, { parentId: 'by-id' }), byId);
  assert.equal(api.resolveParent(document, {}), conventional);
});

test('default mode mounts inside the caller parent without taking over its layout', async () => {
  const { document, window, context } = createHarness();
  const parent = document.createElement('main');
  parent.id = 'kabibi-terminal';
  document.body.appendChild(parent);

  const mounted = await context.KabibiBrowser.mount({
    document,
    window,
    Terminal: FakeTerminal,
    FitAddon: FakeFitAddon
  });

  test('owned host follows visual viewport geometry and removes its listeners on dispose', async () => {
    const { document, window, context } = createHarness();
    const listeners = new Map();
    const viewport = {
      width: 360,
      height: 640,
      offsetLeft: 0,
      offsetTop: 18,
      addEventListener(name, callback) { listeners.set(name, callback); },
      removeEventListener(name) { listeners.delete(name); }
    };
    window.visualViewport = viewport;
    window.innerWidth = 400;
    window.innerHeight = 800;

    const mounted = await context.KabibiBrowser.mount({
      document,
      window,
      Terminal: FakeTerminal,
      FitAddon: FakeFitAddon
    });
    assert.equal(mounted.parent.style.width, '360px');
    assert.equal(mounted.parent.style.height, '640px');
    assert.equal(mounted.parent.style.top, '18px');

    viewport.height = 570;
    viewport.offsetTop = 4;
    listeners.get('resize')();
    assert.equal(mounted.parent.style.height, '570px');
    assert.equal(mounted.parent.style.top, '4px');

    mounted.dispose();
    assert.equal(listeners.size, 0);
  });

  test('downloadLoadedArtifact downloads the exact loaded URL as kabibi.bat', () => {
    const { document, context, api } = createHarness();
    const url = './release/dist/kabibi.bat';
    let clicked;
    context.washmhostExports = {
      getActiveHost: () => ({ getLoadedWasmURL: () => url })
    };
    const createElement = document.createElement.bind(document);
    document.createElement = (tagName) => {
      const element = createElement(tagName);
      if (tagName === 'a') {
        element.click = () => { clicked = element; };
      }
      return element;
    };

    assert.equal(api.downloadLoadedArtifact(), url);
    assert.ok(clicked);
    assert.equal(clicked.href, url);
    assert.equal(clicked.download, 'kabibi.bat');
    assert.equal(clicked.parentElement, null);
    assert.equal(document.body.children.length, 0);
  });

  assert.equal(mounted.parent, parent);
  assert.equal(mounted.createdHost, false);
  assert.equal(mounted.mode, 'default');
  assert.equal(mounted.root.parentElement, parent);
  assert.equal(parent.style.position, undefined);
  assert.equal(parent.style.height, undefined);
  assert.equal(mounted.root.style.height, '100%');
  assert.equal(context.KabibiBrowserSuperDemo, undefined);

  mounted.dispose();
  assert.equal(parent.children.length, 0);
});

test('missing parent creates a fixed full-viewport host and removes it on dispose', async () => {
  const { document, window, context } = createHarness();
  const mounted = await context.KabibiBrowser.mount({
    document,
    window,
    Terminal: FakeTerminal,
    FitAddon: FakeFitAddon
  });

  assert.equal(mounted.createdHost, true);
  assert.equal(mounted.parent.style.position, 'fixed');
  assert.equal(mounted.parent.style.inset, 'auto');
  assert.equal(mounted.parent.style.left, '0px');
  assert.equal(mounted.parent.style.top, '0px');
  assert.equal(mounted.parent.style.width, '100vw');
  assert.equal(mounted.parent.style.height, '100vh');
  mounted.dispose();
  assert.equal(document.body.children.length, 0);
});

test('browser terminal supports right-click clipboard menu and Ctrl/Cmd+V', async () => {
  const { document, window, context } = createHarness();
  const pasted = [];
  const copied = [];
  window.navigator = {
    clipboard: {
      async readText() { return 'clipboard contents'; },
      async writeText(text) { copied.push(text); }
    }
  };
  let terminal;
  const mounted = await context.KabibiBrowser.mount({
    document,
    window,
    Terminal: class extends FakeTerminal {
      constructor(options) {
        super(options);
        terminal = this;
      }
    },
    FitAddon: FakeFitAddon,
    onData: (data) => pasted.push(data)
  });

  assert.equal(terminal.options.rightClickSelectsWord, true);
  const keyHandler = terminal.customKeyEventHandler;
  assert.equal(typeof keyHandler, 'function');
  const keyEvent = {
    type: 'keydown',
    key: 'v',
    ctrlKey: true,
    metaKey: false,
    altKey: false,
    preventDefault() { this.defaultPrevented = true; },
    stopPropagation() {}
  };
  assert.equal(keyHandler(keyEvent), false);
  assert.equal(keyEvent.defaultPrevented, true);
  await new Promise(setImmediate);
  assert.deepEqual(pasted, ['clipboard contents']);

  const contextMenu = mounted.terminalContainer.listeners.get('contextmenu');
  let defaultPrevented = false;
  contextMenu({
    clientX: 12,
    clientY: 18,
    preventDefault() { defaultPrevented = true; },
    stopPropagation() {}
  });
  assert.equal(defaultPrevented, true);
  const menu = document.body.children.find(child => child.getAttribute === undefined
    ? child.attributes.role === 'menu'
    : child.getAttribute('role') === 'menu');
  assert.ok(menu, 'expected a terminal context menu');
  const copyButton = menu.children.find(child => child.textContent === 'Copy');
  assert.ok(copyButton);
  copyButton.listeners.get('click')({
    preventDefault() {},
    stopPropagation() {}
  });
  await new Promise(setImmediate);
  assert.deepEqual(copied, ['selected text']);

  const metaPaste = {
    type: 'keydown',
    key: 'v',
    ctrlKey: false,
    metaKey: true,
    altKey: false,
    preventDefault() {},
    stopPropagation() {}
  };
  assert.equal(keyHandler(metaPaste), false);
  await new Promise(setImmediate);
  assert.deepEqual(pasted, ['clipboard contents', 'clipboard contents']);

  mounted.dispose();
  assert.equal(document.body.children.length, 0);
});

test('super-demo is only started when explicitly selected', async () => {
  const { document, window, context } = createHarness();
  const parent = document.createElement('div');
  document.body.appendChild(parent);
  let starts = 0;
  let disposals = 0;
  context.KabibiBrowserSuperDemo = {
    start() {
      starts += 1;
      return {
        resize() {},
        dispose() { disposals += 1; }
      };
    }
  };

  const mounted = await context.KabibiBrowser.mount({
    parent,
    document,
    window,
    Terminal: FakeTerminal,
    FitAddon: FakeFitAddon,
    mode: 'super-demo'
  });

  assert.equal(starts, 1);
  assert.equal(mounted.mode, 'super-demo');
  mounted.dispose();
  assert.equal(disposals, 1);
});

test('super-demo Ctrl toggle modifies physical keys through the terminal handler', async () => {
  const { document, window, context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);
  const superDemo = context.KabibiBrowserSuperDemo;
  document.iconLink = { href: 'data:image/svg+xml,%3Csvg%3E' };
  context.KabibiBrowserSuperDemo = {
    start({ terminal, root }) {
      const keyboard = superDemo.createVirtualKeyboard(document, terminal, root);
      return {
        handleKeyEvent: keyboard.handleKeyEvent,
        isFallback: () => false,
        resize() {},
        dispose() { keyboard.dispose(); }
      };
    }
  };
  const parent = document.createElement('div');
  document.body.appendChild(parent);
  const mounted = await context.KabibiBrowser.mount({
    parent,
    document,
    window,
    Terminal: FakeTerminal,
    FitAddon: FakeFitAddon,
    mode: 'super-demo'
  });
  const controls = mounted.root.children.find((child) => child.className === 'kabibi-keyboard');
  const ctrl = controls.children[0].children[0];
  assert.equal(ctrl.children[0].src, document.iconLink.href);
  ctrl.click();

  let prevented = false;
  const handled = mounted.term.customKeyEventHandler({
    type: 'keydown',
    key: 'c',
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    preventDefault() { prevented = true; },
    stopPropagation() {}
  });
  assert.equal(handled, false);
  assert.equal(prevented, true);
  assert.deepEqual(mounted.term.inputs, ['\x03']);
  mounted.dispose();
});

test('super-demo updates the page title from its startup banner version', async () => {
  const { document, window, context } = createHarness();
  document.title = 'CRT Super-Demo';
  context.KabibiBrowserSuperDemo = {
    start() {
      return { resize() {}, dispose() {} };
    }
  };
  let attachedCallbacks;
  const host = {
    attachTerminal(callbacks) { attachedCallbacks = callbacks; },
    pushStdin() {},
    triggerSignal() {},
    terminate() {}
  };
  const parent = document.createElement('div');
  document.body.appendChild(parent);

  const mounted = await context.KabibiBrowser.mount({
    parent,
    document,
    window,
    Terminal: FakeTerminal,
    FitAddon: FakeFitAddon,
    host,
    mode: 'super-demo'
  });
  attachedCallbacks.stdout(new TextEncoder().encode('🐚 K A B I B I : wearable all-weather shell v0.8.'));
  assert.equal(document.title, 'CRT Super-Demo');
  attachedCallbacks.stdout(new TextEncoder().encode('15\n   - bash\n'));
  assert.equal(document.title, 'Kabibi v0.8.15');

  mounted.dispose();
  assert.equal(document.title, 'CRT Super-Demo');
});

test('super-demo powers on after startup and waits for its power-off animation before disposal', async () => {
  const { document, window, context } = createHarness();
  const lifecycle = [];
  let resolvePowerOff;
  const powerOffAnimation = new Promise((resolve) => {
    resolvePowerOff = resolve;
  });
  let terminal;
  let hostCallbacks;
  context.KabibiBrowserSuperDemo = {
    start() {
      return {
        powerOn() { lifecycle.push('power-on'); },
        powerOff() {
          lifecycle.push('power-off');
          return powerOffAnimation;
        },
        resize() {},
        dispose() { lifecycle.push('renderer-dispose'); }
      };
    }
  };
  const host = {
    attachTerminal(callbacks) {
      lifecycle.push('host-attached');
      hostCallbacks = callbacks;
    },
    pushStdin() {},
    triggerSignal() {},
    terminate() { lifecycle.push('host-terminate'); }
  };
  const parent = document.createElement('div');
  document.body.appendChild(parent);
  const mounted = await context.KabibiBrowser.mount({
    parent,
    document,
    window,
    Terminal: class extends FakeTerminal {
      constructor(options) {
        super(options);
        terminal = this;
      }
    },
    FitAddon: FakeFitAddon,
    host,
    mode: 'super-demo'
  });

  assert.deepEqual(lifecycle, ['host-attached', 'power-on']);
  hostCallbacks.onExit(0);
  assert.equal(lifecycle.filter((entry) => entry === 'power-off').length, 1);
  const disposal = mounted.dispose();
  await Promise.resolve();
  assert.equal(terminal.disposed, false);

  resolvePowerOff();
  await disposal;
  assert.equal(terminal.disposed, true);
  assert.deepEqual(lifecycle.slice(-3), ['power-off', 'renderer-dispose', 'host-terminate']);
});

test('super-demo powers off when a newly-started host exits', async () => {
  const { document, window, context } = createHarness();
  const lifecycle = [];
  let hostCallbacks;
  context.KabibiBrowserSuperDemo = {
    start() {
      return {
        powerOn() { lifecycle.push('power-on'); },
        powerOff() {
          lifecycle.push('power-off');
          return Promise.resolve();
        },
        resize() {},
        dispose() { lifecycle.push('renderer-dispose'); }
      };
    }
  };
  const parent = document.createElement('div');
  document.body.appendChild(parent);
  const mounted = await context.KabibiBrowser.mount({
    parent,
    document,
    window,
    Terminal: FakeTerminal,
    FitAddon: FakeFitAddon,
    mode: 'super-demo',
    wasm: 'kabibi.bat',
    washmhost: (options) => {
      hostCallbacks = options;
      return Promise.resolve({
        pushStdin() {},
        triggerSignal() {},
        terminate() {}
      });
    }
  });

  assert.deepEqual(lifecycle, ['power-on']);
  hostCallbacks.onExit(0);
  assert.deepEqual(lifecycle, ['power-on', 'power-off']);
  await mounted.dispose();
  assert.deepEqual(lifecycle, ['power-on', 'power-off', 'renderer-dispose']);
});

test('super-demo screen keyboard renders all keys and sends xterm sequences', () => {
  const { document, context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);
  const root = document.createElement('div');
  const terminal = new FakeTerminal({});
  terminal.modes = { applicationCursorKeys: false };
  const keyboard = context.KabibiBrowserSuperDemo.createVirtualKeyboard(document, terminal, root);
  const style = root.children.find((child) => child.tagName === 'STYLE');
  const grid = root.children.find((child) => child.className === 'kabibi-keyboard').children[0];
  const buttons = new Map(grid.children.map((button) => [button.textContent, button]));

  assert.equal(grid.children.length, 19);
  assert.match(style.textContent, /1\.55fr repeat\(18, minmax\(0, 1fr\)\)/);
  assert.match(style.textContent, /1\.55fr repeat\(8, minmax\(0, 1fr\)\)/);
  const functionSequences = [
    '\x1bOP', '\x1bOQ', '\x1bOR', '\x1bOS',
    '\x1b[15~', '\x1b[17~', '\x1b[18~', '\x1b[19~',
    '\x1b[20~', '\x1b[21~', '\x1b[23~', '\x1b[24~'
  ];
  const expected = ['\x1b', '\t', ...functionSequences];
  buttons.get('Esc').click();
  buttons.get('Tab').click();
  for (let index = 0; index < functionSequences.length; index += 1) {
    buttons.get(`F${index + 1}`).click();
  }
  for (const [label, sequence] of [
    ['←', '\x1b[D'],
    ['↑', '\x1b[A'],
    ['↓', '\x1b[B'],
    ['→', '\x1b[C']
  ]) {
    buttons.get(label).click();
    expected.push(sequence);
  }
  terminal.modes.applicationCursorKeys = true;
  for (const [label, sequence] of [
    ['←', '\x1bOD'],
    ['↑', '\x1bOA'],
    ['↓', '\x1bOB'],
    ['→', '\x1bOC']
  ]) {
    buttons.get(label).click();
    expected.push(sequence);
  }
  assert.deepEqual(terminal.inputs, expected);

  const controlButton = grid.children[0];
  assert.equal(controlButton.getAttribute('aria-label'), 'Ctrl');
  assert.equal(controlButton.children[0].src, 'kabibi/kabibi.svg');
  assert.equal(controlButton.children[1].textContent, 'Ctrl');
  assert.equal(controlButton.getAttribute('aria-pressed'), 'false');
  controlButton.click();
  assert.equal(controlButton.getAttribute('aria-pressed'), 'true');
  assert.equal(keyboard.isControlActive(), true);

  let prevented = false;
  assert.equal(keyboard.handleKeyEvent({
    type: 'keydown',
    key: 'c',
    preventDefault() { prevented = true; },
    stopPropagation() {}
  }), false);
  assert.equal(prevented, true);
  assert.equal(terminal.inputs[terminal.inputs.length - 1], '\x03');
  buttons.get('↑').click();
  assert.equal(terminal.inputs[terminal.inputs.length - 1], '\x1b[1;5A');

  controlButton.click();
  assert.equal(controlButton.getAttribute('aria-pressed'), 'false');
  assert.equal(keyboard.isControlActive(), false);
  assert.equal(keyboard.handleKeyEvent({
    type: 'keydown',
    key: 'c',
    preventDefault() {},
    stopPropagation() {}
  }), true);

  keyboard.setNarrow(true);
  assert.match(keyboard.element.className, /kabibi-keyboard--narrow/);
  keyboard.setNarrow(false);
  assert.doesNotMatch(keyboard.element.className, /kabibi-keyboard--narrow/);
  keyboard.dispose();
  assert.equal(root.children.length, 0);
});

test('artifact startup routes output and queues terminal input until host readiness', async () => {
  const { document, window, context } = createHarness();
  const parent = document.createElement('div');
  document.body.appendChild(parent);
  let terminal;
  let resolveHost;
  let hostCallbacks;
  const input = [];
  const signals = [];
  const hostPromise = new Promise((resolve) => { resolveHost = resolve; });
  const mountPromise = context.KabibiBrowser.mount({
    parent,
    document,
    window,
    Terminal: class extends FakeTerminal {
      constructor(options) {
        super(options);
        terminal = this;
      }
    },
    FitAddon: FakeFitAddon,
    wasm: 'kabibi.bat',
    washmhost: (options) => {
      hostCallbacks = options;
      return hostPromise;
    }
  });
  terminal.emitData('queued input');

  const host = {
    pushStdin: (data) => input.push(data),
    triggerSignal: (signal) => signals.push(signal),
    terminate() {}
  };
  resolveHost(host);
  const mounted = await mountPromise;

  assert.deepEqual(input, ['queued input']);
  assert.deepEqual(signals, []);
  assert.equal(hostCallbacks.wasm, 'kabibi.bat');
  const initialSize = hostCallbacks.getTtySize();
  assert.equal(initialSize.cols, 80);
  assert.equal(initialSize.rows, 24);
  const stdout = new Uint8Array([65]);
  const stderr = new Uint8Array([66]);
  hostCallbacks.stdout(stdout);
  hostCallbacks.stderr(stderr);
  hostCallbacks.setTtySize(100, 30);
  hostCallbacks.onExit(7);
  assert.deepEqual(terminal.writes, [
    stdout,
    stderr,
    '\r\n[Process exited with code 7]\r\n'
  ]);
  assert.deepEqual(signals, [27, 28]);
  mounted.dispose();
});

test('invalid WASM host results in an explicit error and cleans up the terminal', async () => {
  const { document, window, context } = createHarness();
  const parent = document.createElement('div');
  document.body.appendChild(parent);
  let terminal;

  await assert.rejects(
    context.KabibiBrowser.mount({
      parent,
      document,
      window,
      Terminal: class extends FakeTerminal {
        constructor(options) {
          super(options);
          terminal = this;
        }
      },
      FitAddon: FakeFitAddon,
      wasm: 'kabibi.wasm',
      washmhost: async () => ({})
    }),
    /washmhost returned an incomplete terminal host/
  );

  assert.equal(terminal.disposed, true);
  assert.equal(parent.children.length, 0);
});

test('CRT setup falls back to the same terminal when WebGL2 is unavailable', async () => {
  const { document, window, context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);
  const parent = document.createElement('div');
  document.body.appendChild(parent);
  const expectedGrid = context.KabibiBrowserSuperDemo.calculateGrid(800, 600);
  let addonDisposed = false;
  let terminal;

  class FakeWebglAddon {
    constructor() {
      assert.equal(terminal.cols, expectedGrid.cols);
      assert.equal(terminal.rows, expectedGrid.rows);
      this._renderer = { _canvas: { width: 640, height: 320 } };
    }

    onContextLoss() {
      return { dispose() {} };
    }

    dispose() {
      addonDisposed = true;
    }
  }

  class FakeVector2 {
    set() {}
    copy() {}
  }

  class FakeRaycaster {}

  class FakeThree {
    static Raycaster = FakeRaycaster;
    static Vector2 = FakeVector2;
  }

  const mounted = await context.KabibiBrowser.mount({
    parent,
    document,
    window,
    Terminal: class extends FakeTerminal {
      constructor(options) {
        super(options);
        terminal = this;
      }
    },
    FitAddon: FakeFitAddon,
    mode: 'super-demo',
    superDemoDependencies: {
      THREE: FakeThree,
      WebglAddon: FakeWebglAddon,
      document
    }
  });

  assert.equal(mounted.term, terminal);
  assert.equal(addonDisposed, true);
  assert.equal(mounted.terminalContainer.style.opacity, '');
  assert.equal(terminal.cols, 80);
  assert.equal(terminal.rows, 24);
  assert.equal(mounted.root.children.length, 1);

  mounted.dispose();
  assert.equal(terminal.disposed, true);
});

test('CRT grid adapts orientation while keeping a constant character budget', () => {
  const { context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);

  const superDemo = context.KabibiBrowserSuperDemo;
  const landscape = superDemo.calculateGrid(1600, 1000);
  const widescreen = superDemo.calculateGrid(1600, 800);
  const portrait = superDemo.calculateGrid(600, 1000);
  const tallPortrait = superDemo.calculateGrid(800, 1600);
  const extremeLandscape = superDemo.calculateGrid(10000, 1000);
  const extremePortrait = superDemo.calculateGrid(10, 1000);

  assert.equal(superDemo.MIN_SCREEN_ASPECT, 8 / 16);
  assert.equal(superDemo.MAX_SCREEN_ASPECT, 16 / 8);
  assert.deepEqual({ ...landscape }, { cols: 72, rows: 20 });
  assert.deepEqual({ ...widescreen }, { cols: 80, rows: 18 });
  assert.deepEqual({ ...portrait }, { cols: 44, rows: 33 });
  assert.deepEqual({ ...tallPortrait }, { cols: 40, rows: 36 });
  assert.deepEqual({ ...extremeLandscape }, { cols: 80, rows: 18 });
  assert.deepEqual({ ...extremePortrait }, { cols: 40, rows: 36 });
  assert.ok(portrait.cols < landscape.cols);
  assert.ok(portrait.rows > landscape.rows);
  assert.ok(Math.abs(portrait.cols * portrait.rows - 72 * 20) <= portrait.cols);
});

test('CRT image fitting preserves the source aspect ratio', () => {
  const { context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);

  const portraitFit = context.KabibiBrowserSuperDemo.calculateImageScale(396, 743, 600, 1000);
  const landscapeFit = context.KabibiBrowserSuperDemo.calculateImageScale(936, 374, 1600, 1000);

  assert.ok(Math.abs(portraitFit.x - 0.888) < 0.002);
  assert.equal(portraitFit.y, 1);
  assert.equal(landscapeFit.x, 1);
  assert.ok(Math.abs(landscapeFit.y - 0.639) < 0.002);
});

test('CRT reflection oscillates left-right and back-forward over thirty seconds', () => {
  const { context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);
  const offset = context.KabibiBrowserSuperDemo.calculateReflectionOffset;
  const depth = context.KabibiBrowserSuperDemo.calculateReflectionDepth;

  assert.ok(Math.abs(offset(0)) < 1e-12);
  assert.ok(Math.abs(offset(7500) - 1.2) < 1e-12);
  assert.ok(Math.abs(offset(15000)) < 1e-12);
  assert.ok(Math.abs(offset(22500) + 1.2) < 1e-12);
  assert.ok(Math.abs(offset(30000)) < 1e-12);
  assert.ok(Math.abs(depth(0) - 1.2) < 1e-12);
  assert.ok(Math.abs(depth(7500) - 1) < 1e-12);
  assert.ok(Math.abs(depth(15000) - 1.2) < 1e-12);
  assert.ok(Math.abs(depth(22500) - 1.4) < 1e-12);
  assert.ok(Math.abs(depth(30000) - 1.2) < 1e-12);
});

test('CRT power transitions clamp to an 800 ms startup and 1800 ms shutdown', () => {
  const { context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);
  const superDemo = context.KabibiBrowserSuperDemo;

  assert.equal(superDemo.POWER_ON_DURATION_MS, 800);
  assert.equal(superDemo.POWER_OFF_DURATION_MS, 1800);
  assert.equal(superDemo.calculatePowerProgress(1, 400), 0.5);
  assert.equal(superDemo.calculatePowerProgress(1, 800), 1);
  assert.equal(superDemo.calculatePowerProgress(3, 900), 0.5);
  assert.equal(superDemo.calculatePowerProgress(3, 1800), 1);
  assert.equal(superDemo.calculatePowerProgress(3, 2400), 1);
});

test('CRT capture crops the WebGL viewport rather than its unused canvas area', () => {
  const { context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);

  const viewport = context.KabibiBrowserSuperDemo.calculateCaptureViewport(648, 450, 432, 300);
  const fallback = context.KabibiBrowserSuperDemo.calculateCaptureViewport(432, 300, 648, 450);

  assert.deepEqual({ ...viewport }, { x: 0, y: 150, width: 432, height: 300 });
  assert.deepEqual({ ...fallback }, { x: 0, y: 0, width: 432, height: 300 });
});

test('CRT blur is a final composite pass with endpoints measured on curved geometry', () => {
  const source = fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8');
  const renderStart = source.indexOf('function render(');
  const renderPass = source.slice(renderStart, source.indexOf('function bindBlurUniforms(', renderStart));
  const firstQuadDraw = renderPass.indexOf('gl.drawArrays(gl.TRIANGLES, 0, 6)');
  const passOrder = [
    'gl.bindFramebuffer(gl.FRAMEBUFFER, sceneFbo)',
    'gl.useProgram(crtProgram)',
    'gl.drawElements(gl.TRIANGLES, crtIndexCount, gl.UNSIGNED_SHORT, 0)',
    'gl.bindFramebuffer(gl.FRAMEBUFFER, blurFbo)',
    'gl.useProgram(blurHProgram)',
    firstQuadDraw,
    'gl.bindFramebuffer(gl.FRAMEBUFFER, null)',
    'gl.useProgram(blurVProgram)',
    renderPass.indexOf('gl.drawArrays(gl.TRIANGLES, 0, 6)', firstQuadDraw + 1)
  ].map((step) => typeof step === 'number' ? step : renderPass.indexOf(step));
  assert.ok(passOrder.every((index) => index >= 0));
  assert.ok(passOrder.every((index, position) => position === 0 || index > passOrder[position - 1]));
  assert.match(source, /function calculateFocalBounds\(/);
  assert.match(source, /if \(dist < nearestDistance\) nearestDistance = dist/);
  assert.match(source, /if \(dist > farthestDistance\) farthestDistance = dist/);
  assert.match(
    source,
    /\(farthestDistance - distanceToFocalPoint\) \/ distanceRange/
  );
  assert.match(source, /const maxBlurPixels = 8\.0/);
  assert.match(source, /gl\.SRGB8_ALPHA8/);
  assert.match(source, /gl\.DEPTH_COMPONENT24/);
  assert.match(source, /float blurAmount = opticalEffects \* proximity \* proximity/);
  assert.match(source, /float radius = blurAmount \* maxBlurPixels/);
  assert.match(source, /addGaussianTap\(-6\.0, 0\.011109/);
  assert.match(source, /addGaussianTap\(0\.0, 1\.0/);
  assert.match(source, /addGaussianTap\(6\.0, 0\.011109/);
  assert.doesNotMatch(source, /for \(int tap = -6; tap <= 6; tap\+\+\)/);
  assert.match(source, /uniform vec2 direction/);
  assert.match(source, /float roundedRectInsideDistance\(vec2 position, vec2 halfSize, float cornerRadius\)/);
  assert.match(source, /float gasketWidth = min\(screenSize\.x, screenSize\.y\) \* 0\.024/);
  assert.match(source, /float gasketHalfWidth = gasketWidth \* 0\.5/);
  assert.match(source, /float cornerRadius = gasketWidth \* 0\.90/);
  assert.match(source, /screenHalfSize \+ vec2\(gasketHalfWidth\),\s+cornerRadius \+ gasketHalfWidth/);
  assert.match(source, /screenHalfSize - vec2\(gasketHalfWidth\),\s+cornerRadius - gasketHalfWidth/);
  assert.match(source, /float pixelFootprint = max\([\s\S]*fwidth\(surfacePosition\.x\)[\s\S]*fwidth\(surfacePosition\.y\)/);
  assert.match(source, /float gasketCoverage = outerCoverage \* \(1\.0 - innerCoverage\) \* hitValid/);
  assert.match(
    source,
    /\) \* 0\.60,\s+vec3\(0\.16\)\);\s+composite\.rgb = mix\(composite\.rgb, gasketColor, gasketCoverage\)/
  );
  assert.match(source, /uniform float lightRadius/);
  assert.match(source, /uniform vec2 powerAnimation/);
  assert.match(source, /uniform vec2 powerCenter/);
  assert.match(source, /powerCenter\.x = 0\.5 \+ 0\.5 \* viewProjMatrix\[12\]/);
  assert.match(source, /powerCenter \+ offset \/ vec2\(scaleX, scaleY\)/);
  assert.match(source, /const POWER_ON_DURATION_MS = 800/);
  assert.match(source, /const POWER_OFF_DURATION_MS = 1800/);
  assert.match(source, /float embossHeight = gasketWidth \* 0\.08/);
  assert.match(source, /float embossSlope = 0\.08 \* 3\.14159265/);
  assert.match(source, /vec2 roundedRectInsideGradient\(/);
  assert.match(source, /surfacePosition\.z \+= embossHeight/);
  assert.match(source, /- embossSlope \* edgeGradient\.x/);
  assert.match(source, /float lineHalfWidth = mix\(0\.008, 1\.12, lineProgress\)/);
  assert.match(source, /float bandHalfHeight = mix\(0\.018, 1\.12, bandProgress\)/);
  assert.match(source, /float scaleY = mix\(1\.0, 0\.010, verticalProgress\)/);
  assert.match(source, /float scaleX = mix\(1\.0, 0\.010, horizontalProgress\)/);
  assert.match(source, /textureContext\.filter = 'sepia\(40%\) saturate\(1\.05\)'/);
  assert.equal(
    (source.match(/drawWarmTerminalImage\((?:viewport, targetWidth, targetHeight|viewport, textureCanvas\.width, textureCanvas\.height)\);\s*applyScanlines\(\);/g) || []).length,
    2
  );
  assert.doesNotMatch(source, /THREE\./);
});

test('thick CRT glass optics compute analytical paraboloid hit, Snell refraction, Newtonian dispersion, and focal bounds', () => {
  const { context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);
  const superDemo = context.KabibiBrowserSuperDemo;

  // 1. Analytical ray-paraboloid intersection at screen center and off-center
  const centerHit = superDemo.solveParaboloidRayHit(
    { x: 0, y: 0, z: 2.5 },
    { x: 0, y: 0, z: -1 },
    2.72,
    1.58,
    false
  );
  assert.ok(centerHit);
  assert.ok(Math.abs(centerHit.u - 0.5) < 1e-6);
  assert.ok(Math.abs(centerHit.v - 0.5) < 1e-6);
  assert.ok(Math.abs(centerHit.z) < 1e-6);

  const cornerRayHit = superDemo.solveParaboloidRayHit(
    { x: 1.0, y: -0.5, z: 2.5 },
    { x: 0, y: 0, z: -1 },
    2.72,
    1.58,
    false
  );
  assert.ok(cornerRayHit);
  const expectedNx = 1.0 / (2.72 * 0.5);
  const expectedNy = -0.5 / (1.58 * 0.5);
  const expectedZ = -0.24 * (expectedNx * expectedNx + 0.72 * expectedNy * expectedNy);
  assert.ok(Math.abs(cornerRayHit.z - expectedZ) < 1e-5);

  // Miss outside screen bounds returns null
  const miss = superDemo.solveParaboloidRayHit(
    { x: 5.0, y: 0, z: 2.5 },
    { x: 0, y: 0, z: -1 },
    2.72,
    1.58,
    false
  );
  assert.equal(miss, null);

  // 2. Thick-glass refraction & Newtonian spectral dispersion ordering (|deltaR| < |deltaG| < |deltaB|)
  const eye = { x: 0.31, y: -0.22, z: 2.66 };
  const edgeUv = { x: 0.88, y: 0.16 };
  const refracted = superDemo.refractThickGlassUV(edgeUv, eye, 2.72, 1.58, false);
  const distR = Math.hypot(refracted.uvR.x - edgeUv.x, refracted.uvR.y - edgeUv.y);
  const distG = Math.hypot(refracted.uvG.x - edgeUv.x, refracted.uvG.y - edgeUv.y);
  const distB = Math.hypot(refracted.uvB.x - edgeUv.x, refracted.uvB.y - edgeUv.y);
  assert.ok(distR > 0);
  assert.ok(distR < distG, `expected red refraction (${distR}) < green (${distG})`);
  assert.ok(distG < distB, `expected green refraction (${distG}) < blue (${distB})`);
  assert.ok(refracted.fresnelOuter > 0.0426);
  assert.ok(Number.isFinite(refracted.lensDensity) && refracted.lensDensity > 0.5);

  const flatRefracted = superDemo.refractThickGlassUV(edgeUv, eye, 2.72, 1.58, true);
  assert.equal(flatRefracted.deltaUvG.x, 0);
  assert.equal(flatRefracted.deltaUvG.y, 0);

  // Verify outward curvature reflection direction: on the left side (u = 0.12),
  // bounceUv.x > uvG.x so a left-of-letter fragment samples the letter, placing the reflection on the left.
  const topLeftUv = { x: 0.04, y: 0.88 };
  const topLeftRefracted = superDemo.refractThickGlassUV(topLeftUv, eye, 2.72, 1.58, false);
  assert.ok(
    topLeftRefracted.bounceUv.x > topLeftRefracted.uvG.x,
    `expected top-left corner bounceUv.x (${topLeftRefracted.bounceUv.x}) > uvG.x (${topLeftRefracted.uvG.x}) so reflection appears to the left`
  );
  assert.ok(
    topLeftRefracted.bounceUv.y < topLeftRefracted.uvG.y,
    `expected top-left corner bounceUv.y (${topLeftRefracted.bounceUv.y}) < uvG.y (${topLeftRefracted.uvG.y}) so reflection appears upward`
  );
  const leftUv = { x: 0.12, y: 0.5 };
  const leftRefracted = superDemo.refractThickGlassUV(leftUv, eye, 2.72, 1.58, false);
  assert.ok(
    leftRefracted.bounceUv.x > leftRefracted.uvG.x,
    `expected left-side bounceUv.x (${leftRefracted.bounceUv.x}) > uvG.x (${leftRefracted.uvG.x}) so reflection appears to the left`
  );
  const rightUv = { x: 0.88, y: 0.5 };
  const rightRefracted = superDemo.refractThickGlassUV(rightUv, eye, 2.72, 1.58, false);
  assert.ok(
    rightRefracted.bounceUv.x < rightRefracted.uvG.x,
    `expected right-side bounceUv.x (${rightRefracted.bounceUv.x}) < uvG.x (${rightRefracted.uvG.x}) so reflection appears to the right`
  );
  const leftShiftMag = Math.abs(leftRefracted.bounceUv.x - leftRefracted.uvG.x);
  const rightShiftMag = Math.abs(rightRefracted.bounceUv.x - rightRefracted.uvG.x);
  assert.ok(
    leftShiftMag > rightShiftMag * 1.25,
    `expected left-edge shift (${leftShiftMag}) > right-edge shift (${rightShiftMag}) due to ~0.6 apex`
  );

  // 3. Dual 3D pendulum light positions (left-close to right-far) and curved focal bounds
  const lightLeft = superDemo.calculateLightPosition(22500, 2.72);
  const lightCenter = superDemo.calculateLightPosition(0, 2.72);
  const lightRight = superDemo.calculateLightPosition(7500, 2.72);
  assert.ok(lightRight.x > lightCenter.x && lightCenter.x > lightLeft.x);
  assert.ok(
    lightRight.z > lightLeft.z * 2.0,
    `expected right-side light depth (${lightRight.z}) to be visibly farther than left-side (${lightLeft.z})`
  );
  assert.ok(Math.abs(lightLeft.z - 2.2) < 0.2);
  assert.ok(lightCenter.ellipsoid && lightCenter.ellipsoid.ry > lightCenter.ellipsoid.rx * 2.0);

  const identityView = new Float32Array([
    1, 0, 0, 0,
    0, 1, 0, 0,
    0, 0, 1, 0,
    0, 0, -2.66, 1
  ]);
  const focal = superDemo.calculateFocalBounds(
    eye,
    { x: 0, y: 1, z: 0 },
    identityView,
    2.72,
    1.58,
    false
  );
  assert.ok(focal.nearestDistance > 0);
  assert.ok(focal.farthestDistance > focal.nearestDistance);
});

test('bootFromGuest honors __super_demo__ and falls back to flat full-page xterm when WebGL2 is unsupported', async () => {
  const { document, window, context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);
  context.__super_demo__ = true;

  let attachedCallbacks = null;
  const signals = [];
  const activeHost = {
    hasTerminal() { return Boolean(attachedCallbacks); },
    attachTerminal(callbacks) { attachedCallbacks = callbacks; },
    pushStdin() {},
    triggerSignal(sig) { signals.push(sig); },
    terminate() {}
  };
  context.washmhostExports = {
    getActiveHost: () => activeHost
  };

  let superDemoStarts = 0;
  context.KabibiBrowserSuperDemo = {
    start() {
      superDemoStarts += 1;
      return { resize() {}, dispose() {} };
    }
  };

  const importedUrls = [];
  const mounted = await context.KabibiBrowser.bootFromGuest({
    document,
    window,
    webgl2Supported: false,
    importModule: async (url) => {
      importedUrls.push(url);
      return {
        Terminal: FakeTerminal,
        FitAddon: FakeFitAddon
      };
    }
  });

  assert.ok(mounted);
  assert.equal(mounted.mode, 'default');
  assert.equal(mounted.createdHost, true);
  assert.equal(mounted.parent.style.position, 'fixed');
  assert.equal(superDemoStarts, 0);
  assert.equal(importedUrls.some((u) => u.includes('three')), false);
  assert.ok(attachedCallbacks);
  assert.deepEqual({ ...attachedCallbacks.getTtySize() }, { cols: 80, rows: 24 });

  // Second call is a no-op because activeHost now has a terminal attached
  const second = await context.KabibiBrowser.bootFromGuest({ document, window });
  assert.equal(second, null);

  mounted.dispose();
});

test('bootFromGuest starts CRT super-demo when __super_demo__ and WebGL2 are available', async () => {
  const { document, window, context } = createHarness();
  context.__super_demo__ = true;

  let attachedCallbacks = null;
  const activeHost = {
    hasTerminal() { return Boolean(attachedCallbacks); },
    attachTerminal(callbacks) { attachedCallbacks = callbacks; },
    pushStdin() {},
    triggerSignal() {},
    terminate() {}
  };
  context.washmhostExports = {
    getActiveHost: () => activeHost
  };

  let superDemoStarts = 0;
  context.KabibiBrowserSuperDemo = {
    start() {
      superDemoStarts += 1;
      return {
        fitTerminal() { return true; },
        resize() {},
        isFallback() { return false; },
        dispose() {}
      };
    }
  };

  const mounted = await context.KabibiBrowser.bootFromGuest({
    document,
    window,
    webgl2Supported: true,
    importModule: async () => ({
      Terminal: FakeTerminal,
      FitAddon: FakeFitAddon,
      WebglAddon: class {},
      THREE: {}
    })
  });

  assert.ok(mounted);
  assert.equal(mounted.mode, 'super-demo');
  assert.equal(superDemoStarts, 1);
  assert.ok(attachedCallbacks);
  mounted.dispose();
});

test('bootFromGuest loads @xterm/addon-unicode-graphemes@0.4.0 and activates 15-graphemes with emoji font fallbacks', async () => {
  const { document, window, context } = createHarness();
  context.__super_demo__ = false;

  let attachedCallbacks = null;
  const activeHost = {
    hasTerminal() { return Boolean(attachedCallbacks); },
    attachTerminal(callbacks) { attachedCallbacks = callbacks; },
    pushStdin() {},
    triggerSignal() {},
    terminate() {}
  };
  context.washmhostExports = {
    getActiveHost: () => activeHost
  };

  class FakeUnicodeGraphemesAddon {
    activate(term) {
      term.unicode.register({ version: '15' });
      term.unicode.register({ version: '15-graphemes' });
      term.unicode.activeVersion = '6';
    }
  }

  const importedUrls = [];
  const mounted = await context.KabibiBrowser.bootFromGuest({
    document,
    window,
    webgl2Supported: false,
    importModule: async (url) => {
      importedUrls.push(url);
      if (url.includes('addon-unicode-graphemes')) {
        return { UnicodeGraphemesAddon: FakeUnicodeGraphemesAddon };
      }
      return {
        Terminal: FakeTerminal,
        FitAddon: FakeFitAddon
      };
    }
  });

  assert.ok(mounted);
  assert.ok(
    importedUrls.includes('https://cdn.jsdelivr.net/npm/@xterm/addon-unicode-graphemes@0.4.0/+esm'),
    'expected @xterm/addon-unicode-graphemes@0.4.0/+esm to be imported'
  );
  assert.equal(mounted.term.options.allowProposedApi, true);
  assert.equal(mounted.term.options.customGlyphs, true);
  assert.equal(mounted.term.options.rescaleOverlappingGlyphs, true);
  assert.match(mounted.term.options.fontFamily, /Apple Color Emoji/);
  assert.match(mounted.term.options.fontFamily, /Segoe UI Emoji/);
  assert.match(mounted.term.options.fontFamily, /Noto Color Emoji/);
  assert.ok(mounted.unicodeGraphemesAddon instanceof FakeUnicodeGraphemesAddon);
  assert.equal(mounted.term.unicode.activeVersion, '15-graphemes');

  mounted.dispose();
});
