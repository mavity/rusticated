#define _GNU_SOURCE
#define _POSIX_C_SOURCE 200809L

#include "abi.h"
#include "payloads.h"

#include <errno.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/types.h>
#include <unistd.h>

#if defined(__APPLE__)
#include <pthread.h>
#ifndef MAP_JIT
#error "This macOS SDK does not expose MAP_JIT"
#endif
#endif

#if defined(__x86_64__)
#define HOST_CALL __attribute__((ms_abi))
#else
#define HOST_CALL
#endif

typedef void (HOST_CALL *probe_entry_fn)(const struct probe_dispatch *);

static probe_result_t HOST_CALL probe_write(
    const unsigned char *buffer,
    probe_size_t length)
{
    probe_size_t offset = 0;
    while (offset < length) {
        ssize_t count = write(STDOUT_FILENO, buffer + offset, length - offset);
        if (count < 0 && errno == EINTR) {
            continue;
        }
        if (count <= 0) {
            return -1;
        }
        offset += (probe_size_t)count;
    }
    return (probe_result_t)offset;
}

static probe_result_t HOST_CALL probe_read(
    unsigned char *buffer,
    probe_size_t capacity)
{
    for (;;) {
        ssize_t count = read(STDIN_FILENO, buffer, capacity);
        if (count < 0 && errno == EINTR) {
            continue;
        }
        return count < 0 ? -1 : (probe_result_t)count;
    }
}

static void HOST_CALL probe_exit(probe_status_t status)
{
    _exit(status);
}

static int prepare_payload(void **memory, size_t *memory_size,
                           const unsigned char **payload, size_t *payload_size)
{
#if defined(__x86_64__)
    *payload = probe_payload_x86_64;
    *payload_size = sizeof(probe_payload_x86_64);
#elif defined(__aarch64__)
    *payload = probe_payload_aarch64;
    *payload_size = sizeof(probe_payload_aarch64);
#else
    (void)memory;
    (void)memory_size;
    (void)payload;
    (void)payload_size;
    fputs("unsupported host architecture\n", stderr);
    return -1;
#endif

    long page_size = sysconf(_SC_PAGESIZE);
    if (page_size <= 0 || *payload_size > SIZE_MAX - (size_t)page_size) {
        fputs("invalid system page size or payload size\n", stderr);
        return -1;
    }
    *memory_size = ((*payload_size + (size_t)page_size - 1) /
                    (size_t)page_size) * (size_t)page_size;

#if defined(__APPLE__)
    *memory = mmap(NULL, *memory_size, PROT_READ | PROT_WRITE | PROT_EXEC,
                   MAP_PRIVATE | MAP_ANON | MAP_JIT, -1, 0);
#else
    *memory = mmap(NULL, *memory_size, PROT_READ | PROT_WRITE,
                   MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
#endif
    if (*memory == MAP_FAILED) {
        perror("mmap executable payload");
        return -1;
    }

#if defined(__APPLE__)
    pthread_jit_write_protect_np(0);
#endif
    memcpy(*memory, *payload, *payload_size);
#if defined(__APPLE__)
    pthread_jit_write_protect_np(1);
#else
    if (mprotect(*memory, *memory_size, PROT_READ | PROT_EXEC) != 0) {
        perror("mprotect executable payload");
        return -1;
    }
#endif
    __builtin___clear_cache((char *)*memory,
                            (char *)*memory + *payload_size);
    return 0;
}

int main(void)
{
    void *memory = NULL;
    size_t memory_size = 0;
    size_t payload_size = 0;
    const unsigned char *payload = NULL;
    if (prepare_payload(&memory, &memory_size, &payload, &payload_size) != 0) {
        return 1;
    }

    struct probe_dispatch dispatch = {
        .write = probe_write,
        .read = probe_read,
        .exit = probe_exit,
    };
    probe_entry_fn entry = (probe_entry_fn)memory;
    entry(&dispatch);

    fprintf(stderr, "probe payload returned unexpectedly (%zu bytes)\n",
            payload_size);
    (void)munmap(memory, memory_size);
    return 70;
}
