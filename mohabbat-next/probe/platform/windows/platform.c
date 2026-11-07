#include "abi.h"
#include "../../payload/abi.h"

typedef void *win_handle;
typedef platform_u32 win_bool;

typedef win_handle (PLATFORM_CALL *get_std_handle_fn)(platform_u32 kind);
typedef win_bool (PLATFORM_CALL *read_file_fn)(
    win_handle handle,
    void *buffer,
    platform_u32 count,
    platform_u32 *read_count,
    void *overlapped);
typedef win_bool (PLATFORM_CALL *write_file_fn)(
    win_handle handle,
    const void *buffer,
    platform_u32 count,
    platform_u32 *written_count,
    void *overlapped);
typedef win_handle (PLATFORM_CALL *get_current_process_fn)(void);
typedef platform_u32 (PLATFORM_CALL *get_last_error_fn)(void);
typedef win_bool (PLATFORM_CALL *flush_instruction_cache_fn)(
    win_handle process,
    const void *address,
    platform_size_t size);
typedef void (PLATFORM_CALL *exit_process_fn)(platform_u32 status);

struct platform_state {
    platform_u32 marker;
    struct windows_platform_host host;
    win_handle input_handle;
    win_handle output_handle;
    win_handle error_handle;
    win_handle process_handle;
    get_std_handle_fn get_std_handle;
    read_file_fn read_file;
    write_file_fn write_file;
    get_last_error_fn get_last_error;
    flush_instruction_cache_fn flush_instruction_cache;
    exit_process_fn exit_process;
    void *payload_code;
    platform_u32 previous_was_cr;
};

__attribute__((section(".data.platform_state")))
static struct platform_state state = { 0x4d4f4842 };

static void *resolve(const char *name)
{
    return state.host.get_proc_address(state.process_handle, name);
}

static platform_result_t PLATFORM_CALL write_stdout(
    const unsigned char *buffer,
    platform_size_t length)
{
    platform_u32 written = 0;

    if (length > 0xffffffffu ||
        !state.write_file(
            state.output_handle,
            buffer,
            (platform_u32)length,
            &written,
            0)) {
        return -1;
    }
    return (platform_result_t)written;
}

static platform_result_t PLATFORM_CALL read_stdin(
    unsigned char *buffer,
    platform_size_t capacity)
{
    if (capacity == 0) {
        return -1;
    }

    for (;;) {
        unsigned char byte = 0;
        platform_u32 count = 0;

        if (!state.read_file(
                state.input_handle, &byte, 1, &count, 0)) {
            if (state.get_last_error() == 109) {
                return 0;
            }
            static const unsigned char prefix[] =
                "ReadFile(stdin) failed with Win32 error ";
            unsigned char digits[10];
            platform_u32 value = state.get_last_error();
            platform_u32 digit_count = 0;
            platform_u32 ignored = 0;
            state.write_file(
                state.error_handle,
                prefix,
                sizeof(prefix) - 1,
                &ignored,
                0);
            do {
                digits[digit_count++] =
                    (unsigned char)('0' + value % 10);
                value /= 10;
            } while (value != 0 && digit_count < sizeof(digits));
            for (platform_u32 digit = 0; digit < digit_count / 2; digit++) {
                unsigned char swap = digits[digit];
                digits[digit] = digits[digit_count - digit - 1];
                digits[digit_count - digit - 1] = swap;
            }
            state.write_file(
                state.error_handle,
                digits,
                digit_count,
                &ignored,
                0);
            state.write_file(
                state.error_handle,
                "\r\n",
                2,
                &ignored,
                0);
            return -1;
        }
        if (count == 0) {
            return 0;
        }
        if (state.previous_was_cr) {
            state.previous_was_cr = 0;
            if (byte == '\n') {
                continue;
            }
        }
        if (byte == '\r') {
            state.previous_was_cr = 1;
            buffer[0] = '\n';
        } else {
            buffer[0] = byte;
        }
        return 1;
    }
}

static void PLATFORM_CALL exit_process(platform_status_t status)
{
    if (state.payload_code != 0) {
        state.host.free(state.payload_code);
        state.payload_code = 0;
    }
    state.exit_process((platform_u32)status);
}

int PLATFORM_CALL windows_platform_entry(
    const struct windows_platform_host *host)
{
    struct probe_dispatch dispatch;
    typedef void (PROBE_CALL *probe_entry_fn)(
        const struct probe_dispatch *);

    void *kernel32;
    get_current_process_fn get_current_process;
    get_last_error_fn get_last_error;
    probe_entry_fn entry;
    platform_size_t index;

    if (host == 0 || host->load_library == 0 ||
        host->get_proc_address == 0 || host->alloc == 0 ||
        host->free == 0 || host->hello_payload == 0 ||
        host->hello_payload_size == 0) {
        return 10;
    }

    state.host = *host;
    kernel32 = host->load_library("kernel32.dll");
    if (kernel32 == 0) {
        return 11;
    }
    state.process_handle = kernel32;

    state.get_std_handle = (get_std_handle_fn)resolve("GetStdHandle");
    state.read_file = (read_file_fn)resolve("ReadFile");
    state.write_file = (write_file_fn)resolve("WriteFile");
    get_current_process =
        (get_current_process_fn)resolve("GetCurrentProcess");
    get_last_error = (get_last_error_fn)resolve("GetLastError");
    state.flush_instruction_cache =
        (flush_instruction_cache_fn)resolve("FlushInstructionCache");
    state.exit_process = (exit_process_fn)resolve("ExitProcess");
    if (state.get_std_handle == 0 || state.read_file == 0 ||
        state.write_file == 0 || get_current_process == 0 ||
        get_last_error == 0 ||
        state.flush_instruction_cache == 0 || state.exit_process == 0) {
        return 12;
    }

    state.input_handle = state.get_std_handle((platform_u32)-10);
    state.output_handle = state.get_std_handle((platform_u32)-11);
    state.error_handle = state.get_std_handle((platform_u32)-12);
    state.get_last_error = get_last_error;
    if (state.input_handle == 0 || state.output_handle == 0 ||
        state.error_handle == 0) {
        return 13;
    }
    state.payload_code = host->alloc(host->hello_payload_size);
    if (state.payload_code == 0) {
        return 14;
    }
    for (index = 0; index < host->hello_payload_size; index++) {
        ((unsigned char *)state.payload_code)[index] =
            host->hello_payload[index];
    }

    state.process_handle = get_current_process();
    if (!state.flush_instruction_cache(
            state.process_handle,
            state.payload_code,
            host->hello_payload_size)) {
        host->free(state.payload_code);
        state.payload_code = 0;
        return 15;
    }

    dispatch.write = write_stdout;
    dispatch.read = read_stdin;
    dispatch.exit = exit_process;
    entry = (probe_entry_fn)state.payload_code;
    entry(&dispatch);

    host->free(state.payload_code);
    state.payload_code = 0;
    return 70;
}
