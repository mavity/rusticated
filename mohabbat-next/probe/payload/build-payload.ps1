$ErrorActionPreference = 'Stop'

$scriptDir = $PSScriptRoot
$probeDir = Join-Path $scriptDir '..'
$workspace = Join-Path $probeDir 'Cargo.toml'
$rustBuildDir = Join-Path $probeDir 'hosts\windows\.build\rust'
$cargoHome = Join-Path $rustBuildDir 'cargo-home'
$outDir = Join-Path $scriptDir 'out'
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$targets = @(
    @{ Name = 'x86_64'; Triple = 'x86_64-unknown-none' },
    @{ Name = 'aarch64'; Triple = 'aarch64-unknown-none' }
)

foreach ($target in $targets) {
    $elf = Join-Path $outDir ("probe-{0}.elf" -f $target.Name)
    $binary = Join-Path $outDir ("probe-{0}.bin" -f $target.Name)

    $previousCargoHome = $env:CARGO_HOME
    $hadCargoHome = Test-Path Env:CARGO_HOME
    $env:CARGO_HOME = $cargoHome
    Push-Location $probeDir
    try {
        & cargo -Z build-std=core build --manifest-path $workspace `
            --package mohabbat-next-payload --release `
            --target $target.Triple --target-dir $rustBuildDir
        if ($LASTEXITCODE -ne 0) {
            throw "Rust payload compilation failed for $($target.Name)."
        }
    }
    finally {
        Pop-Location
        if ($hadCargoHome) {
            $env:CARGO_HOME = $previousCargoHome
        }
        else {
            Remove-Item Env:CARGO_HOME
        }
    }

    $dependencyDir = Join-Path $rustBuildDir (
        Join-Path $target.Triple 'release\deps')
    $archives = @()
    foreach ($pattern in @(
        'libmohabbat_next_payload-*.rlib',
        'libcore-*.rlib'
    )) {
        $archiveMatch = Get-ChildItem -LiteralPath $dependencyDir `
            -Filter $pattern -File |
            Sort-Object LastWriteTimeUtc -Descending |
            Select-Object -First 1
        if (-not $archiveMatch) {
            throw "Could not find a Rust archive matching $pattern for $($target.Name)."
        }
        $archives += $archiveMatch.FullName
    }

    & ld.lld -T (Join-Path $scriptDir 'payload.ld') --build-id=none `
        --gc-sections `
        --no-undefined --start-group $archives --end-group -o $elf
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
