ENTRY(probe_entry)

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
    }
}
