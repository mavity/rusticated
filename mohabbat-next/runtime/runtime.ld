ENTRY(slice_entry)

HIDDEN(__abi_base     = -0x20000);
HIDDEN(__globals_base = -0x10000);

SECTIONS
{
    . = 0x0;
    .payload :
    {
        KEEP(*(.text.entry))
        *(.text .text.*)
        *(.rodata .rodata.*)
        *(.data.rel.ro .data.rel.ro.*)
        *(.got .got.*)
        *(.data .data.*)
        *(.bss .bss.*)
        *(COMMON)
    }

    /DISCARD/ :
    {
        *(.comment*)
        *(.eh_frame*)
        *(.note*)
    }
}
