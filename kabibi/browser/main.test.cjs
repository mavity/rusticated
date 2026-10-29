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

test('CRT reflection oscillates left-right and back-forward over ten minutes', () => {
  const { context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);
  const offset = context.KabibiBrowserSuperDemo.calculateReflectionOffset;
  const depth = context.KabibiBrowserSuperDemo.calculateReflectionDepth;

  assert.ok(Math.abs(offset(0)) < 1e-12);
  assert.ok(Math.abs(offset(150000) - 1.2) < 1e-12);
  assert.ok(Math.abs(offset(300000)) < 1e-12);
  assert.ok(Math.abs(offset(450000) + 1.2) < 1e-12);
  assert.ok(Math.abs(offset(600000)) < 1e-12);
  assert.ok(Math.abs(depth(0) - 1.2) < 1e-12);
  assert.ok(Math.abs(depth(150000) - 1) < 1e-12);
  assert.ok(Math.abs(depth(300000) - 1.2) < 1e-12);
  assert.ok(Math.abs(depth(450000) - 1.4) < 1e-12);
  assert.ok(Math.abs(depth(600000) - 1.2) < 1e-12);
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
  const renderPass = source.slice(renderStart, source.indexOf('function resize()', renderStart));
  const firstPostRender = renderPass.indexOf('renderer.render(postScene, postCamera)');
  const passOrder = [
    'renderer.setRenderTarget(renderTarget)',
    'renderer.render(scene, camera)',
    'postMesh.material = horizontalBlurMaterial',
    'renderer.setRenderTarget(blurTarget)',
    firstPostRender,
    'postMesh.material = postMaterial',
    'renderer.setRenderTarget(null)',
    renderPass.indexOf('renderer.render(postScene, postCamera)', firstPostRender + 1)
  ].map((step) => typeof step === 'number' ? step : renderPass.indexOf(step));
  assert.ok(passOrder.every((index) => index >= 0));
  assert.ok(passOrder.every((index, position) => position === 0 || index > passOrder[position - 1]));
  assert.match(source, /new THREE\.Vector3\(\)\.setFromMatrixColumn\(camera\.matrixWorld, 1\)\.normalize\(\)/);
  assert.match(source, /const focalHeight = cameraPosition\.clone\(\)\.sub\(cornerPoint\)\.dot\(cameraUp\)/);
  assert.match(source, /const focalPoint = cornerPoint\.clone\(\)\.addScaledVector\(cameraUp, focalHeight\)/);
  assert.match(source, /triangleShape\.set\(pointA, pointB, pointC\)\.closestPointToPoint\(focalPoint, closestPoint\)/);
  assert.match(source, /if \(distance < nearestDistance\) nearestDistance = distance/);
  assert.match(source, /const distance = farthestPoint\.distanceTo\(focalPoint\)/);
  assert.match(source, /if \(distance > farthestDistance\) farthestDistance = distance/);
  assert.match(
    source,
    /\(farthestDistance - distanceToFocalPoint\) \/ distanceRange/
  );
  assert.match(source, /maxBlurPixels: \{ value: 8\.0 \}/);
  assert.match(source, /renderTarget\.texture\.colorSpace = THREE\.SRGBColorSpace/);
  assert.match(source, /blurTarget\.texture\.colorSpace = THREE\.SRGBColorSpace/);
  assert.match(source, /float blurAmount = proximity \* proximity/);
  assert.match(source, /float radius = blurAmount \* maxBlurPixels/);
  assert.match(source, /addGaussianTap\(-6\.0, 0\.011109/);
  assert.match(source, /addGaussianTap\(0\.0, 1\.0/);
  assert.match(source, /addGaussianTap\(6\.0, 0\.011109/);
  assert.doesNotMatch(source, /for \(int tap = -6; tap <= 6; tap\+\+\)/);
  assert.match(source, /uniform vec2 direction/);
  assert.match(source, /textureContext\.filter = 'sepia\(10%\) saturate\(1\.05\)'/);
  assert.equal(
    (source.match(/drawWarmTerminalImage\((?:viewport, targetWidth, targetHeight|viewport, textureCanvas\.width, textureCanvas\.height)\);\s*applyScanlines\(\);/g) || []).length,
    2
  );
  assert.doesNotMatch(source, /cornerDistance|lowerRight/);
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

