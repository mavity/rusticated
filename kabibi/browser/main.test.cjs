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

  setAttribute(name, value) {
    this.attributes[name] = value;
  }

  addEventListener(name, callback) {
    this.listeners.set(name, callback);
  }

  removeEventListener(name) {
    this.listeners.delete(name);
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
  }

  open(element) {
    this.element = element;
  }

  loadAddon(addon) {
    this.loadedAddons.push(addon);
  }

  onData(callback) {
    this.dataListeners.push(callback);
    return { dispose: () => this.dataListeners.splice(this.dataListeners.indexOf(callback), 1) };
  }

  onResize(callback) {
    this.resizeListeners.push(callback);
    return { dispose: () => this.resizeListeners.splice(this.resizeListeners.indexOf(callback), 1) };
  }

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
  assert.equal(mounted.parent.style.inset, '0');
  assert.equal(mounted.parent.style.width, '100vw');
  assert.equal(mounted.parent.style.height, '100vh');
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

  const inputs = [];
  context.KabibiBrowserSuperDemo.sendDisplayTest({
    pushStdin(input) { inputs.push(input); }
  });
  assert.equal(inputs.length, 1);
  assert.match(inputs[0], /KABIBI_CRT_/);
  assert.match(inputs[0], /48;2;10;38;46m/);
  assert.match(inputs[0], /\\033\[1;36m/);
  assert.match(inputs[0], /38; do printf/);
  assert.match(inputs[0], /%s\|KABIBI_CRT_0123456789/);
  assert.equal(inputs[0].endsWith('\r'), true);
  context.KabibiBrowserSuperDemo.sendDisplayTest({
    pushStdin(input) { inputs.push(input); }
  }, 36, 40);
  assert.match(inputs[1], /78; do printf/);
  assert.ok(inputs[1].length < inputs[0].length * 2);

  mounted.dispose();
  assert.equal(terminal.disposed, true);
});

test('CRT grid adapts orientation while keeping a constant character budget', () => {
  const { context } = createHarness();
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'super-demo.js'), 'utf8'), context);

  const landscape = context.KabibiBrowserSuperDemo.calculateGrid(1600, 1000);
  const portrait = context.KabibiBrowserSuperDemo.calculateGrid(600, 1000);

  assert.deepEqual({ ...landscape }, { cols: 72, rows: 20 });
  assert.ok(portrait.cols < portrait.rows);
  assert.ok(Math.abs(portrait.cols * portrait.rows - 72 * 20) <= portrait.cols);
});
