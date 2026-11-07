$ErrorActionPreference = 'Stop'

$scriptDir = $PSScriptRoot
$probeDir = Join-Path $scriptDir '..\..'
$outDir = Join-Path (Join-Path $probeDir 'hosts\windows') '.build\platform'
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$targets = @(
    @{ Name = 'x86_64'; Triple = 'x86_64-unknown-none-elf'; FixedRegisters = @() },
    @{ Name = 'aarch64'; Triple = 'aarch64-unknown-none-elf'; FixedRegisters = @('-ffixed-x18') }
)

foreach ($target in $targets) {
    $object = Join-Path $outDir ("platform-windows-{0}.o" -f $target.Name)
    $elf = Join-Path $outDir ("platform-windows-{0}.elf" -f $target.Name)
    $binary = Join-Path $outDir ("platform-windows-{0}.bin" -f $target.Name)

    & clang "--target=$($target.Triple)" -O2 -ffreestanding -fno-builtin `
        -fno-stack-protector -fPIC -fno-unwind-tables `
        -fno-asynchronous-unwind-tables -fno-exceptions -fno-rtti `
        -fno-jump-tables -I (Join-Path $probeDir 'payload') `
        $target.FixedRegisters `
        -c (Join-Path $scriptDir 'platform.c') -o $object
    if ($LASTEXITCODE -ne 0) {
        throw "Windows platform compilation failed for $($target.Name)."
    }

    & ld.lld -T (Join-Path $scriptDir 'platform.ld') --build-id=none `
        --no-undefined -o $elf $object
    if ($LASTEXITCODE -ne 0) {
        throw "Windows platform linking failed for $($target.Name)."
    }

    $relocations = & llvm-readobj --relocations $elf
    if ($LASTEXITCODE -ne 0) {
        throw "Could not inspect $($target.Name) platform relocations."
    }
    $relocationEntries = @(
        $relocations | Select-String '^\s*0x[0-9a-fA-F]+\s+'
    )
    if ($relocationEntries.Count -gt 0) {
        throw "The $($target.Name) Windows platform contains relocations."
    }

    $sections = & llvm-readobj --sections $elf
    if ($LASTEXITCODE -ne 0) {
        throw "Could not inspect $($target.Name) platform sections."
    }
    if ($sections -match '\.(tdata|tbss)') {
        throw "The $($target.Name) Windows platform contains TLS."
    }

    $undefinedSymbols = @(& llvm-nm --undefined-only $elf)
    if ($LASTEXITCODE -ne 0) {
        throw "Could not inspect $($target.Name) platform symbols."
    }
    if ($undefinedSymbols.Count -gt 0) {
        throw "The $($target.Name) Windows platform has undefined symbols."
    }

    $instructions = & llvm-objdump --disassemble-symbols=windows_platform_entry $elf
    if ($LASTEXITCODE -ne 0) {
        throw "Could not disassemble $($target.Name) Windows platform."
    }
    if ($instructions -match '\b(syscall|sysenter|svc)\b|\bint\s+0x80\b') {
        throw "The $($target.Name) Windows platform contains a direct trap instruction."
    }

    & llvm-objcopy -O binary --only-section=.payload $elf $binary
    if ($LASTEXITCODE -ne 0) {
        throw "Windows platform extraction failed for $($target.Name)."
    }
    $binaryLength = (Get-Item -LiteralPath $binary).Length
    if ($binaryLength -eq 0 -or $binaryLength -gt 4096) {
        throw "The $($target.Name) Windows platform must fit within one 4 KiB page."
    }
}

Write-Output "Built x86_64 and AArch64 Windows platform abstractions in $outDir"
