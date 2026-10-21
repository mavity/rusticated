// @ts-check
/// <reference types="node" />

/**
 * VSCodeMountedFS — Hybrid Virtual Filesystem for Kabibi in VS Code & vscode.dev.
 *
 * Mounts `vscode.workspace.workspaceFolders` at `/workspace` (backed by the
 * asynchronous `vscode.workspace.fs` API), while delegating `/`, `/bin`, `/etc`,
 * `/home/user`, `/tmp`, `/var`, and `/usr` to `AsyncIndexedDBFS` (backed by
 * IndexedDB in browsers/workers or an in-memory Map fallback).
 */
(function (root) {
  'use strict';

  const FILE_TYPE_UNKNOWN = 0;
  const FILE_TYPE_FILE = 1;
  const FILE_TYPE_DIRECTORY = 2;
  const FILE_TYPE_SYMLINK = 64;

  function normalizePath(p) {
    const raw = String(p || '/').trim().replace(/\\/g, '/');
    const parts = [];
    for (const seg of raw.split('/')) {
      if (!seg || seg === '.') continue;
      if (seg === '..') {
        if (parts.length > 0) parts.pop();
      } else {
        parts.push(seg);
      }
    }
    return '/' + parts.join('/');
  }

  function mapVSCodeFileType(ft) {
    if ((ft & FILE_TYPE_DIRECTORY) !== 0) return 'dir';
    if ((ft & FILE_TYPE_SYMLINK) !== 0) return 'symlink';
    return 'file';
  }

  function isNotFoundError(err) {
    if (!err) return false;
    const code = String(err.code || err.name || '');
    const msg = String(err.message || '');
    return (
      code === 'FileNotFound' ||
      code === 'EntryNotFound' ||
      code === 'ENOENT' ||
      code === 'NotFoundError' ||
      msg.includes('EntryNotFound') ||
      msg.includes('FileNotFound') ||
      msg.includes('ENOENT')
    );
  }

  function translateError(err) {
    if (!err) return err;
    if (isNotFoundError(err)) {
      const e = new Error(err.message || 'ENOENT');
      e.code = 'ENOENT';
      return e;
    }
    const code = String(err.code || err.name || '');
    if (code === 'FileExists' || code === 'EntryExists') {
      const e = new Error(err.message || 'EEXIST');
      e.code = 'EEXIST';
      return e;
    }
    if (code === 'FileNotADirectory' || code === 'EntryNotADirectory') {
      const e = new Error(err.message || 'ENOTDIR');
      e.code = 'ENOTDIR';
      return e;
    }
    if (code === 'FileIsADirectory' || code === 'EntryIsADirectory') {
      const e = new Error(err.message || 'EISDIR');
      e.code = 'EISDIR';
      return e;
    }
    if (code === 'NoPermissions') {
      const e = new Error(err.message || 'EACCES');
      e.code = 'EACCES';
      return e;
    }
    return err;
  }

  class VSCodeMountedFS {
    /**
     * @param {object} options
     * @param {any} options.vscode - The `vscode` API namespace (or mock in tests)
     * @param {any} options.baseFS - Instance of `AsyncIndexedDBFS`
     * @param {string} [options.mountPoint='/workspace'] - Guest mount path
     */
    constructor(options) {
      if (!options || !options.vscode) {
        throw new Error('VSCodeMountedFS requires options.vscode');
      }
      if (!options.baseFS) {
        throw new Error('VSCodeMountedFS requires options.baseFS');
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

    hasWorkspace() {
      return this.getWorkspaceFolders().length > 0;
    }

    /**
     * Resolves a guest path against `/workspace` and returns:
     * - `{ kind: 'base', path }` for paths outside `/workspace` (or when no workspace is open)
     * - `{ kind: 'multi-root', path, folders }` when `/workspace` itself is queried in a multi-root workspace
     * - `{ kind: 'vscode', path, uri, folder }` when mapped to a concrete `vscode.Uri`
     * - `{ kind: 'not-found', path }` when a multi-root child folder name does not match any workspace folder
     *
     * @param {string} guestPath
     */
    resolveMount(guestPath) {
      const norm = normalizePath(guestPath);
      const folders = this.getWorkspaceFolders();

      if (folders.length === 0) {
        return { kind: 'base', path: norm };
      }

      if (norm !== this.mountPoint && !norm.startsWith(this.mountPoint + '/')) {
        return { kind: 'base', path: norm };
      }

      const rel = norm === this.mountPoint ? '' : norm.slice(this.mountPoint.length + 1);
      const segs = rel ? rel.split('/').filter(Boolean) : [];

      if (folders.length === 1) {
        const folder = folders[0];
        const uri = segs.length === 0
          ? folder.uri
          : this.vscode.Uri.joinPath(folder.uri, ...segs);
        return { kind: 'vscode', path: norm, uri, folder, isMountRoot: segs.length === 0 };
      }

      // Multi-root workspace: /workspace/<folder.name>/...
      if (segs.length === 0) {
        return { kind: 'multi-root', path: norm, folders };
      }

      const folderName = segs[0];
      const folder = folders.find((f, idx) => (f.name || `folder-${idx}`) === folderName);
      if (!folder) {
        return { kind: 'not-found', path: norm };
      }

      const subSegs = segs.slice(1);
      const uri = subSegs.length === 0
        ? folder.uri
        : this.vscode.Uri.joinPath(folder.uri, ...subSegs);
      return { kind: 'vscode', path: norm, uri, folder, isMountRoot: subSegs.length === 0 };
    }

    async init() {
      await this.baseFS.init();
    }

    async seed(extraEntries = []) {
      const now = Date.now();
      const encoder = new TextEncoder();
      const codeHelperScript =
        '#!/bin/sh\n' +
        '# Open one or more files in the host VS Code editor\n' +
        'for f in "$@"; do\n' +
        '  case "$f" in\n' +
        '    /*) p="$f" ;;\n' +
        '    *)  p="$PWD/$f" ;;\n' +
        '  esac\n' +
        '  printf "globalThis.__kabibiOpenInVSCode && globalThis.__kabibiOpenInVSCode(%s)" "\\"$p\\"" > /dev/eval\n' +
        'done\n';

      const defaults = [
        { path: this.mountPoint, type: 'dir', mode: 0o755, mtime: now },
        {
          path: '/bin/code',
          type: 'file',
          content: encoder.encode(codeHelperScript),
          size: encoder.encode(codeHelperScript).length,
          mode: 0o755,
          mtime: now
        },
        ...extraEntries
      ];

      await this.baseFS.seed(defaults);
      // Ensure mountPoint directory exists in baseFS even if baseFS was previously seeded
      const existingMount = await this.baseFS.getEntry(this.mountPoint);
      if (!existingMount) {
        await this.baseFS.putEntry({ path: this.mountPoint, type: 'dir', mode: 0o755, mtime: now });
      }
    }

    /**
     * Metadata-only stat lookup (used by `washmhost.js` `path_stat` so `ls -l`
     * and the Ctrl+O Dual-Pane File Manager do not read full file contents).
     *
     * @param {string} guestPath
     */
    async statEntry(guestPath) {
      const target = this.resolveMount(guestPath);
      if (target.kind === 'base') {
        const entry = await this.baseFS.getEntry(target.path);
        return entry || null;
      }
      if (target.kind === 'not-found') {
        return null;
      }
      if (target.kind === 'multi-root') {
        return {
          path: target.path,
          type: 'dir',
          size: 0,
          mode: 0o755,
          mtime: Date.now()
        };
      }

      try {
        const st = await this.vscode.workspace.fs.stat(target.uri);
        const type = mapVSCodeFileType(st.type);
        const defaultMode = type === 'dir' ? 0o755 : 0o644;
        const mode = this.modeOverrides.get(target.path) ?? defaultMode;
        return {
          path: target.path,
          type,
          size: Number(st.size || 0),
          mode,
          mtime: Number(st.mtime || Date.now())
        };
      } catch (err) {
        if (isNotFoundError(err)) return null;
        throw translateError(err);
      }
    }

    /**
     * Full entry lookup (used by `path_open`). Reads file content bytes for regular files.
     *
     * @param {string} guestPath
     */
    async getEntry(guestPath) {
      const target = this.resolveMount(guestPath);
      if (target.kind === 'base') {
        return this.baseFS.getEntry(target.path);
      }
      if (target.kind === 'not-found') {
        return null;
      }
      if (target.kind === 'multi-root') {
        return {
          path: target.path,
          type: 'dir',
          size: 0,
          mode: 0o755,
          mtime: Date.now()
        };
      }

      try {
        const st = await this.vscode.workspace.fs.stat(target.uri);
        const type = mapVSCodeFileType(st.type);
        const defaultMode = type === 'dir' ? 0o755 : 0o644;
        const mode = this.modeOverrides.get(target.path) ?? defaultMode;
        if (type === 'dir') {
          return {
            path: target.path,
            type: 'dir',
            size: 0,
            mode,
            mtime: Number(st.mtime || Date.now())
          };
        }

        const rawBytes = await this.vscode.workspace.fs.readFile(target.uri);
        const content = rawBytes instanceof Uint8Array ? new Uint8Array(rawBytes) : new Uint8Array(rawBytes || []);
        return {
          path: target.path,
          type: 'file',
          content,
          size: content.length,
          mode,
          mtime: Number(st.mtime || Date.now())
        };
      } catch (err) {
        if (isNotFoundError(err)) return null;
        throw translateError(err);
      }
    }

    /**
     * Writes a file or creates a directory.
     *
     * @param {object} entry
     */
    async putEntry(entry) {
      if (!entry || !entry.path) {
        throw new Error('putEntry requires entry.path');
      }
      const normPath = normalizePath(entry.path);
      const target = this.resolveMount(normPath);

      if (target.kind === 'base') {
        return this.baseFS.putEntry({ ...entry, path: normPath });
      }
      if (target.kind === 'multi-root' || target.kind === 'not-found') {
        const e = new Error('EACCES: cannot write directly to multi-root container');
        e.code = 'EACCES';
        throw e;
      }

      if (typeof entry.mode === 'number') {
        this.modeOverrides.set(normPath, entry.mode);
      }

      try {
        if (entry.type === 'dir') {
          await this.vscode.workspace.fs.createDirectory(target.uri);
          return;
        }

        const rawContent = entry.content instanceof Uint8Array
          ? entry.content
          : new Uint8Array(entry.content || []);
        const exactLen = typeof entry.size === 'number' && entry.size <= rawContent.length
          ? entry.size
          : rawContent.length;
        const bytesToWrite = rawContent.subarray(0, exactLen);
        await this.vscode.workspace.fs.writeFile(target.uri, bytesToWrite);
      } catch (err) {
        throw translateError(err);
      }
    }

    /**
     * Removes a file or directory.
     *
     * @param {string} guestPath
     */
    async removeEntry(guestPath) {
      const target = this.resolveMount(guestPath);
      if (target.kind === 'base') {
        return this.baseFS.removeEntry(target.path);
      }
      if (target.kind === 'not-found') {
        return false;
      }
      if (target.kind === 'multi-root' || target.isMountRoot) {
        const e = new Error('EBUSY: cannot remove workspace root');
        e.code = 'EBUSY';
        throw e;
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

    /**
     * Renames/moves a file or directory, supporting both intra-workspace native
     * renames and cross-mount transfers (`/tmp` <-> `/workspace`).
     *
     * @param {string} srcPath
     * @param {string} dstPath
     */
    async renameEntry(srcPath, dstPath) {
      const srcTarget = this.resolveMount(srcPath);
      const dstTarget = this.resolveMount(dstPath);

      if (srcTarget.kind === 'vscode' && dstTarget.kind === 'vscode') {
        try {
          await this.vscode.workspace.fs.rename(srcTarget.uri, dstTarget.uri, { overwrite: true });
          if (this.modeOverrides.has(srcTarget.path)) {
            this.modeOverrides.set(dstTarget.path, this.modeOverrides.get(srcTarget.path));
            this.modeOverrides.delete(srcTarget.path);
          }
          return true;
        } catch (err) {
          if (isNotFoundError(err)) return false;
          throw translateError(err);
        }
      }

      // Cross-mount or baseFS fallback
      const ent = await this.getEntry(srcPath);
      if (!ent) return false;
      ent.path = normalizePath(dstPath);
      await this.putEntry(ent);
      await this.removeEntry(srcPath);
      return true;
    }

    /**
     * Lists direct child entries of a directory.
     *
     * @param {string} dirPath
     */
    async readdir(dirPath) {
      const normDir = normalizePath(dirPath);

      if (normDir === '/') {
        const baseChildren = await this.baseFS.readdir('/');
        const mountName = this.mountPoint.slice(1);
        if (!mountName.includes('/') && !baseChildren.some(c => c.path === this.mountPoint)) {
          baseChildren.push({
            path: this.mountPoint,
            type: 'dir',
            size: 0,
            mode: 0o755,
            mtime: Date.now()
          });
        }
        return baseChildren;
      }

      const target = this.resolveMount(normDir);
      if (target.kind === 'base') {
        return this.baseFS.readdir(target.path);
      }
      if (target.kind === 'not-found') {
        const e = new Error('ENOENT');
        e.code = 'ENOENT';
        throw e;
      }
      if (target.kind === 'multi-root') {
        const now = Date.now();
        return target.folders.map((f, idx) => ({
          path: `${this.mountPoint}/${f.name || `folder-${idx}`}`,
          type: 'dir',
          size: 0,
          mode: 0o755,
          mtime: now
        }));
      }

      try {
        const pairs = await this.vscode.workspace.fs.readDirectory(target.uri);
        const now = Date.now();
        const prefix = target.path === '/' ? '' : target.path;
        return pairs.map(([name, ft]) => {
          const childPath = `${prefix}/${name}`;
          const type = mapVSCodeFileType(ft);
          return {
            path: childPath,
            type,
            size: 0,
            mode: this.modeOverrides.get(childPath) ?? (type === 'dir' ? 0o755 : 0o644),
            mtime: now
          };
        });
      } catch (err) {
        throw translateError(err);
      }
    }
  }

  const exportsObj = {
    VSCodeMountedFS,
    normalizePath,
    FILE_TYPE_UNKNOWN,
    FILE_TYPE_FILE,
    FILE_TYPE_DIRECTORY,
    FILE_TYPE_SYMLINK
  };

  if (typeof root !== 'undefined') {
    root.KabibiVSCodeVFS = exportsObj;
  }
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = exportsObj;
  }
})(typeof globalThis !== 'undefined' ? globalThis : typeof self !== 'undefined' ? self : this);
