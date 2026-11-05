## Goal and scope

Move directory-mount implementation into Kabibi’s existing embedded browser runtime so the **built Kabibi vegetable** carries the feature. Do not introduce a new deployable JS file. Keep the feature Kabibi-initiated: once Kabibi installs it, filesystem calls from that active WASM guest use the mounted directory; another Rusticated WASM guest does not gain mounting automatically.

The current implementation is a Washmhost-side wrapper loaded before the guest starts, and the release builder stages it separately. The change must eliminate that dependency—not merely stop copying the file—while preserving Kabibi’s existing picker, mount dialog, shell, and file-manager behavior.

## Implementation plan

1. **Move the adapter into Kabibi’s existing embedded runtime.**  
   Transfer the implementation from `mount_fs.js` into the existing `main.js`. That file is already embedded by `assets.go` and evaluated by `install_wasip1.go`, so it can ship as part of the vegetable without a new runtime asset.

2. **Install the adapter when Kabibi boots.**  
   In Kabibi’s guest-boot path, obtain the active host and its filesystem through the existing `getFS()` controller API. Install the mount behavior idempotently before Kabibi offers the mount action. Preserve the underlying filesystem methods separately so adapter calls delegate to the original filesystem without recursion. Keep path validation, overlap checks, error translation, and unsupported-operation errors.

   The host’s WASI imports retain a reference to the filesystem object and call its methods dynamically. The implementation must confirm this seam in tests; it should not assume that replacing the object after host creation will redirect those imports.

3. **Keep the user-facing flow in Kabibi.**  
   Retain the browser directory picker and pending selection in the embedded Kabibi runtime. Change the mount action to call the adapter Kabibi installed on the active host filesystem, rather than relying on Washmhost’s mount-specific forwarding method. Preserve user-gesture requirements for opening the picker and keep read/write permission behavior explicit.

4. **Remove the Washmhost-side mount implementation and loader.**  
   Update `index.js` to stop dynamically loading `mount_fs.js`, wrapping the filesystem with its factory, or exposing mount-specific forwarding if Kabibi no longer needs it. Washmhost should continue to provide the generic filesystem and WASI boundary; it should not own the mount feature.

5. **Remove the sidecar from all release paths.**  
   Update `release/main.go` to remove both `mount_fs.js` staging entries and revise the bundle status message. Also remove any stale, previously generated copy from the exact output locations (`release/dist/mount_fs.js` and `kabibi/vscode/mount_fs.js`), handling “not found” normally and surfacing other removal errors. Removing copy entries alone would leave old output files behind.

6. **Keep the VS Code package working without that file.**  
   `extension.js` currently loads and requires the adapter. Remove that dependency and verify it can initialize Washmhost with the existing `vfs.js`, which already provides the VS Code workspace filesystem. Update `vfs.test.js` accordingly. Do not remove the existing Washmhost or VFS assets; this change removes the mount adapter sidecar, not the browser host runtime.

7. **Retire the standalone mount tests and document the new boundary.**  
   Move their behavioral coverage into the existing `main.test.cjs`, then remove references to the standalone test and asset. Update `browser README` to say the mount implementation is embedded in Kabibi and does not ship as a separate file.

## Test plan

### Unit tests

In Kabibi’s existing browser test suite, use fake filesystem and directory-handle objects to cover:

- Mount path normalization; traversal, `/`, `/dev`, occupied paths, and overlapping mounts are rejected.
- Parent directories appear as needed; mounted entries support stat, listing, reads, writes, nested directory creation, deletion, and rename/copy behavior across mounted and base filesystems.
- Existing path resolution into a mount behaves as before.
- Browser errors map to explicit guest filesystem errors, including denied and revoked access.
- Unsupported symlink creation and timestamp changes fail explicitly; permission bits remain guest-side metadata.
- Reinstalling the adapter does not stack wrappers or corrupt delegation.
- A non-Kabibi guest does not get the Kabibi mount bootstrap.

### Washmhost and VS Code regression tests

- Washmhost starts with no mount module present and does not try to fetch or evaluate `mount_fs.js`.
- Its ordinary filesystem and WASI imports still work without the adapter.
- The Kabibi-installed adapter affects later calls through the same filesystem object used by the WASI imports.
- VS Code extension initialization and workspace reads/writes work without loading the adapter; preserve multi-root behavior.

### Built-vegetable integration

Build the actual artifact through the repository’s build path, then run that artifact in the browser. Confirm it boots without a sidecar, invokes the picker only after a user action, mounts a temporary directory, and lets Kabibi shell commands and the dual-pane file manager read and modify it. Check the actual host directory afterward. Also verify the documented session behavior: reload loses the directory handle, and unsupported browser APIs or permission failures produce clear errors.

### Release verification

Run the release builder and inspect the resulting website and VS Code package. Confirm neither contains `mount_fs.js`, and confirm the built Kabibi vegetable still mounts successfully. This verifies the actual packaging requirement, not just the source-level tests.

## Validation commands

After implementation, run the focused JS tests, updated to exclude the retired standalone mount test and include the VS Code VFS test:

```powershell
node --test .\kabibi\browser\main.test.cjs .\mohabbat\washmhost\js\browser\index.test.js .\kabibi\vscode\test\vfs.test.js
```

Build the Kabibi vegetable with the repository’s builder:

```powershell
go run . kabibi -o release\dist\kabibi.bat
```

Then validate full release staging:

```powershell
go run .\release
```

The project uses custom Rusticated build practices; use these repository workflows rather than substituting a plain `cargo build`. If focused tests and release checks pass, run the broader release test option (`go run .\release --test-all`) as the final regression check.

## Acceptance criteria and key risk

The change is complete only if the **actual vegetable** provides mounting; no release output contains the mount sidecar; Washmhost and VS Code start without it; and the mounted directory works through Kabibi’s ABI filesystem operations.

The main technical risk is installing a wrapper after the WASM host has started. Tests must prove that the WASI imports’ live filesystem reference observes the installed behavior and that original filesystem methods remain callable without recursion. If that seam does not hold, stop and revisit the design rather than reintroducing a separate mount implementation in Washmhost.