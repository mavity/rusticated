#ifndef MOHABBAT_NEXT_PROBE_ABI_H
#define MOHABBAT_NEXT_PROBE_ABI_H

typedef __SIZE_TYPE__ probe_size_t;
typedef __INT64_TYPE__ probe_result_t;
typedef __INT32_TYPE__ probe_status_t;

#if defined(__x86_64__)
#define PROBE_CALL __attribute__((ms_abi))
#else
#define PROBE_CALL
#endif

typedef probe_result_t (PROBE_CALL *probe_write_fn)(
    const unsigned char *buffer,
    probe_size_t length);
typedef probe_result_t (PROBE_CALL *probe_read_fn)(
    unsigned char *buffer,
    probe_size_t capacity);
typedef void (PROBE_CALL *probe_exit_fn)(probe_status_t status);

struct probe_dispatch {
    probe_write_fn write;
    probe_read_fn read;
    probe_exit_fn exit;
};

#endif
