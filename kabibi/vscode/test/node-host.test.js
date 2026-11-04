const test = require('node:test');
const assert = require('node:assert/strict');
const os = require('node:os');
const path = require('node:path');

const hostHelpersPromise = import('../../../mohabbat/washmhost/js/node/index.js');

test('Node washmhost maps the guest workspace mount to its host mount', async (t) => {
  const { translatePath, virtualizeWorkspacePath } = await hostHelpersPromise;
  const previousMount = process.env.MOHABBAT_WORKSPACE_MOUNT;
  const previousRoots = process.env.MOHABBAT_WORKSPACE_ROOTS;
  t.after(() => {
    if (previousMount === undefined) delete process.env.MOHABBAT_WORKSPACE_MOUNT;
    else process.env.MOHABBAT_WORKSPACE_MOUNT = previousMount;
    if (previousRoots === undefined) delete process.env.MOHABBAT_WORKSPACE_ROOTS;
    else process.env.MOHABBAT_WORKSPACE_ROOTS = previousRoots;
  });

  const mount = path.join(os.tmpdir(), 'kabibi-workspace-mount');
  const workspace = path.join(os.tmpdir(), 'kabibi-workspace-real');
  process.env.MOHABBAT_WORKSPACE_MOUNT = mount;
  process.env.MOHABBAT_WORKSPACE_ROOTS = JSON.stringify([
    { host: workspace, guest: '/workspace' }
  ]);

  assert.equal(translatePath('/workspace'), mount);
  assert.equal(translatePath('/workspace/nested/file.txt'), path.join(mount, 'nested', 'file.txt'));
  assert.equal(translatePath('/workspace/../outside'), '/outside');
  assert.equal(virtualizeWorkspacePath(path.join(workspace, 'nested')), '/workspace/nested');
});

test('Node washmhost reverses multi-root host paths to disambiguated guest paths', async (t) => {
  const { virtualizeWorkspacePath } = await hostHelpersPromise;
  const previousRoots = process.env.MOHABBAT_WORKSPACE_ROOTS;
  t.after(() => {
    if (previousRoots === undefined) delete process.env.MOHABBAT_WORKSPACE_ROOTS;
    else process.env.MOHABBAT_WORKSPACE_ROOTS = previousRoots;
  });

  const first = path.join(os.tmpdir(), 'kabibi-first-root');
  const second = path.join(os.tmpdir(), 'kabibi-second-root');
  process.env.MOHABBAT_WORKSPACE_ROOTS = JSON.stringify([
    { host: first, guest: '/workspace/project' },
    { host: second, guest: '/workspace/project-2' }
  ]);

  assert.equal(virtualizeWorkspacePath(path.join(second, 'src', 'main.go')), '/workspace/project-2/src/main.go');
});
