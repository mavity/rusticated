use crate::net::ToSocketAddrs;
use crate::vec::Vec;
use crate::{abi, fs, io, net, process, signal, tty};

#[test]
fn arguments_and_environment_use_the_host_wrappers() {
    let args: Vec<_> = crate::env::args().collect();
    assert!(
        args.first().is_some_and(|program| !program.is_empty()),
        "host argument vector must contain a program name"
    );

    let vars: Vec<_> = crate::env::vars_os().collect();
    assert!(!vars.is_empty(), "host environment must not be empty");
    for (key, value) in vars {
        assert_eq!(
            crate::env::var(&key).expect("environment key must be retrievable"),
            value,
            "environment wrapper changed the value for {key:?}"
        );
    }
}

#[test]
fn wall_clock_wrapper_returns_unix_nanoseconds() {
    const JAN_2020_NS: u64 = 1_577_836_800_000_000_000;

    assert!(
        crate::time::now_ns() >= JAN_2020_NS,
        "host clock should return nanoseconds since the Unix epoch"
    );
}

#[test]
fn platform_info_wrapper_reads_base_fields_and_versioned_context() {
    let info = crate::system_info::get_platform_info()
        .expect("host platform information should be available");

    assert_ne!(info.os_kind, 0, "host should report an operating system");
    assert_ne!(info.path_sep, 0, "host should report a path separator");
    assert_ne!(info.list_sep, 0, "host should report a path-list separator");

    if info.system_context_version == 1 {
        for (name, field) in [
            ("locale", info.locale.as_slice()),
            ("time zone", info.time_zone.as_slice()),
            ("keyboard layout", info.keyboard_layout_id.as_slice()),
            ("appearance", info.appearance.as_slice()),
            ("power source", info.power_source.as_slice()),
        ] {
            assert!(
                field.contains(&0),
                "system-context {name} field must be NUL-terminated"
            );
        }
    } else {
        assert_eq!(
            info.system_context_version, 0,
            "host reported an unknown system-context ABI version"
        );
    }
}

fn metadata(kind: u32, mode: u32, size: u64) -> fs::Metadata {
    fs::Metadata {
        stat: abi::AbiStat {
            kind,
            mode,
            size,
            ..abi::AbiStat::default()
        },
    }
}

fn directory_entry(name: &str, kind: u32) -> fs::DirEntry {
    fs::DirEntry {
        name: crate::string::String::from(name),
        metadata: Some(metadata(kind, 0, 0)),
    }
}

#[test]
fn metadata_decodes_file_kind_size_mode_and_permission_bits() {
    let file = metadata(abi::STAT_KIND_FILE, 0o100640, 37);

    assert!(file.is_file());
    assert!(!file.is_dir());
    assert!(!file.is_symlink());
    assert_eq!(file.len(), 37);
    assert_eq!(file.mode(), 0o100640);
    assert_eq!(file.permissions().mode(), 0o100640);
}

#[test]
fn metadata_distinguishes_directories_symlinks_and_unknown_kinds() {
    let dir = metadata(abi::STAT_KIND_DIR, 0o040755, 0);
    let symlink = metadata(abi::STAT_KIND_SYMLINK, 0o120777, 8);
    let unknown = metadata(abi::STAT_KIND_UNKNOWN, 0, 0);

    assert!(dir.is_dir());
    assert!(!dir.is_file());
    assert!(symlink.is_symlink());
    assert!(!symlink.is_file());
    assert!(!unknown.is_dir());
    assert!(!unknown.is_file());
    assert!(!unknown.is_symlink());
}

#[test]
fn metadata_preserves_optional_stat_fields_and_validity_mask() {
    let stat = abi::AbiStat {
        uid: 12,
        gid: 34,
        nlink: 2,
        inode: 56,
        dev: 78,
        rdev: 90,
        blksize: 4096,
        blocks: 3,
        valid_mask: abi::STAT_VALID_UID | abi::STAT_VALID_GID | abi::STAT_VALID_INODE,
        modified_ns: 123,
        accessed_ns: 456,
        changed_ns: 789,
        created_ns: 987,
        ..abi::AbiStat::default()
    };
    let file = fs::Metadata { stat };

    assert_eq!(file.uid(), 12);
    assert_eq!(file.gid(), 34);
    assert_eq!(file.nlink(), 2);
    assert_eq!(file.inode(), 56);
    assert_eq!(file.dev(), 78);
    assert_eq!(file.rdev(), 90);
    assert_eq!(file.blksize(), 4096);
    assert_eq!(file.blocks(), 3);
    assert_eq!(
        file.valid_mask(),
        abi::STAT_VALID_UID | abi::STAT_VALID_GID | abi::STAT_VALID_INODE
    );
    assert_eq!(file.modified_ns(), 123);
    assert_eq!(file.accessed_ns(), 456);
    assert_eq!(file.changed_ns(), 789);
    assert_eq!(file.created_ns(), 987);
}

#[test]
fn directory_entry_exposes_name_path_and_metadata() {
    let entry = directory_entry("data.bin", abi::STAT_KIND_FILE);

    assert_eq!(entry.file_name(), "data.bin");
    assert_eq!(entry.path().as_str(), "data.bin");
    assert!(entry.metadata().expect("entry metadata").is_file());
    assert!(entry.file_type().expect("entry file type").is_file());
}

#[test]
fn directory_entry_file_type_preserves_directory_and_link_kinds() {
    let dir_type = directory_entry("subdir", abi::STAT_KIND_DIR)
        .file_type()
        .expect("directory file type");
    let link_type = directory_entry("alias", abi::STAT_KIND_SYMLINK)
        .file_type()
        .expect("symlink file type");

    assert!(dir_type.is_dir());
    assert!(!dir_type.is_file());
    assert!(!dir_type.is_symlink());
    assert!(link_type.is_symlink());
    assert!(!link_type.is_dir());
    assert!(!link_type.is_file());
}

#[test]
fn directory_entry_reports_missing_metadata_as_an_io_error() {
    let entry = fs::DirEntry {
        name: crate::string::String::from("unknown"),
        metadata: None,
    };

    let error = entry
        .metadata()
        .err()
        .expect("missing stat payload must fail");
    assert_eq!(error.kind(), io::ErrorKind::Other);
    assert_eq!(error.raw_os_error(), None);
    assert!(entry.file_type().is_err());
}

#[test]
fn directory_iterator_yields_each_entry_once_then_ends() {
    let mut entries = Vec::new();
    entries.push(directory_entry("first", abi::STAT_KIND_FILE));
    entries.push(directory_entry("second", abi::STAT_KIND_DIR));
    let mut read_dir = fs::ReadDir { entries, pos: 0 };

    assert_eq!(read_dir.next().unwrap().unwrap().file_name(), "first");
    assert_eq!(read_dir.next().unwrap().unwrap().file_name(), "second");
    assert!(read_dir.next().is_none());
    assert_eq!(read_dir.pos, 2);
}

#[test]
fn permissions_toggle_read_only_and_restore_owner_write_access() {
    let mut permissions = fs::Permissions { mode: 0o100644 };

    assert!(!permissions.readonly());
    permissions.set_readonly(true);
    assert!(permissions.readonly());
    assert_eq!(permissions.mode(), 0o100444);
    permissions.set_readonly(false);
    assert!(!permissions.readonly());
    assert_eq!(permissions.mode(), 0o100644);
}

#[test]
fn permissions_mode_replacement_updates_read_only_state() {
    let mut permissions = fs::Permissions { mode: 0 };

    permissions.set_mode(0o100555);
    assert_eq!(permissions.mode(), 0o100555);
    assert!(permissions.readonly());
    permissions.set_readonly(false);
    assert_eq!(permissions.mode(), 0o100755);
    assert!(!permissions.readonly());
}

#[test]
fn current_directory_wrapper_returns_a_nonempty_host_path() {
    let cwd = crate::env::current_dir().expect("host current directory");

    assert!(!cwd.as_str().is_empty());
}

#[test]
fn setting_the_current_directory_to_itself_preserves_the_path() {
    let cwd = crate::env::current_dir().expect("host current directory");
    crate::env::set_current_dir(&cwd).expect("setting the existing directory");

    let after = crate::env::current_dir().expect("current directory after set");
    assert_eq!(after.as_str(), cwd.as_str());
}

#[test]
fn setting_a_nul_containing_current_directory_is_rejected() {
    let error = crate::env::set_current_dir("bad\0path")
        .expect_err("host must reject a path containing NUL");

    assert!(error.raw_os_error().is_some());
}

#[test]
fn tty_handles_report_the_wasm_standard_stream_contract() {
    use crate::io::IsTerminal;

    assert!(tty::stdin().is_terminal());
    assert!(tty::stdout().is_terminal());
    assert!(!tty::stderr().is_terminal());
    assert!(io::stdin().is_terminal());
    assert!(io::stdout().is_terminal());
    assert!(!io::stderr().is_terminal());
}

#[test]
fn tty_mode_changes_and_cursor_query_use_the_host_wrappers() {
    tty::set_mode(0, 0).expect("restore stdin mode");
    tty::set_mode(1, 0).expect("restore stdout mode");
    tty::set_mode(2, 0).expect("restore stderr mode");
    tty::enable_raw_mode().expect("enable raw mode");
    tty::disable_raw_mode().expect("disable raw mode");

    assert_eq!(tty::cursor_position().expect("cursor position"), (0, 0));
}

#[test]
fn ipv4_and_ipv6_address_structs_preserve_components() {
    let v4 = net::Ipv4Addr::new(192, 0, 2, 7);
    let v6 = net::Ipv6Addr::new(0x2001, 0xdb8, 0, 1, 2, 3, 4, 5);

    assert_eq!(v4.octets(), [192, 0, 2, 7]);
    assert_eq!(v6.segments(), [0x2001, 0xdb8, 0, 1, 2, 3, 4, 5]);
}

#[test]
fn socket_address_constructors_preserve_ip_and_port() {
    let v4 = net::SocketAddrV4::new(net::Ipv4Addr::new(127, 0, 0, 1), 0);
    let v6 = net::SocketAddrV6::new(net::Ipv6Addr::new(0, 0, 0, 0, 0, 0, 0, 1), u16::MAX);

    assert_eq!(v4.ip().octets(), [127, 0, 0, 1]);
    assert_eq!(v4.port(), 0);
    assert_eq!(v6.ip().segments()[7], 1);
    assert_eq!(v6.port(), u16::MAX);
    assert_eq!(net::SocketAddr::V4(v4).port(), 0);
    assert_eq!(net::SocketAddr::V6(v6).port(), u16::MAX);
}

#[test]
fn socket_address_conversion_supports_ipv4_ipv6_and_ip_enum_inputs() {
    let v4 = net::Ipv4Addr::new(198, 51, 100, 9);
    let v6 = net::Ipv6Addr::new(0, 0, 0, 0, 0, 0, 0, 2);
    let from_v4 = (v4, 80).to_socket_addrs().unwrap().next().unwrap();
    let from_v6 = (v6, 443).to_socket_addrs().unwrap().next().unwrap();
    let from_ip = (net::IpAddr::V4(v4), 8080)
        .to_socket_addrs()
        .unwrap()
        .next()
        .unwrap();

    assert_eq!(from_v4.port(), 80);
    assert_eq!(from_v6.port(), 443);
    assert_eq!(from_ip.port(), 8080);
}

#[test]
fn textual_socket_address_parser_accepts_ipv4_and_rejects_invalid_forms() {
    let parsed = ("203.0.113.4", 1234)
        .to_socket_addrs()
        .unwrap()
        .next()
        .unwrap();
    assert_eq!(parsed.port(), 1234);

    for bad in ["localhost", "127.0.0", "127.0.0.256", "1.2.3.4.5", ""] {
        assert!(
            (bad, 80).to_socket_addrs().is_err(),
            "parser unexpectedly accepted {bad:?}"
        );
    }
}

#[test]
fn wasm_process_identity_and_signal_delivery_failure_are_explicit() {
    assert_eq!(process::id(), 0);
    assert!(
        signal::kill_process(0, signal::Signal::SIGTERM as u32).is_err(),
        "WASM guest signal delivery is currently unsupported"
    );
}

#[test]
fn signal_name_and_numeric_conversions_cover_supported_values() {
    for (number, name) in [
        (1, "SIGHUP"),
        (2, "SIGINT"),
        (9, "SIGKILL"),
        (10, "SIGUSR1"),
        (15, "SIGTERM"),
        (17, "SIGCHLD"),
        (28, "SIGWINCH"),
        (31, "SIGSYS"),
    ] {
        let signal = signal::Signal::try_from(number).expect("supported signal number");
        assert_eq!(signal.as_str(), name);
        assert_eq!(
            name.parse::<signal::Signal>().expect("supported name"),
            signal
        );
    }

    assert_eq!(signal::Signal::iterator().count(), 30);
    assert!(signal::Signal::try_from(0).is_err());
    assert!(signal::Signal::try_from(32).is_err());
    assert!("SIGUNKNOWN".parse::<signal::Signal>().is_err());
}

#[test]
fn overlapped_abi_completion_flag_tracks_only_the_completed_bit() {
    let mut overlapped = abi::Overlapped::default();

    assert!(!overlapped.is_complete());
    overlapped.flags = 1 << 31;
    assert!(!overlapped.is_complete());
    overlapped.flags |= abi::Overlapped::FLAG_COMPLETED;
    assert!(overlapped.is_complete());
}

#[test]
fn overlapped_and_stat_abi_layouts_match_the_wire_contract() {
    assert_eq!(core::mem::size_of::<abi::Overlapped>(), 24);
    assert_eq!(core::mem::size_of::<abi::AbiStat>(), 112);

    let stat = abi::AbiStat::default();
    assert_eq!(stat.kind, abi::STAT_KIND_UNKNOWN);
    assert_eq!(stat.valid_mask, 0);
}

#[test]
fn platform_abi_default_has_zeroed_extension_and_memory_slots() {
    let info = abi::AbiPlatformInfo::default();

    assert_eq!(core::mem::size_of::<abi::AbiPlatformInfo>(), 1264);
    assert_eq!(info.system_context_version, 0);
    assert!(info.memory_slots.iter().all(|slot| slot.bytes == 0));
    assert!(info.locale.iter().all(|byte| *byte == 0));
}

#[test]
fn io_errors_preserve_raw_codes_and_synthetic_kinds() {
    let os_error = io::Error::from_raw_os_error(5);
    assert_eq!(os_error.raw_os_error(), Some(5));
    assert_eq!(os_error.kind(), io::ErrorKind::Other);
    assert_eq!(crate::alloc::format!("{os_error}"), "os error 5");

    let invalid = io::Error::new(io::ErrorKind::InvalidInput, "bad input");
    assert_eq!(invalid.raw_os_error(), None);
    assert_eq!(invalid.kind(), io::ErrorKind::InvalidInput);
    assert_eq!(crate::alloc::format!("{invalid}"), "bad input");

    let other = io::Error::other("host failure");
    assert_eq!(other.raw_os_error(), None);
    assert_eq!(other.kind(), io::ErrorKind::Other);
    assert_eq!(crate::alloc::format!("{other}"), "host failure");
}
