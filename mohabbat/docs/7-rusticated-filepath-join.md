# Rusticated Go `filepath.Join` Compatibility

**Status:** Specification

## 1. Purpose

Specify a fix for Rusticated Go's `filepath.Join` behavior when a later
component begins with a path separator. The Rusticated overlay currently treats
such a component as a reset of the accumulated path. Go's `filepath.Join`
instead joins and cleans the elements; it does not use a later rooted element
to discard the earlier prefix.

This discrepancy breaks consumers such as go-billy's chroot filesystem, which
joins its host root with slash-prefixed paths from its virtual filesystem.

## 2. Intended Go Semantics

`filepath.Join` is lexical path composition, not path resolution. It combines
the supplied elements using the platform's path rules and cleans the result.
A later component beginning with `/` is not, by itself, a request to discard
the preceding components. The Go documentation demonstrates this on Unix:

```go
filepath.Join("a/b", "/c") == "a/b/c"
```

Windows has additional volume, separator, and UNC rules, implemented in its
own join logic. The Rusticated overlay must reproduce the relevant Go
`filepath.Join` semantics for its configured path model; it must not apply a
generic “absolute component replaces the prefix” rule.

This distinction matters for go-billy. Its `ChrootHelper` accepts a
slash-prefixed path as a path from the virtual filesystem root, then joins it
to the underlying filesystem root. Consequently, joining a host root with `/`
must remain at that host root, and joining it with `/child` must remain below
that root. This behavior is path composition, not a security guarantee:
go-billy explicitly documents that `ChrootHelper` is not itself a security
boundary.

## 3. Historical Context

Go's `path/filepath.Clean` documentation points to Rob Pike's Plan 9 paper,
“Lexical File Names in Plan 9 or Getting Dot-Dot Right,” as background for
lexical path cleaning. On Unix-like paths, `Join` composes the elements and
cleans the combined path. That is why a later slash-prefixed element is not
treated as a navigation operation that resets the base.

Do not overstate this history: the Plan 9 connection is documented for the
lexical cleaning model; it is not, by itself, evidence for the detailed
behavior of every platform-specific `Join` implementation. In particular,
Windows joining has explicit volume and UNC handling. The relevant requirement
here is Go API compatibility, not a claim that all programming-language
standard libraries use the same rule.

## 4. Relevant Go Issues

These issues provide useful context. They are not all direct reports of the
Rusticated regression, and should not be cited as if they were.

1. [golang/go#11551 — document Windows drive-relative `Join` behavior](https://github.com/golang/go/issues/11551)
   - Records a historical debate over whether `filepath.Join("c:", "a")`
     should insert a separator on Windows. The issue's original reproducer and
     title describe an older behavior; the
     [current Windows implementation](https://go.dev/src/path/filepath/path_windows.go)
     documents `Join("C:", "f")` as `C:f` and `Join("C:", "\f")` as `C:\f`.
   - Relevant as an example of platform-specific path behavior and
     compatibility expectations; it is not about a later `/` component.

2. [golang/go#20126 — proposal to add `SecureJoin`](https://github.com/golang/go/issues/20126)
   - Discusses the need for filesystem-aware, rooted path resolution in
     security-sensitive code.
   - Relevant because ordinary `filepath.Join` is lexical composition, not
     symlink-aware resolution or a containment boundary.

3. [golang/go#56219 — add `filepath.IsLocal`](https://github.com/golang/go/issues/56219)
   - The accepted proposal led to `filepath.IsLocal` in Go 1.20, providing a
     separate way to validate that a path is local before joining it to a
     base.
   - This reinforces that `Join` itself is not an input-validation or
     containment API; it does not redefine `Join`.

4. [golang/go#56336 — Windows UNC path handling](https://github.com/golang/go/issues/56336)
   - Reports edge cases involving incomplete UNC paths and Windows volume
     parsing.
   - Relevant as a warning that Windows join behavior must retain its
     platform-specific cases; it is not evidence for the rooted-element
     behavior described in this specification.

## 5. Required Change and Scope

1. Update the Rusticated Go standard-library **overlay** implementation of
   filepath joining, centered on
   [`rusticated-go/internal/filepathlite/path_nonwindows.go`](../../rusticated-go/internal/filepathlite/path_nonwindows.go).
   The overlay's `CoreJoin` must preserve the accumulated base when a later
   component is rooted, in keeping with Go's `filepath.Join` behavior.
2. Keep the existing overlay wiring in
   [`mohabbat/prebuild.go`](../prebuild.go) pointed at the Rusticated
   implementation, updating it only if required to make the corrected
   implementation take effect.
3. Add regression tests that exercise the public `filepath.Join` behavior
   through the Rusticated overlay, rather than validating only the native
   host's standard library.
4. Do **not** change the Rusticated ABI, platform-info ABI, host-side path
   operations, or path-separator values. This is a Go standard-library
   overlay compatibility fix, not an ABI change.
5. Do **not** patch go-billy or go-git to compensate for the overlay's
   incompatible join behavior.

## 6. Acceptance Criteria

1. Under the Rusticated overlay with Windows-style path separators:

   ```text
   filepath.Join(`C:\base`, `/`)     -> `C:\base`
   filepath.Join(`C:\base`, `/child`) -> `C:\base\child`
   ```

   The results must be cleaned according to Go's `filepath.Join` contract.
2. Add cases for ordinary relative elements and relevant Windows volume/UNC
   paths so the fix does not regress existing path handling.
3. Run the focused Rusticated filepath tests using the generated Go overlay.
4. Run the consumer regression tests for repository discovery and Kabibi's
   Git-log pager under Rusticated. A native-only pass is insufficient.

## 7. Explicit Non-Goals

- Do not change Go's documented `filepath.Join` semantics.
- Do not make `Join` resolve symlinks, sanitize untrusted paths, or enforce a
  chroot/security boundary.
- Do not change the Rusticated ABI or host contract.
- Do not modify vendored go-billy or go-git code to work around this overlay
  discrepancy.
