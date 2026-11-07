#include "abi.h"

static void probe_terminate(
    const struct probe_dispatch *dispatch,
    probe_status_t status)
{
    dispatch->exit(status);
    for (;;) {
    }
}

static int probe_write_all(
    const struct probe_dispatch *dispatch,
    const unsigned char *buffer,
    probe_size_t length)
{
    probe_result_t written = dispatch->write(buffer, length);
    return written == (probe_result_t)length;
}

void PROBE_CALL probe_entry(const struct probe_dispatch *dispatch)
{
    static const unsigned char greeting[] =
        "Anonymous-memory probe ready. Enter an empty line to exit.\n> ";
    static const unsigned char echo_prefix[] = "echo: ";
    static const unsigned char newline[] = "\n";
    static const unsigned char prompt[] = "> ";
    unsigned char line[256];

    if (!probe_write_all(dispatch, greeting, sizeof(greeting) - 1)) {
        probe_terminate(dispatch, 3);
    }

    for (;;) {
        probe_size_t length = 0;

        for (;;) {
            unsigned char byte = 0;
            probe_result_t count = dispatch->read(&byte, 1);

            if (count < 0) {
                probe_terminate(dispatch, 3);
            }
            if (count == 0) {
                probe_terminate(dispatch, 0);
            }
            if (byte == '\n') {
                break;
            }
            if (length == sizeof(line)) {
                probe_terminate(dispatch, 2);
            }
            line[length++] = byte;
        }

        if (length > 0 && line[length - 1] == '\r') {
            length--;
        }
        if (length == 0) {
            probe_terminate(dispatch, 0);
        }
        if (!probe_write_all(dispatch, echo_prefix, sizeof(echo_prefix) - 1) ||
            !probe_write_all(dispatch, line, length) ||
            !probe_write_all(dispatch, newline, sizeof(newline) - 1) ||
            !probe_write_all(dispatch, prompt, sizeof(prompt) - 1)) {
            probe_terminate(dispatch, 3);
        }
    }
}
