foreach(required_variable IN ITEMS
    PAYLOAD_TARGET_DIR
    PAYLOAD_TRIPLE
    PAYLOAD_LINKER
    PAYLOAD_LINKER_SCRIPT
    PAYLOAD_ELF
)
    if(NOT DEFINED ${required_variable})
        message(FATAL_ERROR "Missing ${required_variable}.")
    endif()
endforeach()

set(dependency_dir "${PAYLOAD_TARGET_DIR}/${PAYLOAD_TRIPLE}/release/deps")

function(find_one_archive pattern result)
    file(GLOB matches "${dependency_dir}/${pattern}")
    list(LENGTH matches count)
    if(NOT count EQUAL 1)
        message(FATAL_ERROR
            "Expected one Rust archive matching ${pattern}; found ${count}.")
    endif()
    list(GET matches 0 archive)
    set(${result} "${archive}" PARENT_SCOPE)
endfunction()

find_one_archive("libmohabbat_next_payload-*.rlib" payload_archive)
find_one_archive("libcore-*.rlib" core_archive)
find_one_archive("libcompiler_builtins-*.rlib" compiler_builtins_archive)

execute_process(
    COMMAND "${PAYLOAD_LINKER}"
        -T "${PAYLOAD_LINKER_SCRIPT}"
        --build-id=none
        --gc-sections
        --no-undefined
        --start-group
        "${payload_archive}"
        "${core_archive}"
        "${compiler_builtins_archive}"
        --end-group
        -o "${PAYLOAD_ELF}"
    RESULT_VARIABLE link_result
    ERROR_VARIABLE link_error
)
if(NOT link_result EQUAL 0)
    message(FATAL_ERROR "Rust payload linking failed: ${link_error}")
endif()
