ENTRY(unix_platform_entry)

HIDDEN(__abi_base     = -0x20000);
HIDDEN(__globals_base = -0x10000);

SECTIONS
{
    . = 0;
    .payload :
    {
        KEEP(*(.text.unix_platform_entry))
        *(.text*)
        *(.rodata*)
        *(.data.rel.ro .data.rel.ro.*)
    }

    /DISCARD/ :
    {
        *(.comment*)
        *(.eh_frame*)
        *(.note*)
        *(.data .data.*)
        *(.bss*)
        *(COMMON)
        *(.got*)
        *(.plt*)
    }
}
