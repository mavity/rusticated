ENTRY(probe_code_start)

HIDDEN(__abi_base     = -0x20000);
HIDDEN(__globals_base = -0x10000);

SECTIONS
{
    . = 0;
    .payload :
    {
        KEEP(*(.text.probe_entry))
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
        *(.got*)
        *(.plt*)
    }
}
