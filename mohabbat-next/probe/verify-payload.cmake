execute_process(
    COMMAND "${LLVM_READOBJ}" --relocations "${PAYLOAD_ELF}"
    RESULT_VARIABLE readobj_result
    OUTPUT_VARIABLE relocations
    ERROR_VARIABLE readobj_error
)
if(NOT readobj_result EQUAL 0)
    message(FATAL_ERROR "llvm-readobj failed: ${readobj_error}")
endif()
if(relocations MATCHES "Relocations \\[[\r\n ]*0x")
    message(FATAL_ERROR "Payload contains relocations: ${PAYLOAD_ELF}")
endif()

execute_process(
    COMMAND "${LLVM_NM}" --undefined-only "${PAYLOAD_ELF}"
    RESULT_VARIABLE nm_result
    OUTPUT_VARIABLE undefined_symbols
    ERROR_VARIABLE nm_error
)
if(NOT nm_result EQUAL 0)
    message(FATAL_ERROR "llvm-nm failed: ${nm_error}")
endif()
if(NOT undefined_symbols STREQUAL "")
    message(FATAL_ERROR "Payload has undefined symbols: ${undefined_symbols}")
endif()

execute_process(
    COMMAND "${LLVM_OBJDUMP}" --disassemble-symbols=probe_entry "${PAYLOAD_ELF}"
    RESULT_VARIABLE objdump_result
    OUTPUT_VARIABLE instructions
    ERROR_VARIABLE objdump_error
)
if(NOT objdump_result EQUAL 0)
    message(FATAL_ERROR "llvm-objdump failed: ${objdump_error}")
endif()
if(instructions MATCHES "[\r\n][^\r\n]*(syscall|svc)[\r\n]")
    message(FATAL_ERROR "Payload contains a direct system-call instruction.")
endif()

file(SIZE "${PAYLOAD_BINARY}" payload_size)
if(payload_size LESS 1 OR payload_size GREATER 4096)
    message(FATAL_ERROR "Payload must fit within one 4 KiB page.")
endif()
