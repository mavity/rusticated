//! Thin re-export shim: `rusticated` presents as `std` by re-exporting the
//! entire `sysroot` implementation crate.
#![no_std]
#![allow(unused_features)]
#![feature(lang_items)]
#![allow(internal_features)]
#[doc(inline)]
pub use sysroot::*;
