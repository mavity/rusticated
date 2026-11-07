ENTRY(unix_platform_entry)

SECTIONS
{
    . = 0;
    .payload :
    {
        KEEP(*(.text.unix_platform_entry))
        *(.text*)
        *(.rodata*)
        *(.data*)
        *(.bss*)
        *(COMMON)
    }

    /DISCARD/ :
    {
        *(.comment*)
        *(.eh_frame*)
        *(.note*)
    }
}
