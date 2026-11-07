cmake_minimum_required(VERSION 3.24)
project(mohabbat_next_probe LANGUAGES C)

if(NOT UNIX)
    message(FATAL_ERROR "Use hosts/windows/build.ps1 for the Windows host.")
endif()

find_package(Python3 REQUIRED COMPONENTS Interpreter)
find_program(CARGO_EXECUTABLE cargo REQUIRED)
find_program(LLD_EXECUTABLE ld.lld REQUIRED)
find_program(LLVM_OBJCOPY_EXECUTABLE llvm-objcopy REQUIRED)
find_program(LLVM_READOBJ_EXECUTABLE llvm-readobj REQUIRED)
find_program(LLVM_NM_EXECUTABLE llvm-nm REQUIRED)
find_program(LLVM_OBJDUMP_EXECUTABLE llvm-objdump REQUIRED)

set(PAYLOAD_OUTPUT_DIR "${CMAKE_BINARY_DIR}/payload")
file(MAKE_DIRECTORY "${PAYLOAD_OUTPUT_DIR}")

function(add_payload_slice name triple)
    set(elf "${PAYLOAD_OUTPUT_DIR}/probe-${name}.elf")
    set(binary "${PAYLOAD_OUTPUT_DIR}/probe-${name}.bin")
    set(rust_target_dir "${CMAKE_BINARY_DIR}/rust-${name}")
    set(cargo_home "${CMAKE_BINARY_DIR}/cargo-home")
    add_custom_command(
        OUTPUT "${binary}"
        COMMAND "${CMAKE_COMMAND}" -E env "CARGO_HOME=${cargo_home}"
            "${CARGO_EXECUTABLE}" clean
            --manifest-path "${CMAKE_CURRENT_SOURCE_DIR}/Cargo.toml"
            --target "${triple}" --target-dir "${rust_target_dir}"
        COMMAND "${CMAKE_COMMAND}" -E env "CARGO_HOME=${cargo_home}"
            "${CARGO_EXECUTABLE}" -Z build-std=core build
            --manifest-path "${CMAKE_CURRENT_SOURCE_DIR}/Cargo.toml"
            --package mohabbat-next-payload --release
            --target "${triple}" --target-dir "${rust_target_dir}"
        COMMAND "${CMAKE_COMMAND}"
            "-DPAYLOAD_TARGET_DIR=${rust_target_dir}"
            "-DPAYLOAD_TRIPLE=${triple}"
            "-DPAYLOAD_LINKER=${LLD_EXECUTABLE}"
            "-DPAYLOAD_LINKER_SCRIPT=${CMAKE_CURRENT_SOURCE_DIR}/payload/payload.ld"
            "-DPAYLOAD_ELF=${elf}"
            -P "${CMAKE_CURRENT_SOURCE_DIR}/payload/link-rust-payload.cmake"
        COMMAND "${LLVM_OBJCOPY_EXECUTABLE}" -O binary
            --only-section=.payload "${elf}" "${binary}"
        COMMAND "${CMAKE_COMMAND}"
            "-DLLVM_READOBJ=${LLVM_READOBJ_EXECUTABLE}"
            "-DLLVM_NM=${LLVM_NM_EXECUTABLE}"
            "-DLLVM_OBJDUMP=${LLVM_OBJDUMP_EXECUTABLE}"
            "-DPAYLOAD_ELF=${elf}"
            "-DPAYLOAD_BINARY=${binary}"
            -P "${CMAKE_CURRENT_SOURCE_DIR}/verify-payload.cmake"
        DEPENDS
            Cargo.toml
            Cargo.lock
            .cargo/config.toml
            payload/Cargo.toml
            payload/probe.rs
            payload/abi.h
            payload/payload.ld
            payload/link-rust-payload.cmake
            verify-payload.cmake
        VERBATIM
        WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}"
    )
    set(PAYLOAD_BINARIES ${PAYLOAD_BINARIES} "${binary}" PARENT_SCOPE)
endfunction()

add_payload_slice(x86_64 x86_64-unknown-none)
add_payload_slice(aarch64 aarch64-unknown-none)

set(PAYLOAD_HEADER "${CMAKE_BINARY_DIR}/payloads.h")
add_custom_command(
    OUTPUT "${PAYLOAD_HEADER}"
    COMMAND "${Python3_EXECUTABLE}"
        "${CMAKE_CURRENT_SOURCE_DIR}/payload/generate-header.py"
        "${PAYLOAD_OUTPUT_DIR}" "${PAYLOAD_HEADER}"
    DEPENDS ${PAYLOAD_BINARIES} payload/generate-header.py
    VERBATIM
)
add_custom_target(probe_payloads DEPENDS "${PAYLOAD_HEADER}")

add_executable(probe hosts/posix/host.c "${PAYLOAD_HEADER}")
add_dependencies(probe probe_payloads)
target_include_directories(probe PRIVATE
    "${CMAKE_CURRENT_SOURCE_DIR}/payload"
    "${CMAKE_BINARY_DIR}"
)
target_compile_features(probe PRIVATE c_std_11)
target_compile_options(probe PRIVATE -Wall -Wextra -Werror)
