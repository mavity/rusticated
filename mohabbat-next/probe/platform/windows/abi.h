#ifndef MOHABBAT_NEXT_WINDOWS_PLATFORM_ABI_H
#define MOHABBAT_NEXT_WINDOWS_PLATFORM_ABI_H

typedef __SIZE_TYPE__ platform_size_t;
typedef __INT64_TYPE__ platform_result_t;
typedef __INT32_TYPE__ platform_status_t;
typedef __UINT32_TYPE__ platform_u32;

#if defined(__x86_64__)
#define PLATFORM_CALL __attribute__((ms_abi))
#else
#define PLATFORM_CALL
#endif

typedef void *(PLATFORM_CALL *platform_load_library_fn)(const char *name);
typedef void *(PLATFORM_CALL *platform_get_proc_address_fn)(
    void *module,
    const char *name);
typedef void *(PLATFORM_CALL *platform_alloc_fn)(platform_size_t size);
typedef void (PLATFORM_CALL *platform_free_fn)(void *address);

struct windows_platform_host {
    platform_load_library_fn load_library;
    platform_get_proc_address_fn get_proc_address;
    platform_alloc_fn alloc;
    platform_free_fn free;
    const unsigned char *hello_payload;
    platform_size_t hello_payload_size;
};

int PLATFORM_CALL windows_platform_entry(
    const struct windows_platform_host *host);

#endif
