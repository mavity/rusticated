cmake_minimum_required(VERSION 3.24)
project(mohabbat_next_probe LANGUAGES C)

if(NOT UNIX)
    message(FATAL_ERROR "Use hosts/windows/build.ps1 for the Windows host.")
endif()

find_package(Python3 REQUIRED COMPONENTS Interpreter)
find_program(CLANG_EXECUTABLE clang REQUIRED)
find_program(LLD_EXECUTABLE ld.lld REQUIRED)
find_program(LLVM_OBJCOPY_EXECUTABLE llvm-objcopy REQUIRED)
find_program(LLVM_READOBJ_EXECUTABLE llvm-readobj REQUIRED)
find_program(LLVM_NM_EXECUTABLE llvm-nm REQUIRED)
find_program(LLVM_OBJDUMP_EXECUTABLE llvm-objdump REQUIRED)

set(PAYLOAD_OUTPUT_DIR "${CMAKE_BINARY_DIR}/payload")
file(MAKE_DIRECTORY "${PAYLOAD_OUTPUT_DIR}")

function(add_payload_slice name triple)
    set(object "${PAYLOAD_OUTPUT_DIR}/probe-${name}.o")
    set(elf "${PAYLOAD_OUTPUT_DIR}/probe-${name}.elf")
    set(binary "${PAYLOAD_OUTPUT_DIR}/probe-${name}.bin")
    add_custom_command(
        OUTPUT "${binary}"
        COMMAND "${CLANG_EXECUTABLE}" "--target=${triple}" -O2
            -ffreestanding -fno-builtin -fno-stack-protector -fPIC
            -fno-unwind-tables -fno-asynchronous-unwind-tables
            -fno-exceptions -fno-rtti
            -c "${CMAKE_CURRENT_SOURCE_DIR}/payload/probe.c" -o "${object}"
        COMMAND "${LLD_EXECUTABLE}"
            -T "${CMAKE_CURRENT_SOURCE_DIR}/payload/payload.ld"
            --build-id=none --no-undefined -o "${elf}" "${object}"
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
            payload/abi.h
            payload/probe.c
            payload/payload.ld
            verify-payload.cmake
        VERBATIM
    )
    set(PAYLOAD_BINARIES ${PAYLOAD_BINARIES} "${binary}" PARENT_SCOPE)
endfunction()

add_payload_slice(x86_64 x86_64-unknown-none-elf)
add_payload_slice(aarch64 aarch64-unknown-none-elf)

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
