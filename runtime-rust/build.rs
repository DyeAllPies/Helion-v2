// build.rs — compile proto/runtime.proto into Rust types using prost-build.
//
// The generated file is written to $OUT_DIR/helion.rs and included in
// src/proto.rs via the include! macro.

fn main() {
    prost_build::compile_protos(
        &["../proto/runtime.proto"],
        &["../proto"],
    )
    .expect("prost_build failed — ensure proto/runtime.proto exists");
}