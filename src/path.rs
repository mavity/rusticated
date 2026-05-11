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

use std::borrow::Cow;
use std::path::PathBuf;

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
        let bytes = path.as_os_str().as_encoded_bytes();
        let needs_sep = !bytes.is_empty() && !matches!(bytes.last(), Some(b'/' | b'\\'));
        let buf = path.as_mut_os_string();
        if needs_sep {
            buf.push("/");
        }
        buf.push(component);
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
