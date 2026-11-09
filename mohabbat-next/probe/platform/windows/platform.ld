ENTRY(windows_platform_entry)

HIDDEN(__abi_base     = -0x20000);
HIDDEN(__globals_base = -0x10000);

SECTIONS
{
    . = 0;
    .payload :
    {
        KEEP(*(.text.windows_platform_entry))
        *(.text*)
        *(.rodata*)
    }

    /DISCARD/ :
    {
        *(.comment*)
        *(.eh_frame*)
        *(.note*)
        *(.data*)
        *(.bss*)
        *(COMMON)
        *(.got*)
        *(.plt*)
    }
}
