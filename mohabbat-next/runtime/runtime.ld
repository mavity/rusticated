ENTRY(runtime_code_start)

HIDDEN(__abi_base     = -0x20000);
HIDDEN(__globals_base = -0x10000);

SECTIONS
{
    . = -0x1F000;
    .bss (NOLOAD) :
    {
        *(.bss .bss.*)
        *(COMMON)
    }

    . = -0x18000;
    .rwinit :
    {
        *(.data.rel.ro .data.rel.ro.*)
    }

    . = 0x0;
    .payload :
    {
        KEEP(*(.text.entry))
        *(.text .text.*)
        *(zwasm_interp)
        *(.rodata .rodata.*)
        . = ALIGN(16);
        HIDDEN(runtime_code_end = .);
    }

    /DISCARD/ :
    {
        *(.comment*)
        *(.eh_frame*)
        *(.note*)
        *(.data .data.*)
        *(.got .got.*)
        *(.plt .plt.*)
    }
}
