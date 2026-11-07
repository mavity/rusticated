$ErrorActionPreference = 'Stop'

$scriptDir = $PSScriptRoot
$outDir = Join-Path $scriptDir 'out'
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$targets = @(
    @{ Name = 'x86_64'; Triple = 'x86_64-unknown-none-elf' },
    @{ Name = 'aarch64'; Triple = 'aarch64-unknown-none-elf' }
)

foreach ($target in $targets) {
    $object = Join-Path $outDir ("probe-{0}.o" -f $target.Name)
    $elf = Join-Path $outDir ("probe-{0}.elf" -f $target.Name)
    $binary = Join-Path $outDir ("probe-{0}.bin" -f $target.Name)

    & clang --target=$($target.Triple) -O2 -ffreestanding -fno-builtin `
        -fno-stack-protector -fPIC -fno-unwind-tables `
        -fno-asynchronous-unwind-tables -fno-exceptions -fno-rtti `
        -c (Join-Path $scriptDir 'probe.c') -o $object
    if ($LASTEXITCODE -ne 0) { throw "Payload compilation failed for $($target.Name)." }

    & ld.lld -T (Join-Path $scriptDir 'payload.ld') --build-id=none `
        --no-undefined -o $elf $object
    if ($LASTEXITCODE -ne 0) { throw "Payload linking failed for $($target.Name)." }

    $relocations = & llvm-readobj --relocations $elf
    if ($LASTEXITCODE -ne 0) { throw "Could not inspect $($target.Name) relocations." }
    $relocationEntries = @($relocations | Select-String '^\s*0x[0-9a-fA-F]+\s+')
    if ($relocationEntries.Count -gt 0) {
        throw "The $($target.Name) payload contains relocations."
    }

    $sections = & llvm-readobj --sections $elf
    if ($LASTEXITCODE -ne 0) {
        throw "Could not inspect $($target.Name) sections."
    }
    if ($sections -match '\.(tdata|tbss)') {
        throw "The $($target.Name) payload contains TLS."
    }

    $undefinedSymbols = @(& llvm-nm --undefined-only $elf)
    if ($LASTEXITCODE -ne 0) { throw "Could not inspect $($target.Name) symbols." }
    if ($undefinedSymbols.Count -gt 0) {
        throw "The $($target.Name) payload has undefined symbols."
    }

    $instructions = & llvm-objdump --disassemble-symbols=probe_entry $elf
    if ($LASTEXITCODE -ne 0) { throw "Could not disassemble $($target.Name) payload." }
    if ($instructions -match '\b(syscall|sysenter|svc)\b|\bint\s+0x80\b') {
        throw "The $($target.Name) payload contains a direct system-call instruction."
    }

    & llvm-objcopy -O binary --only-section=.payload $elf $binary
    if ($LASTEXITCODE -ne 0) { throw "Payload extraction failed for $($target.Name)." }
    $binaryLength = (Get-Item -LiteralPath $binary).Length
    if ($binaryLength -eq 0 -or $binaryLength -gt 4096) {
        throw "The $($target.Name) payload must fit within one 4 KiB page."
    }
}

Write-Output "Built x86_64 and aarch64 payloads in $outDir"
