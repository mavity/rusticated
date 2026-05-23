//! Pure compile-target path-convention utilities.
//!
//! These functions answer "what counts as a path separator on the host this
//! shell was compiled for?" They are *not* on the [`crate::Platform`] trait
//! because no `Platform` implementation has any business returning a
//! different answer than the compile target dictates: `brush-core`'s glob
//! expansion, redirection parsing, and PATH search are wired against
//! `cfg(unix)` / `cfg(windows)` invariants at build time.
//!
//! Exposing them as free `cfg`-gated functions keeps `brush-core` itself
//! free of `cfg(unix|windows)` blocks while preserving zero-cost dispatch.

#![allow(clippy::missing_const_for_fn)]

use crate::borrow::Cow;
use crate::ops::Deref;
use crate::string::{String, ToString};

// --- Path (borrowed DST) -----------------------------------------------------

/// A borrowed, immutable path slice - analogous to `std::path::Path`.
///
/// `Path` is a DST (`repr(transparent)` over `str`). Obtain a reference via
/// [`Path::new`] or by dereferencing a [`PathBuf`].
#[repr(transparent)]
#[derive(PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Path(str);

impl Path {
    /// Coerce a string slice to a `Path`.
    ///
    /// This is a zero-cost operation; no allocation is performed.
    pub fn new<S: AsRef<str> + ?Sized>(s: &S) -> &Self {
        // SAFETY: `Path` is `repr(transparent)` over `str`; the pointer cast
        // is valid because both types have identical memory layouts.
        unsafe { &*(s.as_ref() as *const str as *const Path) }
    }

    /// Converts the path to a UTF-16 wide string with a null terminator.
    #[cfg(windows)]
    pub fn to_wide_null(&self) -> crate::vec::Vec<u16> {
        self.0.encode_utf16().chain(core::iter::once(0)).collect()
    }

    /// Converts the `Path` to an owned [`PathBuf`].
    pub fn to_owned(&self) -> PathBuf {
        PathBuf::from(self.0.to_string())
    }
}

impl AsRef<Path> for String {
    fn as_ref(&self) -> &Path {
        Path::new(self)
    }
}

impl AsRef<Path> for str {
    fn as_ref(&self) -> &Path {
        Path::new(self)
    }
}

impl AsRef<Path> for crate::borrow::Cow<'_, str> {
    fn as_ref(&self) -> &Path {
        Path::new(self.as_ref() as &str)
    }
}

impl crate::borrow::Borrow<Path> for PathBuf {
    fn borrow(&self) -> &Path {
        self.as_path()
    }
}

impl crate::borrow::ToOwned for Path {
    type Owned = PathBuf;
    fn to_owned(&self) -> PathBuf {
        PathBuf::from(self.0.to_string())
    }
}

impl Path {
    /// Returns the path as a `str` slice.
    #[must_use]
    pub fn as_str(&self) -> &str {
        &self.0
    }

    /// Returns the path as a byte slice.
    #[must_use]
    pub fn as_bytes(&self) -> &[u8] {
        self.0.as_bytes()
    }

    /// Returns an owned [`PathBuf`] with the contents of this path.
    #[must_use]
    pub fn to_path_buf(&self) -> PathBuf {
        PathBuf::from(self.as_str())
    }

    /// Returns `true` if this path is absolute.
    #[must_use]
    pub fn is_absolute(&self) -> bool {
        self.0.starts_with('/') || {
            #[cfg(windows)]
            {
                // Windows: `C:\` or `\\server`
                let b = self.0.as_bytes();
                (b.len() >= 3
                    && b[0].is_ascii_alphabetic()
                    && b[1] == b':'
                    && (b[2] == b'\\' || b[2] == b'/'))
                    || (b.len() >= 2 && b[0] == b'\\' && b[1] == b'\\')
            }
            #[cfg(not(windows))]
            false
        }
    }

    /// Returns `true` if the path is relative.
    #[must_use]
    pub fn is_relative(&self) -> bool {
        !self.is_absolute()
    }

    /// Returns the final component of the path, or `None` for a bare root.
    #[must_use]
    pub fn file_name(&self) -> Option<&str> {
        let s = strip_trailing_separators(self.as_str());
        if s.is_empty() {
            return None;
        }
        match rfind_sep(s) {
            None => Some(s),
            Some(i) => Some(&s[i + 1..]),
        }
    }

    /// Returns the stem of the final component (filename without extension).
    #[must_use]
    pub fn file_stem(&self) -> Option<&str> {
        self.file_name().map(|name| match name.rfind('.') {
            None | Some(0) => name,
            Some(i) => &name[..i],
        })
    }

    /// Returns the extension of the final component, without the leading dot.
    #[must_use]
    pub fn extension(&self) -> Option<&str> {
        self.file_name().and_then(|name| {
            let i = name.rfind('.')?;
            if i == 0 {
                None // dotfile like `.bashrc` has no extension
            } else {
                Some(&name[i + 1..])
            }
        })
    }

    /// Returns the parent directory, or `None` if this is a root or bare filename.
    #[must_use]
    pub fn parent(&self) -> Option<&Path> {
        let s = strip_trailing_separators(self.as_str());
        let i = rfind_sep(s)?;
        let parent = if i == 0 {
            &s[..1] // root "/"
        } else {
            &s[..i]
        };
        Some(Path::new(parent))
    }

    /// Joins this path with `other`, returning a new [`PathBuf`].
    ///
    /// If `other` is absolute it replaces `self`; otherwise it is appended.
    #[must_use]
    pub fn join<P: AsRef<Path>>(&self, other: P) -> PathBuf {
        let other = other.as_ref();
        if other.is_absolute() {
            other.to_path_buf()
        } else {
            let mut buf = self.to_path_buf();
            buf.push(other.as_str());
            buf
        }
    }

    /// Returns `true` if `self` starts with `base` as a path prefix.
    #[must_use]
    pub fn starts_with<P: AsRef<Path>>(&self, base: P) -> bool {
        let base = base.as_ref().as_str();
        self.as_str() == base
            || self.as_str().starts_with(&alloc::format!("{base}/"))
            || self.as_str().starts_with(&alloc::format!("{base}\\"))
    }

    /// Returns `true` if `self` ends with `child` as a path suffix.
    #[must_use]
    pub fn ends_with<P: AsRef<Path>>(&self, child: P) -> bool {
        let child = child.as_ref().as_str();
        self.as_str() == child
            || self.as_str().ends_with(&alloc::format!("/{child}"))
            || self.as_str().ends_with(&alloc::format!("\\{child}"))
    }

    /// Strips `prefix` from this path, returning the remainder.
    ///
    /// Returns `None` if `self` does not start with `prefix`.
    #[must_use]
    pub fn strip_prefix<P: AsRef<Path>>(&self, prefix: P) -> Option<&Path> {
        let prefix = prefix.as_ref().as_str();
        let s = self.as_str();
        if s == prefix {
            return Some(Path::new(""));
        }
        let sep_prefix = alloc::format!("{prefix}/");
        if s.starts_with(sep_prefix.as_str()) {
            return Some(Path::new(&s[sep_prefix.len()..]));
        }
        #[cfg(windows)]
        {
            let win_sep_prefix = alloc::format!("{prefix}\\");
            if s.starts_with(win_sep_prefix.as_str()) {
                return Some(Path::new(&s[win_sep_prefix.len()..]));
            }
        }
        None
    }

    /// Returns the metadata for the file at this path (async).
    #[cfg(not(target_arch = "wasm32"))]
    pub async fn metadata(&self) -> crate::io::Result<crate::fs::Metadata> {
        crate::fs::metadata(self.as_str()).await
    }

    /// Query metadata for this path (sync).
    #[cfg(not(target_arch = "wasm32"))]
    pub fn metadata_sync(&self) -> crate::io::Result<crate::fs::Metadata> {
        crate::fs::metadata_sync(self.as_str())
    }

    /// Returns `true` if the path exists on disk (async).
    #[cfg(not(target_arch = "wasm32"))]
    pub async fn exists(&self) -> bool {
        self.metadata().await.is_ok()
    }

    /// Returns `true` if the path exists on disk (sync).
    #[cfg(not(target_arch = "wasm32"))]
    pub fn exists_sync(&self) -> bool {
        self.metadata_sync().is_ok()
    }

    /// Returns `true` if the path exists and is a regular file (async).
    #[cfg(not(target_arch = "wasm32"))]
    pub async fn is_file(&self) -> bool {
        self.metadata().await.map(|m| m.is_file()).unwrap_or(false)
    }

    /// Returns `true` if the path exists and is a regular file (sync).
    #[cfg(not(target_arch = "wasm32"))]
    pub fn is_file_sync(&self) -> bool {
        self.metadata_sync().map(|m| m.is_file()).unwrap_or(false)
    }

    /// Returns `true` if the path exists and is a directory (async).
    #[cfg(not(target_arch = "wasm32"))]
    pub async fn is_dir(&self) -> bool {
        self.metadata().await.map(|m| m.is_dir()).unwrap_or(false)
    }

    /// Returns `true` if the path exists and is a directory (sync).
    #[cfg(not(target_arch = "wasm32"))]
    pub fn is_dir_sync(&self) -> bool {
        self.metadata_sync().map(|m| m.is_dir()).unwrap_or(false)
    }

    /// Returns `true` if the path exists and is a symbolic link (async).
    #[cfg(not(target_arch = "wasm32"))]
    pub async fn is_symlink(&self) -> bool {
        crate::fs::symlink_metadata(self.as_str())
            .await
            .map(|m| m.is_symlink())
            .unwrap_or(false)
    }

    /// Returns `true` if the path exists and is a symbolic link (sync).
    #[cfg(not(target_arch = "wasm32"))]
    pub fn is_symlink_sync(&self) -> bool {
        crate::fs::symlink_metadata_sync(self.as_str())
            .map(|m| m.is_symlink())
            .unwrap_or(false)
    }

    /// Read the directory entries at this path (async).
    #[cfg(not(target_arch = "wasm32"))]
    pub async fn read_dir(&self) -> crate::io::Result<crate::fs::ReadDir> {
        crate::fs::read_dir(self.as_str()).await
    }

    /// Returns the canonical form of the path (async).
    #[cfg(not(target_arch = "wasm32"))]
    pub async fn canonicalize(&self) -> crate::io::Result<PathBuf> {
        crate::fs::canonicalize(self.as_str()).await
    }

    /// Returns the canonical form of the path (sync).
    #[cfg(not(target_arch = "wasm32"))]
    pub fn canonicalize_sync(&self) -> crate::io::Result<PathBuf> {
        crate::fs::canonicalize_sync(self.as_str())
    }

    /// Returns a displayable object for this path.
    #[must_use]
    pub fn display(&self) -> PathDisplay<'_> {
        PathDisplay(self)
    }

    /// Iterates over the components of this path.
    #[must_use]
    pub fn components(&self) -> Components<'_> {
        Components::new(self.as_str())
    }

    /// Returns the path as a string.
    pub fn to_str(&self) -> Option<&str> {
        Some(self.as_str())
    }

    /// Returns the path as a string (lossy).
    pub fn to_string_lossy(&self) -> crate::string::String {
        crate::string::String::from(self.as_str())
    }
}

impl AsRef<Path> for Path {
    fn as_ref(&self) -> &Path {
        self
    }
}

impl core::fmt::Debug for Path {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        write!(f, "{:?}", &self.0)
    }
}

impl core::fmt::Display for Path {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.write_str(&self.0)
    }
}

/// Helper returned by [`Path::display`].
pub struct PathDisplay<'a>(&'a Path);

impl core::fmt::Display for PathDisplay<'_> {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.write_str(self.0.as_str())
    }
}

// --- PartialEq / PartialOrd implementations for Path / PathBuf --------------

impl PartialEq<PathBuf> for Path {
    fn eq(&self, other: &PathBuf) -> bool {
        self == other.as_path()
    }
}

impl PartialEq<Path> for PathBuf {
    fn eq(&self, other: &Path) -> bool {
        self.as_path() == other
    }
}

impl PartialEq<str> for Path {
    fn eq(&self, other: &str) -> bool {
        self.as_str() == other
    }
}

impl PartialEq<Path> for str {
    fn eq(&self, other: &Path) -> bool {
        self == other.as_str()
    }
}

impl PartialEq<&str> for Path {
    fn eq(&self, other: &&str) -> bool {
        self.as_str() == *other
    }
}

impl PartialEq<Path> for &str {
    fn eq(&self, other: &Path) -> bool {
        *self == other.as_str()
    }
}

impl PartialEq<String> for Path {
    fn eq(&self, other: &String) -> bool {
        self.as_str() == other.as_str()
    }
}

impl PartialEq<Path> for String {
    fn eq(&self, other: &Path) -> bool {
        self.as_str() == other.as_str()
    }
}

impl PartialEq<str> for PathBuf {
    fn eq(&self, other: &str) -> bool {
        self.as_str() == other
    }
}

impl PartialEq<PathBuf> for str {
    fn eq(&self, other: &PathBuf) -> bool {
        self == other.as_str()
    }
}

impl PartialEq<&str> for PathBuf {
    fn eq(&self, other: &&str) -> bool {
        self.as_str() == *other
    }
}

impl PartialEq<PathBuf> for &str {
    fn eq(&self, other: &PathBuf) -> bool {
        *self == other.as_str()
    }
}

impl PartialEq<String> for PathBuf {
    fn eq(&self, other: &String) -> bool {
        self.0 == *other
    }
}

impl PartialEq<PathBuf> for String {
    fn eq(&self, other: &PathBuf) -> bool {
        *self == other.0
    }
}

impl PartialOrd<PathBuf> for Path {
    fn partial_cmp(&self, other: &PathBuf) -> Option<core::cmp::Ordering> {
        self.partial_cmp(other.as_path())
    }
}

impl PartialOrd<Path> for PathBuf {
    fn partial_cmp(&self, other: &Path) -> Option<core::cmp::Ordering> {
        self.as_path().partial_cmp(other)
    }
}

impl PartialOrd<str> for Path {
    fn partial_cmp(&self, other: &str) -> Option<core::cmp::Ordering> {
        self.as_str().partial_cmp(other)
    }
}

impl PartialOrd<Path> for str {
    fn partial_cmp(&self, other: &Path) -> Option<core::cmp::Ordering> {
        self.partial_cmp(other.as_str())
    }
}

impl PartialOrd<str> for PathBuf {
    fn partial_cmp(&self, other: &str) -> Option<core::cmp::Ordering> {
        self.as_str().partial_cmp(other)
    }
}

impl PartialOrd<PathBuf> for str {
    fn partial_cmp(&self, other: &PathBuf) -> Option<core::cmp::Ordering> {
        self.partial_cmp(other.as_str())
    }
}

// --- Path components ---------------------------------------------------------

/// A single component of a path.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Component<'a> {
    /// The root directory separator (`/` on Unix, `\` or `C:\` on Windows).
    RootDir,
    /// A relative directory component (`..`).
    ParentDir,
    /// The current directory (`.`).
    CurDir,
    /// A regular path component.
    Normal(&'a str),
}

impl<'a> Component<'a> {
    /// Returns the component as a `str` slice.
    #[must_use]
    pub fn as_str(&self) -> &'a str {
        match self {
            Component::RootDir => "/",
            Component::ParentDir => "..",
            Component::CurDir => ".",
            Component::Normal(s) => s,
        }
    }
}

/// Iterator over the [`Component`]s of a [`Path`].
pub struct Components<'a> {
    remaining: &'a str,
    prefix_done: bool,
}

impl<'a> Components<'a> {
    fn new(s: &'a str) -> Self {
        Self {
            remaining: s,
            prefix_done: false,
        }
    }
}

impl<'a> Iterator for Components<'a> {
    type Item = Component<'a>;

    fn next(&mut self) -> Option<Component<'a>> {
        if !self.prefix_done
            && (self.remaining.starts_with('/') || self.remaining.starts_with('\\'))
        {
            self.prefix_done = true;
            self.remaining = self.remaining.trim_start_matches(['/', '\\']);
            return Some(Component::RootDir);
        }
        self.prefix_done = true;
        // Skip consecutive separators.
        self.remaining = self.remaining.trim_start_matches(['/', '\\']);
        if self.remaining.is_empty() {
            return None;
        }
        let end = self
            .remaining
            .find(['/', '\\'])
            .unwrap_or(self.remaining.len());
        let part = &self.remaining[..end];
        self.remaining = &self.remaining[end..];
        Some(match part {
            ".." => Component::ParentDir,
            "." => Component::CurDir,
            other => Component::Normal(other),
        })
    }
}

// --- Internal helpers ---------------------------------------------------------

/// Strip trailing `/` or `\` characters (keeping a bare root `/`).
fn strip_trailing_separators(s: &str) -> &str {
    let t = s.trim_end_matches(['/', '\\']);
    if t.is_empty() && !s.is_empty() {
        &s[..1] // preserve bare root "/"
    } else {
        t
    }
}

/// Index of the last `/` or `\` in `s`.
fn rfind_sep(s: &str) -> Option<usize> {
    s.rfind(['/', '\\'])
}

// --- PathBuf -----------------------------------------------------------------

/// An owned, mutable platform path string.
#[derive(Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct PathBuf(String);

impl PathBuf {
    /// Creates a new, empty `PathBuf`.
    pub fn new() -> Self {
        Self(String::new())
    }

    /// Extends the path with a component.
    pub fn push(&mut self, component: &str) {
        if !self.0.is_empty() && !matches!(self.0.as_bytes().last(), Some(b'/' | b'\\')) {
            self.0.push('/');
        }
        self.0.push_str(component);
    }

    /// Returns the path as a `str`.
    #[must_use]
    pub fn as_str(&self) -> &str {
        &self.0
    }

    /// Returns the path as a byte slice.
    #[must_use]
    pub fn as_bytes(&self) -> &[u8] {
        self.0.as_bytes()
    }

    /// Returns a mutable reference to the inner [`String`].
    pub fn as_mut_string(&mut self) -> &mut String {
        &mut self.0
    }

    /// Returns a borrowed [`Path`] slice of this `PathBuf`.
    #[must_use]
    pub fn as_path(&self) -> &Path {
        Path::new(self.0.as_str())
    }

    /// Returns the path as a string.
    #[must_use]
    #[allow(clippy::inherent_to_string_shadow_display)]
    pub fn to_string(&self) -> String {
        self.0.clone()
    }

    /// Returns the path as a string slice (lossy).
    #[must_use]
    pub fn to_string_lossy(&self) -> Cow<'_, str> {
        Cow::Borrowed(self.0.as_str())
    }

    /// Returns the metadata for the file at this path (async).
    #[cfg(not(target_arch = "wasm32"))]
    pub async fn metadata(&self) -> crate::io::Result<crate::fs::Metadata> {
        self.as_path().metadata().await
    }
}

impl Deref for PathBuf {
    type Target = Path;
    fn deref(&self) -> &Path {
        self.as_path()
    }
}

impl AsRef<Path> for PathBuf {
    fn as_ref(&self) -> &Path {
        self.as_path()
    }
}

impl From<&str> for PathBuf {
    fn from(s: &str) -> Self {
        Self(s.into())
    }
}

impl From<String> for PathBuf {
    fn from(s: String) -> Self {
        Self(s)
    }
}

impl From<&String> for PathBuf {
    fn from(s: &String) -> Self {
        Self(s.clone())
    }
}

impl From<&Path> for PathBuf {
    fn from(p: &Path) -> Self {
        Self(p.as_str().into())
    }
}

impl<'a> From<&'a Path> for Cow<'a, Path> {
    fn from(p: &'a Path) -> Self {
        Cow::Borrowed(p)
    }
}

impl From<PathBuf> for Cow<'_, Path> {
    fn from(p: PathBuf) -> Self {
        Cow::Owned(p)
    }
}

impl AsRef<str> for PathBuf {
    fn as_ref(&self) -> &str {
        &self.0
    }
}

impl core::fmt::Display for PathBuf {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.write_str(&self.0)
    }
}

#[cfg(any(windows, target_os = "redox"))]
const PATH_SEPARATORS: [char; 2] = ['/', '\\'];

#[cfg(not(any(windows, target_os = "redox")))]
const PATH_SEPARATOR: char = '/';

/// Returns true if `s` contains any host path-separator character.
///
/// Unix: `/` only. Windows: `/` or `\`.
#[must_use]
pub fn contains_separator(s: &str) -> bool {
    #[cfg(any(windows, target_os = "redox"))]
    {
        s.contains(PATH_SEPARATORS)
    }
    #[cfg(not(any(windows, target_os = "redox")))]
    {
        s.contains(PATH_SEPARATOR)
    }
}

/// Returns true if `s` ends with a host path-separator character.
#[must_use]
pub fn ends_with_separator(s: &str) -> bool {
    #[cfg(any(windows, target_os = "redox"))]
    {
        s.ends_with(PATH_SEPARATORS)
    }
    #[cfg(not(any(windows, target_os = "redox")))]
    {
        s.ends_with(PATH_SEPARATOR)
    }
}

/// Returns `s` with a single trailing host path-separator stripped, if any.
#[must_use]
pub fn strip_separator_suffix(s: &str) -> &str {
    #[cfg(any(windows, target_os = "redox"))]
    {
        s.strip_suffix(PATH_SEPARATORS).unwrap_or(s)
    }
    #[cfg(not(any(windows, target_os = "redox")))]
    {
        s.strip_suffix(PATH_SEPARATOR).unwrap_or(s)
    }
}

/// Byte index of the last host path-separator in `s`, or `None`.
#[must_use]
pub fn rfind_separator(s: &str) -> Option<usize> {
    #[cfg(any(windows, target_os = "redox"))]
    {
        s.rfind(PATH_SEPARATORS)
    }
    #[cfg(not(any(windows, target_os = "redox")))]
    {
        s.rfind(PATH_SEPARATOR)
    }
}

/// Splits `s` on host path-separator characters.
///
/// Used by glob expansion in `brush-core` to break a pattern into
/// per-directory pieces.
pub fn split_for_pattern(s: &str) -> impl Iterator<Item = &str> {
    #[cfg(any(windows, target_os = "redox"))]
    {
        s.split(PATH_SEPARATORS)
    }
    #[cfg(not(any(windows, target_os = "redox")))]
    {
        s.split(PATH_SEPARATOR)
    }
}

/// If `first_component` (typically the result of splitting a pattern's first
/// piece) indicates the host's notion of an absolute path root, returns the
/// matching root path.
///
/// Unix: an empty `first_component` means a leading `/`, rooted at `/`.
/// Windows: also recognizes a two-character drive prefix like `C:`,
/// returning `C:/`.
#[must_use]
pub fn pattern_root(first_component: &str) -> Option<PathBuf> {
    if first_component.is_empty() {
        return Some(PathBuf::from("/"));
    }

    #[cfg(windows)]
    {
        if first_component.len() == 2
            && first_component.as_bytes()[0].is_ascii_alphabetic()
            && first_component.as_bytes()[1] == b':'
        {
            let mut root = String::with_capacity(3);
            root.push_str(first_component);
            root.push('/');
            return Some(PathBuf::from(root));
        }
    }

    None
}

/// Pushes `component` onto `path` for pattern expansion, avoiding
/// `PathBuf::push`'s drive-letter and root-replacement quirks on Windows.
///
/// Unix: equivalent to `PathBuf::push`. Windows: always appends as a child,
/// using `/` as the inserted separator (mixed separators in the result are
/// fine because [`normalize_separators`] is applied before display).
pub fn push_for_pattern(path: &mut PathBuf, component: &str) {
    #[cfg(not(windows))]
    {
        path.push(component);
    }
    #[cfg(windows)]
    {
        let bytes = path.as_bytes();
        let needs_sep = !bytes.is_empty() && !matches!(bytes.last(), Some(b'/' | b'\\'));
        let buf = path.as_mut_string();
        if needs_sep {
            buf.push_str("/");
        }
        buf.push_str(component);
    }
}

/// Normalizes path separators for shell output.
///
/// Unix: pass-through. Windows: replaces `\` with `/`, since backslash is
/// the shell escape character.
#[must_use]
pub fn normalize_separators(s: &str) -> Cow<'_, str> {
    #[cfg(windows)]
    {
        if s.contains('\\') {
            return Cow::Owned(s.replace('\\', "/"));
        }
    }
    Cow::Borrowed(s)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vec::Vec;
    use alloc::vec;

    #[test]
    fn separators_basic() {
        assert!(contains_separator("foo/bar"));
        assert!(!contains_separator("foobar"));
        assert!(ends_with_separator("foo/"));
        assert!(!ends_with_separator("foo"));
        assert_eq!(strip_separator_suffix("foo/"), "foo");
        assert_eq!(strip_separator_suffix("foo"), "foo");
        assert_eq!(rfind_separator("a/b/c"), Some(3));
        assert_eq!(rfind_separator("abc"), None);
    }

    #[test]
    fn split_basic() {
        let parts: Vec<_> = split_for_pattern("a/b/c").collect();
        assert_eq!(parts, vec!["a", "b", "c"]);
    }

    #[test]
    fn pattern_root_leading_slash() {
        assert_eq!(pattern_root(""), Some(PathBuf::from("/")));
        assert_eq!(pattern_root("foo"), None);
    }
}
