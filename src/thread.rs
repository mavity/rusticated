//! Thread-local storage key type.

/// A key for accessing thread-local storage, analogous to [`std::thread::LocalKey`].
///
/// Instances are produced exclusively by the [`thread_local!`] macro and must be
/// stored in `static` items.
pub struct LocalKey<T: 'static> {
    inner: fn() -> *const T,
}

impl<T: 'static> LocalKey<T> {
    /// Constructs a new `LocalKey` from a getter function.
    ///
    /// This is an implementation detail used by the [`thread_local!`] macro; do
    /// not call it directly.
    #[doc(hidden)]
    pub const fn new(inner: fn() -> *const T) -> Self {
        Self { inner }
    }

    /// Acquires a reference to the thread-local value, initialising it on first
    /// access, then passes it to `f`.
    pub fn with<R>(&'static self, f: impl FnOnce(&T) -> R) -> R {
        // SAFETY: `inner` was supplied by the `thread_local!` macro and returns a
        // pointer into a `#[thread_local]` static.  The pointer is valid for the
        // entire lifetime of the calling thread, and `f` cannot outlive this call.
        f(unsafe { &*(self.inner)() })
    }
}
