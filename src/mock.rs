//! In-memory [`Platform`] implementation used by unit tests.
//!
//! `MockPlatform` is a deliberately minimal stand-in: it does not implement a
//! virtual filesystem yet. Every fallible method returns
//! [`PlatformError::Unsupported`]. As `brush-core` callers migrate onto the
//! [`Platform`] trait, the mock grows the specific behaviors those callers
//! need, driven by failing tests.

use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};

use async_trait::async_trait;

use crate::error::{PlatformError, PlatformResult};
use crate::platform::Platform;
use crate::types::{Capability, EnvVar, Metadata, OpenOptions, ProcessSpec, UserInfo};

/// In-memory, side-effect-free [`Platform`] for tests.
#[derive(Debug, Default, Clone)]
pub struct MockPlatform {
    /// Fixed wall-clock value returned from [`Platform::now`].
    now: Option<SystemTime>,
}

impl MockPlatform {
    /// Creates a new mock with default state.
    pub fn new() -> Self {
        Self::default()
    }

    /// Pins the value returned by [`Platform::now`]. Useful for snapshot tests.
    #[must_use]
    pub const fn with_now(mut self, when: SystemTime) -> Self {
        self.now = Some(when);
        self
    }
}

#[async_trait(?Send)]
impl Platform for MockPlatform {
    fn supports(&self, _capability: Capability) -> bool {
        false
    }

    async fn metadata(&self, _path: &Path) -> PlatformResult<Metadata> {
        Err(PlatformError::Unsupported("metadata"))
    }

    async fn symlink_metadata(&self, _path: &Path) -> PlatformResult<Metadata> {
        Err(PlatformError::Unsupported("symlink_metadata"))
    }

    async fn read_file(&self, _path: &Path) -> PlatformResult<Vec<u8>> {
        Err(PlatformError::Unsupported("read_file"))
    }

    async fn write_file(&self, _path: &Path, _contents: &[u8]) -> PlatformResult<()> {
        Err(PlatformError::Unsupported("write_file"))
    }

    async fn create_dir_all(&self, _path: &Path) -> PlatformResult<()> {
        Err(PlatformError::Unsupported("create_dir_all"))
    }

    async fn remove_file(&self, _path: &Path) -> PlatformResult<()> {
        Err(PlatformError::Unsupported("remove_file"))
    }

    async fn rename(&self, _from: &Path, _to: &Path) -> PlatformResult<()> {
        Err(PlatformError::Unsupported("rename"))
    }

    async fn read_link(&self, _path: &Path) -> PlatformResult<PathBuf> {
        Err(PlatformError::Unsupported("read_link"))
    }

    async fn canonicalize(&self, _path: &Path) -> PlatformResult<PathBuf> {
        Err(PlatformError::Unsupported("canonicalize"))
    }

    async fn check_open(&self, _path: &Path, _opts: &OpenOptions) -> PlatformResult<()> {
        Err(PlatformError::Unsupported("check_open"))
    }

    async fn current_dir(&self) -> PlatformResult<PathBuf> {
        // Convenience for tests: most brush-core unit tests don't pin a cwd
        // but expect shell construction to succeed. Delegating to the host
        // process keeps that behavior intact while still letting tests
        // override by constructing a custom mock or supplying a real
        // platform.
        std::env::current_dir().map_err(PlatformError::PlainIo)
    }

    async fn set_current_dir(&self, _path: &Path) -> PlatformResult<()> {
        Err(PlatformError::Unsupported("set_current_dir"))
    }

    fn host_env_snapshot(&self) -> Vec<EnvVar> {
        Vec::new()
    }

    fn host_env_var(&self, _name: &str) -> Option<OsString> {
        None
    }

    async fn current_user(&self) -> PlatformResult<UserInfo> {
        Err(PlatformError::Unsupported("current_user"))
    }

    async fn lookup_user(&self, _name: &str) -> PlatformResult<Option<UserInfo>> {
        Err(PlatformError::Unsupported("lookup_user"))
    }

    async fn hostname(&self) -> PlatformResult<OsString> {
        Err(PlatformError::Unsupported("hostname"))
    }

    fn now(&self) -> SystemTime {
        self.now.unwrap_or(SystemTime::UNIX_EPOCH)
    }

    async fn sleep(&self, _dur: Duration) {
        // No-op: tests should not depend on real time passing.
    }

    async fn check_spawn(&self, _spec: &ProcessSpec) -> PlatformResult<()> {
        Err(PlatformError::Unsupported("spawn"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn supports_returns_false_for_all_known_capabilities() {
        let p = MockPlatform::new();
        for cap in [
            Capability::ProcessSpawn,
            Capability::JobControl,
            Capability::Signals,
            Capability::Terminal,
            Capability::Symlinks,
            Capability::PipeSizing,
        ] {
            assert!(!p.supports(cap));
        }
    }

    #[test]
    fn now_can_be_pinned() {
        let when = SystemTime::UNIX_EPOCH + Duration::from_secs(42);
        let p = MockPlatform::new().with_now(when);
        assert_eq!(p.now(), when);
    }

    #[test]
    fn host_env_is_empty_by_default() {
        let p = MockPlatform::new();
        assert!(p.host_env_snapshot().is_empty());
        assert!(p.host_env_var("PATH").is_none());
    }
}
