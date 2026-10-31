(function (global) {
  'use strict';

  function fsError(code, message) {
    return Object.assign(new Error(message), { code });
  }

  function translateError(error) {
    if (!error || error.code) return error;
    const codeByName = {
      InvalidModificationError: 'ENOTEMPTY',
      NotAllowedError: 'EACCES',
      NotFoundError: 'ENOENT',
      SecurityError: 'EACCES',
      TypeMismatchError: 'ENOTDIR'
    };
    const code = codeByName[error.name];
    if (!code) return error;
    return fsError(code, error.message || code);
  }

  function isNamedError(error, ...names) {
    return Boolean(error && names.includes(error.name));
  }

  function normalizeGuestPath(value) {
    const raw = String(value || '').replace(/\\/g, '/');
    if (!raw.startsWith('/')) {
      throw fsError('EINVAL', 'mount path must be absolute');
    }

    const parts = [];
    for (const part of raw.split('/')) {
      if (!part || part === '.') continue;
      if (part === '..') {
        if (parts.length === 0) throw fsError('EINVAL', 'mount path cannot escape the guest root');
        parts.pop();
        continue;
      }
      parts.push(part);
    }
    return `/${parts.join('/')}`;
  }

  function childPath(parent, name) {
    return parent === '/' ? `/${name}` : `${parent}/${name}`;
  }

  function isWithin(path, root) {
    return path === root || path.startsWith(`${root}/`);
  }

  function entryForDirectory(path) {
    return { path, type: 'dir', size: 0, mode: 0o755, mtime: Date.now() };
  }

  function create(baseFS) {
    if (!baseFS) throw new Error('mountable filesystem requires a base filesystem');

    const mounts = [];
    const modeOverrides = new Map();

    function mountForPath(path) {
      let match = null;
      for (const mount of mounts) {
        if (isWithin(path, mount.path) && (!match || mount.path.length > match.path.length)) {
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
        const rest = mount.path.slice(prefix.length);
        const name = rest.split('/')[0];
        const child = childPath(path, name);
        children.set(child, entryForDirectory(child));
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
          throw translateError(error);
        }
      }
      return { directory, name };
    }

    async function getMountedEntry(target) {
      if (!target.relative) {
        return {
          ...entryForDirectory(target.path),
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
        if (!isNamedError(error, 'NotFoundError', 'TypeMismatchError')) {
          throw translateError(error);
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
        if (isNamedError(error, 'NotFoundError')) return null;
        throw translateError(error);
      }
    }

    async function readMountedEntry(target) {
      const entry = await getMountedEntry(target);
      if (!entry || entry.type === 'dir') return entry;
      const content = new Uint8Array(await entry.file.arrayBuffer());
      return { ...entry, content };
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
        current = childPath(current, part);
        const entry = await statBase(current);
        if (entry && entry.type !== 'dir') {
          throw fsError('ENOTDIR', `mount path parent is not a directory: ${current}`);
        }
      }
    }

    async function copyTree(sourcePath, destinationPath) {
      const entry = await filesystem.getEntry(sourcePath);
      if (!entry) throw fsError('ENOENT', `no such file or directory: ${sourcePath}`);
      if (entry.type === 'dir') {
        await filesystem.putEntry({
          path: destinationPath,
          type: 'dir',
          mode: entry.mode || 0o755,
          mtime: entry.mtime
        });
        for (const child of await filesystem.readdir(sourcePath)) {
          const name = child.path.slice(sourcePath === '/' ? 1 : sourcePath.length + 1);
          await copyTree(child.path, childPath(destinationPath, name));
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
          throw fsError('EINVAL', 'a browser directory must be selected before mounting');
        }
        const path = normalizeGuestPath(
          typeof guestPath === 'string' ? guestPath.trim() : guestPath
        );
        if (path === '/' || path === '/dev' || path.startsWith('/dev/')) {
          throw fsError('EINVAL', 'mount path is reserved');
        }
        if (mounts.some((mount) => isWithin(path, mount.path) || isWithin(mount.path, path))) {
          throw fsError('EBUSY', `mount path overlaps an existing mount: ${path}`);
        }

        await ensureParentDirectories(path);
        const existing = await statBase(path);
        if (existing && existing.type !== 'dir') {
          throw fsError('EEXIST', `mount path is not a directory: ${path}`);
        }
        if (existing && (await baseFS.readdir(path)).length > 0) {
          throw fsError('EEXIST', `mount path is not empty: ${path}`);
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
        if (pathHasMount(path) || pathIsMountAncestor(path)) return entryForDirectory(path);
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
        if (pathHasMount(path) || pathIsMountAncestor(path)) return entryForDirectory(path);
        return baseFS.getEntry(path);
      },

      async putEntry(entry) {
        if (!entry || !entry.path) throw new Error('putEntry requires entry.path');
        const path = normalizeGuestPath(entry.path);
        const target = resolveMountPath(path);
        if (!target) {
          if (pathIsMountAncestor(path)) {
            if (entry.type === 'dir') return;
            throw fsError('EISDIR', `cannot replace mount parent directory: ${path}`);
          }
          return baseFS.putEntry({ ...entry, path });
        }
        if (!target.relative) {
          if (entry.type === 'dir') return;
          throw fsError('EBUSY', `cannot replace mount root: ${path}`);
        }
        if (entry.type === 'symlink') {
          throw fsError('ENOTSUP', 'symbolic links are not supported in browser directory mounts');
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
          throw translateError(error);
        }
      },

      async chmodEntry(entry) {
        const path = normalizeGuestPath(entry.path);
        if (!resolveMountPath(path)) {
          if (typeof baseFS.chmodEntry === 'function') return baseFS.chmodEntry(entry);
          return baseFS.putEntry({ ...entry, path });
        }
        modeOverrides.set(path, Number(entry.mode));
      },

      async setEntryTimes(entry) {
        const path = normalizeGuestPath(entry.path);
        if (resolveMountPath(path)) {
          throw fsError('ENOTSUP', 'timestamps cannot be changed in browser directory mounts');
        }
        if (typeof baseFS.setEntryTimes === 'function') return baseFS.setEntryTimes(entry);
        return baseFS.putEntry({ ...entry, path });
      },

      async removeEntry(pathValue) {
        const path = normalizeGuestPath(pathValue);
        const target = resolveMountPath(path);
        if (!target) {
          if (pathIsMountAncestor(path)) throw fsError('EBUSY', `cannot remove mount parent directory: ${path}`);
          return baseFS.removeEntry(path);
        }
        if (!target.relative) throw fsError('EBUSY', `cannot remove mount root: ${path}`);

        let parent;
        try {
          parent = await getParentDirectory(target, false);
          await parent.directory.removeEntry(parent.name);
          modeOverrides.delete(path);
          return true;
        } catch (error) {
          if (error && error.code === 'ENOENT') return false;
          if (isNamedError(error, 'NotFoundError')) return false;
          throw translateError(error);
        }
      },

      async renameEntry(sourceValue, destinationValue) {
        const source = normalizeGuestPath(sourceValue);
        const destination = normalizeGuestPath(destinationValue);
        if (source === destination) return true;
        if (pathHasMount(source) || pathIsMountAncestor(source)) {
          throw fsError('EBUSY', `cannot move a mount root or parent: ${source}`);
        }
        if (pathHasMount(destination) || pathIsMountAncestor(destination)) {
          throw fsError('EBUSY', `cannot replace a mount root or parent: ${destination}`);
        }
        if (isWithin(destination, source)) {
          throw fsError('EINVAL', 'cannot move a directory into itself');
        }
        if (!mountForPath(source) && !mountForPath(destination) &&
            typeof baseFS.renameEntry === 'function') {
          return baseFS.renameEntry(source, destination);
        }
        if (!(await filesystem.getEntry(source))) return false;
        if (await filesystem.getEntry(destination)) {
          throw fsError('EEXIST', `destination already exists: ${destination}`);
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
            throw fsError(entry ? 'ENOTDIR' : 'ENOENT', `not a directory: ${path}`);
          }
          const results = [];
          try {
            for await (const [name, handle] of entry.handle.entries()) {
              const child = childPath(path, name);
              if (handle.kind === 'directory') {
                results.push({
                  ...entryForDirectory(child),
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
            throw translateError(error);
          }
          return results;
        }

        if (pathHasMount(path)) return [];
        const baseEntry = await statBase(path);
        if (!baseEntry && !pathIsMountAncestor(path)) {
          throw fsError('ENOENT', `no such directory: ${path}`);
        }
        if (baseEntry && baseEntry.type !== 'dir') throw fsError('ENOTDIR', `not a directory: ${path}`);

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

        while (pending.length > 0) {
          const component = pending.shift();
          if (!component || component === '.') continue;
          if (component === '..') {
            resolved.pop();
            continue;
          }

          const candidate = `/${resolved.concat(component).join('/')}`;
          const entry = await filesystem.getEntry(candidate);
          const hasMore = pending.some((part) => part !== '' && part !== '.');
          if (!entry) {
            if (hasMore) throw fsError('ENOENT', `no such file or directory: ${candidate}`);
            return { path: candidate, entry: null };
          }

          if (entry.type === 'symlink' && (hasMore || followFinal)) {
            if (++followed > 40) throw fsError('ELOOP', 'too many symbolic links');
            if (typeof entry.target !== 'string' || entry.target.length === 0) {
              throw fsError('ENOENT', 'empty symbolic link target');
            }
            if (entry.target.startsWith('/')) resolved.length = 0;
            pending.unshift(...entry.target.replace(/\\/g, '/').split('/'));
            continue;
          }
          if (hasMore && entry.type !== 'dir') {
            throw fsError('ENOTDIR', `not a directory: ${candidate}`);
          }
          resolved.push(component);
        }

        const path = resolved.length === 0 ? '/' : `/${resolved.join('/')}`;
        return { path, entry: await filesystem.getEntry(path) };
      }
    };

    return filesystem;
  }

  global.WashmhostBrowserMountFS = Object.freeze({ create, normalizeGuestPath });
})(globalThis);
