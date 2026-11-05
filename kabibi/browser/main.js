(function (global) {
  'use strict';

  const CONVENTIONAL_PARENT_ID = 'kabibi-terminal';
  const MOUNT_REQUEST_KEY = '\uE000';
  const XTERM_CSS_URL = 'https://cdn.jsdelivr.net/npm/@xterm/xterm@6.0.0/css/xterm.css';
  const XTERM_ESM_URL = 'https://cdn.jsdelivr.net/npm/@xterm/xterm@6.0.0/+esm';
  const FIT_ADDON_ESM_URL = 'https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.11.0/+esm';
  const UNICODE_GRAPHEMES_ADDON_ESM_URL = 'https://cdn.jsdelivr.net/npm/@xterm/addon-unicode-graphemes@0.4.0/+esm';
  const WEBGL_ADDON_ESM_URL = 'https://cdn.jsdelivr.net/npm/@xterm/addon-webgl@0.19.0/+esm';
  const DEFAULT_FONT_FAMILY = 'Consolas, "Cascadia Mono", "Fira Code", Menlo, "Apple Color Emoji", "Segoe UI Emoji", "Noto Color Emoji", monospace';
  const installedMountFilesystems = new WeakMap();

  function mountFSError(code, message) {
    return Object.assign(new Error(message), { code });
  }

  function translateMountError(error) {
    if (!error || error.code) return error;
    const codeByName = {
      InvalidModificationError: 'ENOTEMPTY',
      NotAllowedError: 'EACCES',
      NotFoundError: 'ENOENT',
      SecurityError: 'EACCES',
      TypeMismatchError: 'ENOTDIR'
    };
    const code = codeByName[error.name];
    return code ? mountFSError(code, error.message || code) : error;
  }

  function normalizeGuestPath(value) {
    const raw = String(value || '').replace(/\\/g, '/');
    if (!raw.startsWith('/')) {
      throw mountFSError('EINVAL', 'mount path must be absolute');
    }

    const parts = [];
    for (const part of raw.split('/')) {
      if (!part || part === '.') continue;
      if (part === '..') {
        if (parts.length === 0) {
          throw mountFSError('EINVAL', 'mount path cannot escape the guest root');
        }
        parts.pop();
        continue;
      }
      parts.push(part);
    }
    return `/${parts.join('/')}`;
  }

  function mountChildPath(parent, name) {
    return parent === '/' ? `/${name}` : `${parent}/${name}`;
  }

  function mountPathIsWithin(path, root) {
    return path === root || path.startsWith(`${root}/`);
  }

  function mountDirectoryEntry(path) {
    return { path, type: 'dir', size: 0, mode: 0o755, mtime: Date.now() };
  }

  function createKabibiMountFilesystem(baseFS) {
    const mounts = [];
    const modeOverrides = new Map();

    function mountForPath(path) {
      let match = null;
      for (const mount of mounts) {
        if (mountPathIsWithin(path, mount.path) &&
            (!match || mount.path.length > match.path.length)) {
          match = mount;
        }
      }
      return match;
    }

    function virtualMountChildren(path) {
      const children = new Map();
      const prefix = path === '/' ? '/' : `${path}/`;
      for (const mount of mounts) {
        if (!mount.path.startsWith(prefix) || mount.path === path) continue;
        const name = mount.path.slice(prefix.length).split('/')[0];
        const child = mountChildPath(path, name);
        children.set(child, mountDirectoryEntry(child));
      }
      return children;
    }

    function pathHasMount(path) {
      return mounts.some((mount) => mount.path === path);
    }

    function pathIsMountAncestor(path) {
      return mounts.some((mount) => mount.path.startsWith(`${path === '/' ? '' : path}/`));
    }

    function resolveMountPath(path) {
      const mount = mountForPath(path);
      if (!mount) return null;
      return {
        mount,
        path,
        relative: path === mount.path ? '' : path.slice(mount.path.length + 1)
      };
    }

    async function getParentDirectory(target, createParents) {
      const parts = target.relative.split('/').filter(Boolean);
      const name = parts.pop();
      let directory = target.mount.handle;
      for (const part of parts) {
        try {
          directory = await directory.getDirectoryHandle(
            part,
            createParents ? { create: true } : undefined
          );
        } catch (error) {
          throw translateMountError(error);
        }
      }
      return { directory, name };
    }

    async function getMountedEntry(target) {
      if (!target.relative) {
        return {
          ...mountDirectoryEntry(target.path),
          mode: modeOverrides.get(target.path) ?? 0o755,
          handle: target.mount.handle
        };
      }

      let parent;
      try {
        parent = await getParentDirectory(target, false);
      } catch (error) {
        if (error && error.code === 'ENOENT') return null;
        throw error;
      }

      try {
        const handle = await parent.directory.getDirectoryHandle(parent.name);
        return {
          path: target.path,
          type: 'dir',
          size: 0,
          mode: modeOverrides.get(target.path) ?? 0o755,
          mtime: Date.now(),
          handle
        };
      } catch (error) {
        if (!error || !['NotFoundError', 'TypeMismatchError'].includes(error.name)) {
          throw translateMountError(error);
        }
      }

      try {
        const handle = await parent.directory.getFileHandle(parent.name);
        const file = await handle.getFile();
        return {
          path: target.path,
          type: 'file',
          size: file.size,
          mode: modeOverrides.get(target.path) ?? 0o644,
          mtime: file.lastModified || Date.now(),
          handle,
          file
        };
      } catch (error) {
        if (error && error.name === 'NotFoundError') return null;
        throw translateMountError(error);
      }
    }

    async function readMountedEntry(target) {
      const entry = await getMountedEntry(target);
      if (!entry || entry.type === 'dir') return entry;
      return { ...entry, content: new Uint8Array(await entry.file.arrayBuffer()) };
    }

    async function statBase(path) {
      if (typeof baseFS.statEntry === 'function') return baseFS.statEntry(path);
      return baseFS.getEntry(path);
    }

    async function ensureParentDirectories(path) {
      const parts = path.split('/').filter(Boolean);
      parts.pop();
      let current = '/';
      for (const part of parts) {
        current = mountChildPath(current, part);
        const entry = await statBase(current);
        if (entry && entry.type !== 'dir') {
          throw mountFSError('ENOTDIR', `mount path parent is not a directory: ${current}`);
        }
      }
    }

    async function copyTree(sourcePath, destinationPath) {
      const entry = await filesystem.getEntry(sourcePath);
      if (!entry) throw mountFSError('ENOENT', `no such file or directory: ${sourcePath}`);
      if (entry.type === 'dir') {
        await filesystem.putEntry({
          path: destinationPath,
          type: 'dir',
          mode: entry.mode || 0o755,
          mtime: entry.mtime
        });
        for (const child of await filesystem.readdir(sourcePath)) {
          const name = child.path.slice(sourcePath === '/' ? 1 : sourcePath.length + 1);
          await copyTree(child.path, mountChildPath(destinationPath, name));
        }
        return;
      }
      await filesystem.putEntry({ ...entry, path: destinationPath });
    }

    async function removeTree(path) {
      const entry = await filesystem.getEntry(path);
      if (!entry) return;
      if (entry.type === 'dir') {
        for (const child of await filesystem.readdir(path)) {
          await removeTree(child.path);
        }
      }
      await filesystem.removeEntry(path);
    }

    const filesystem = {
      mounts,

      init() {
        return baseFS.init();
      },

      seed(extraEntries) {
        return baseFS.seed(extraEntries);
      },

      async mountAt(directoryHandle, guestPath) {
        if (!directoryHandle || directoryHandle.kind !== 'directory') {
          throw mountFSError('EINVAL', 'a browser directory must be selected before mounting');
        }
        const path = normalizeGuestPath(
          typeof guestPath === 'string' ? guestPath.trim() : guestPath
        );
        if (path === '/' || path === '/dev' || path.startsWith('/dev/')) {
          throw mountFSError('EINVAL', 'mount path is reserved');
        }
        if (mounts.some((mount) =>
          mountPathIsWithin(path, mount.path) || mountPathIsWithin(mount.path, path)
        )) {
          throw mountFSError('EBUSY', `mount path overlaps an existing mount: ${path}`);
        }

        await ensureParentDirectories(path);
        const existing = await statBase(path);
        if (existing && existing.type !== 'dir') {
          throw mountFSError('EEXIST', `mount path is not a directory: ${path}`);
        }
        if (existing && (await baseFS.readdir(path)).length > 0) {
          throw mountFSError('EEXIST', `mount path is not empty: ${path}`);
        }

        mounts.push({ path, handle: directoryHandle });
        mounts.sort((left, right) => left.path.localeCompare(right.path));
        return path;
      },

      async statEntry(pathValue) {
        const path = normalizeGuestPath(pathValue);
        const target = resolveMountPath(path);
        if (target) {
          const entry = await getMountedEntry(target);
          if (entry) {
            const { handle, file, ...metadata } = entry;
            return metadata;
          }
          return null;
        }
        if (pathHasMount(path) || pathIsMountAncestor(path)) return mountDirectoryEntry(path);
        return statBase(path);
      },

      async getEntry(pathValue) {
        const path = normalizeGuestPath(pathValue);
        const target = resolveMountPath(path);
        if (target) {
          const entry = await readMountedEntry(target);
          if (entry) {
            const { handle, file, ...result } = entry;
            return result;
          }
          return null;
        }
        if (pathHasMount(path) || pathIsMountAncestor(path)) return mountDirectoryEntry(path);
        return baseFS.getEntry(path);
      },

      async putEntry(entry) {
        if (!entry || !entry.path) throw new Error('putEntry requires entry.path');
        const path = normalizeGuestPath(entry.path);
        const target = resolveMountPath(path);
        if (!target) {
          if (pathIsMountAncestor(path)) {
            if (entry.type === 'dir') return;
            throw mountFSError('EISDIR', `cannot replace mount parent directory: ${path}`);
          }
          return baseFS.putEntry({ ...entry, path });
        }
        if (!target.relative) {
          if (entry.type === 'dir') return;
          throw mountFSError('EBUSY', `cannot replace mount root: ${path}`);
        }
        if (entry.type === 'symlink') {
          throw mountFSError('ENOTSUP', 'symbolic links are not supported in browser directory mounts');
        }

        const parent = await getParentDirectory(target, true);
        try {
          if (entry.type === 'dir') {
            await parent.directory.getDirectoryHandle(parent.name, { create: true });
            if (typeof entry.mode === 'number') modeOverrides.set(path, entry.mode);
            return;
          }
          const handle = await parent.directory.getFileHandle(parent.name, { create: true });
          const writable = await handle.createWritable();
          const content = entry.content instanceof Uint8Array
            ? entry.content
            : new Uint8Array(entry.content || []);
          const size = Number.isInteger(entry.size) && entry.size >= 0
            ? Math.min(entry.size, content.length)
            : content.length;
          await writable.write(content.subarray(0, size));
          await writable.close();
          if (typeof entry.mode === 'number') modeOverrides.set(path, entry.mode);
        } catch (error) {
          throw translateMountError(error);
        }
      },

      async chmodEntry(entry) {
        const path = normalizeGuestPath(entry.path);
        if (!resolveMountPath(path)) {
          if (typeof baseFS.chmodEntry === 'function') return baseFS.chmodEntry(entry);
          if (entry.type === 'file' && entry.content === undefined) {
            const current = await baseFS.getEntry(path);
            return baseFS.putEntry({ ...(current || {}), ...entry, path });
          }
          return baseFS.putEntry({ ...entry, path });
        }
        modeOverrides.set(path, Number(entry.mode));
      },

      async setEntryTimes(entry) {
        const path = normalizeGuestPath(entry.path);
        if (resolveMountPath(path)) {
          throw mountFSError('ENOTSUP', 'timestamps cannot be changed in browser directory mounts');
        }
        if (typeof baseFS.setEntryTimes === 'function') return baseFS.setEntryTimes(entry);
        if (entry.type === 'file' && entry.content === undefined) {
          const current = await baseFS.getEntry(path);
          return baseFS.putEntry({ ...(current || {}), ...entry, path });
        }
        return baseFS.putEntry({ ...entry, path });
      },

      async removeEntry(pathValue) {
        const path = normalizeGuestPath(pathValue);
        const target = resolveMountPath(path);
        if (!target) {
          if (pathIsMountAncestor(path)) {
            throw mountFSError('EBUSY', `cannot remove mount parent directory: ${path}`);
          }
          return baseFS.removeEntry(path);
        }
        if (!target.relative) throw mountFSError('EBUSY', `cannot remove mount root: ${path}`);

        let parent;
        try {
          parent = await getParentDirectory(target, false);
          await parent.directory.removeEntry(parent.name);
          modeOverrides.delete(path);
          return true;
        } catch (error) {
          if ((error && error.code === 'ENOENT') || (error && error.name === 'NotFoundError')) {
            return false;
          }
          throw translateMountError(error);
        }
      },

      async renameEntry(sourceValue, destinationValue) {
        const source = normalizeGuestPath(sourceValue);
        const destination = normalizeGuestPath(destinationValue);
        if (source === destination) return true;
        if (pathHasMount(source) || pathIsMountAncestor(source)) {
          throw mountFSError('EBUSY', `cannot move a mount root or parent: ${source}`);
        }
        if (pathHasMount(destination) || pathIsMountAncestor(destination)) {
          throw mountFSError('EBUSY', `cannot replace a mount root or parent: ${destination}`);
        }
        if (mountPathIsWithin(destination, source)) {
          throw mountFSError('EINVAL', 'cannot move a directory into itself');
        }
        if (!mountForPath(source) && !mountForPath(destination) &&
            typeof baseFS.renameEntry === 'function') {
          return baseFS.renameEntry(source, destination);
        }
        if (!(await filesystem.getEntry(source))) return false;
        if (await filesystem.getEntry(destination)) {
          throw mountFSError('EEXIST', `destination already exists: ${destination}`);
        }

        await copyTree(source, destination);
        try {
          await removeTree(source);
        } catch (error) {
          try {
            await removeTree(destination);
          } catch (cleanupError) {
            throw new Error(
              `move failed: ${error.message}; destination cleanup failed: ${cleanupError.message}`
            );
          }
          throw error;
        }
        return true;
      },

      async readdir(pathValue) {
        const path = normalizeGuestPath(pathValue);
        const target = resolveMountPath(path);
        if (target) {
          const entry = await getMountedEntry(target);
          if (!entry || entry.type !== 'dir') {
            throw mountFSError(entry ? 'ENOTDIR' : 'ENOENT', `not a directory: ${path}`);
          }
          const results = [];
          try {
            for await (const [name, handle] of entry.handle.entries()) {
              const child = mountChildPath(path, name);
              if (handle.kind === 'directory') {
                results.push({
                  ...mountDirectoryEntry(child),
                  mode: modeOverrides.get(child) ?? 0o755
                });
              } else {
                const file = await handle.getFile();
                results.push({
                  path: child,
                  type: 'file',
                  size: file.size,
                  mode: modeOverrides.get(child) ?? 0o644,
                  mtime: file.lastModified || Date.now()
                });
              }
            }
          } catch (error) {
            throw translateMountError(error);
          }
          return results;
        }

        if (pathHasMount(path)) return [];
        const baseEntry = await statBase(path);
        if (!baseEntry && !pathIsMountAncestor(path)) {
          throw mountFSError('ENOENT', `no such directory: ${path}`);
        }
        if (baseEntry && baseEntry.type !== 'dir') {
          throw mountFSError('ENOTDIR', `not a directory: ${path}`);
        }

        const baseEntries = baseEntry ? await baseFS.readdir(path) : [];
        const merged = new Map(baseEntries.map((entry) => [entry.path, entry]));
        for (const [child, entry] of virtualMountChildren(path)) merged.set(child, entry);
        return Array.from(merged.values());
      },

      async resolveEntryPath(pathValue, cwd = '/', followFinal = true) {
        const raw = String(pathValue || '').replace(/\\/g, '/');
        const absolute = raw.startsWith('/') ? raw : `${cwd}/${raw}`;
        const pending = absolute.split('/');
        const resolved = [];
        let followed = 0;
        let finalEntry = null;

        while (pending.length > 0) {
          const component = pending.shift();
          if (!component || component === '.') continue;
          if (component === '..') {
            resolved.pop();
            finalEntry = null;
            continue;
          }

          const candidate = `/${resolved.concat(component).join('/')}`;
          const hasMore = pending.some((part) => part !== '' && part !== '.');
          const entry = await filesystem.statEntry(candidate);
          if (!entry) {
            if (hasMore) throw mountFSError('ENOENT', `no such file or directory: ${candidate}`);
            return { path: candidate, entry: null };
          }

          if (entry.type === 'symlink' && (hasMore || followFinal)) {
            if (++followed > 40) throw mountFSError('ELOOP', 'too many symbolic links');
            if (typeof entry.target !== 'string' || entry.target.length === 0) {
              throw mountFSError('ENOENT', 'empty symbolic link target');
            }
            if (entry.target.startsWith('/')) resolved.length = 0;
            pending.unshift(...entry.target.replace(/\\/g, '/').split('/'));
            finalEntry = null;
            continue;
          }
          if (hasMore && entry.type !== 'dir') {
            throw mountFSError('ENOTDIR', `not a directory: ${candidate}`);
          }
          resolved.push(component);
          finalEntry = entry;
        }

        const path = resolved.length === 0 ? '/' : `/${resolved.join('/')}`;
        return {
          path,
          entry: finalEntry !== null ? finalEntry : await filesystem.statEntry(path)
        };
      }
    };

    return filesystem;
  }

  function installMountFilesystem(host) {
    if (!host || typeof host.getFS !== 'function') return null;
    const guestFS = host.getFS();
    if (!guestFS || typeof guestFS !== 'object') return null;

    const alreadyInstalled = installedMountFilesystems.get(guestFS);
    if (alreadyInstalled) return alreadyInstalled;
    if (typeof guestFS.mountAt === 'function') {
      installedMountFilesystems.set(guestFS, guestFS);
      return guestFS;
    }

    const baseFS = Object.create(null);
    const methodNames = [
      'init', 'seed', 'statEntry', 'getEntry', 'putEntry', 'chmodEntry',
      'setEntryTimes', 'removeEntry', 'renameEntry', 'readdir', 'resolveEntryPath'
    ];
    for (const name of methodNames) {
      if (typeof guestFS[name] === 'function') {
        baseFS[name] = guestFS[name].bind(guestFS);
      }
    }
    for (const name of ['init', 'seed', 'getEntry', 'putEntry', 'removeEntry', 'readdir']) {
      if (typeof baseFS[name] !== 'function') {
        throw new Error(`browser filesystem is missing required method ${name}`);
      }
    }
    if (typeof guestFS !== 'object' || !Object.isExtensible(guestFS)) {
      throw new Error('browser filesystem cannot install Kabibi directory mounts');
    }

    const adapter = createKabibiMountFilesystem(baseFS);
    const names = Object.keys(adapter);
    for (const name of names) {
      const descriptor = Object.getOwnPropertyDescriptor(guestFS, name);
      if (descriptor && descriptor.configurable === false &&
          (!Object.prototype.hasOwnProperty.call(descriptor, 'value') || !descriptor.writable)) {
        throw new Error(`browser filesystem method ${name} cannot be wrapped`);
      }
    }
    for (const name of names) {
      Object.defineProperty(guestFS, name, {
        value: adapter[name],
        configurable: true,
        enumerable: true,
        writable: true
      });
    }
    installedMountFilesystems.set(guestFS, adapter);
    return adapter;
  }

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

  function showMountError(document, error) {
    const message = error && error.message ? error.message : String(error);
    if (global.console && typeof global.console.error === 'function') {
      global.console.error('Kabibi directory mount failed:', error);
    }
    if (!document || !document.body || typeof document.createElement !== 'function') return;

    const notice = document.createElement('div');
    notice.setAttribute('role', 'status');
    notice.setAttribute('aria-live', 'assertive');
    notice.textContent = `Directory mount failed: ${message}`;
    Object.assign(notice.style, {
      position: 'fixed',
      left: '50%',
      bottom: '20px',
      transform: 'translateX(-50%)',
      zIndex: '2147483647',
      maxWidth: 'min(640px, calc(100vw - 32px))',
      padding: '10px 14px',
      border: '1px solid #FFD700',
      borderRadius: '5px',
      background: '#1A1F26',
      color: '#E5E5E5',
      boxShadow: '0 4px 18px rgba(0, 0, 0, 0.6)',
      font: '13px system-ui, sans-serif'
    });
    document.body.appendChild(notice);
    setTimeout(() => notice.remove(), 6000);
  }

  function directoryMountError(error) {
    if (error && error.name === 'AbortError') return null;
    if (error && (error.name === 'NotAllowedError' || error.name === 'SecurityError')) {
      return new Error('directory access was denied; choose a directory and grant read/write access');
    }
    return error;
  }

  function installTerminalContextMenu(term, container, document, window, superDemo, onMountShortcut) {
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
        if (event.type === 'keydown' && (event.ctrlKey || event.metaKey) && !event.altKey &&
            String(event.key || '').toLowerCase() === 'm') {
          event.preventDefault();
          if (typeof event.stopPropagation === 'function') event.stopPropagation();
          if (typeof onMountShortcut === 'function') onMountShortcut();
          return false;
        }
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

    let host = options.host || null;
    let mountedFilesystem = host ? installMountFilesystem(host) : null;
    let startingHost = typeof options.wasm !== 'undefined' && options.wasm !== null;
    const hostOptions = { ...(options.hostOptions || {}) };
    let pendingMountDirectory = null;
    const mountDirectoryName = (directory) => String(directory.name || 'directory')
      .replace(/[\\/]/g, '-')
      .replace(/[\u0000-\u001f]/g, '_') || 'directory';
    const selectMountDirectory = async () => {
      pendingMountDirectory = null;
      if (!window || typeof window.showDirectoryPicker !== 'function') {
        throw new Error('directory mounting requires the browser File System Access API');
      }

      let directory;
      try {
        directory = await window.showDirectoryPicker({ mode: 'readwrite' });
      } catch (error) {
        const mountError = directoryMountError(error);
        if (mountError === null) return '';
        throw mountError;
      }
      if (!directory) return '';

      pendingMountDirectory = directory;
      return mountDirectoryName(directory);
    };

    const selectedMountDirectoryName = () => {
      return pendingMountDirectory ? mountDirectoryName(pendingMountDirectory) : '';
    };

    const mountSelectedDirectory = async (guestPath) => {
      if (!pendingMountDirectory) {
        throw new Error('choose a browser directory before mounting it');
      }
      if (!mountedFilesystem && host) {
        mountedFilesystem = installMountFilesystem(host);
      }
      if (!mountedFilesystem || typeof mountedFilesystem.mountAt !== 'function') {
        throw new Error('browser host does not support mounted directories');
      }

      const directory = pendingMountDirectory;
      pendingMountDirectory = null;
      const mountedPath = await mountedFilesystem.mountAt(directory, guestPath);
      if (host && typeof host.triggerSignal === 'function') {
        host.triggerSignal(27);
        host.triggerSignal(28);
      }
      return mountedPath;
    };

    const cancelMountSelection = () => {
      pendingMountDirectory = null;
    };

    const onMountShortcut = () => {
      if (!host || typeof host.pushStdin !== 'function') {
        showMountError(document, new Error('browser terminal is not ready to mount a directory'));
        return;
      }
      selectMountDirectory()
        .then((directoryName) => {
          if (directoryName) host.pushStdin(MOUNT_REQUEST_KEY);
        })
        .catch((error) => showMountError(document, error));
    };
    const terminalContextMenuDispose = installTerminalContextMenu(
      term,
      terminalContainer,
      document,
      window,
      superDemo,
      onMountShortcut
    );
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
          ...hostOptions,
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
        mountedFilesystem = installMountFilesystem(host);
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
      selectMountDirectory,
      selectedMountDirectoryName,
      mountSelectedDirectory,
      cancelMountSelection,
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

  function mountedBrowserApi(methodName) {
    const mounted = global.__kabibiMountedBrowser;
    if (!mounted || typeof mounted[methodName] !== 'function') {
      throw new Error('Kabibi browser terminal is not ready for directory mounting');
    }
    return mounted[methodName];
  }

  function selectMountDirectory() {
    return mountedBrowserApi('selectMountDirectory').call(global.__kabibiMountedBrowser);
  }

  function selectedMountDirectoryName() {
    return mountedBrowserApi('selectedMountDirectoryName').call(global.__kabibiMountedBrowser);
  }

  function mountSelectedDirectory(path) {
    return mountedBrowserApi('mountSelectedDirectory').call(global.__kabibiMountedBrowser, path);
  }

  function cancelMountSelection() {
    return mountedBrowserApi('cancelMountSelection').call(global.__kabibiMountedBrowser);
  }

  global.KabibiBrowser = Object.freeze({
    mount,
    bootFromGuest,
    selectMountDirectory,
    selectedMountDirectoryName,
    mountSelectedDirectory,
    cancelMountSelection,
    resolveParent,
    isSuperDemoEnabled,
    isWebGL2Supported,
    ensureBrowserStyles,
    loadBrowserDependencies,
    downloadLoadedArtifact,
    conventionalParentId: CONVENTIONAL_PARENT_ID
  });
})(globalThis);