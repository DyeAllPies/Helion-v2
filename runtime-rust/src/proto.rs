// src/proto.rs — include prost-generated types from OUT_DIR.
//
// Compiled from proto/runtime.proto by build.rs.
// Package name "helion" maps to helion.rs in OUT_DIR.
#![allow(clippy::all)]

include!(concat!(env!("OUT_DIR"), "/helion.rs"));