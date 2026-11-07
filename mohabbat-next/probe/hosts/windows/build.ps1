param(
    [switch]$WindowsHostOnly
)

$ErrorActionPreference = 'Stop'

$scriptDir = $PSScriptRoot
$probeDir = Join-Path $scriptDir '..\..'
$payloadDir = Join-Path $probeDir 'payload'
$platformBuilder = Join-Path $probeDir 'platform\windows\build-platform.ps1'
$unixAssets = Join-Path $scriptDir '..\unix\out'
$workDir = Join-Path $scriptDir '.build'
$componentDir = Join-Path $workDir 'components'
$packerProject = Join-Path $scriptDir 'PackComponents.csproj'
$outDir = Join-Path $scriptDir 'out'
$outputExe = Join-Path $outDir 'MohabbatProbe.exe'
$windowsHostExe = Join-Path $workDir 'MohabbatWindowsHost.exe'
$compiler = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
$manifest = Join-Path $scriptDir 'ProbeHost.manifest'

if (-not (Test-Path -LiteralPath $compiler)) {
    throw ".NET Framework compiler not found: $compiler"
}

& (Join-Path $payloadDir 'build-payload.ps1')
if ($LASTEXITCODE -ne 0) { throw 'Hello-world payload build failed.' }

& $platformBuilder
if ($LASTEXITCODE -ne 0) { throw 'Windows platform abstraction build failed.' }

$unixPayloadDir = Join-Path $payloadDir 'out'
New-Item -ItemType Directory -Force -Path $unixAssets | Out-Null
Copy-Item (Join-Path $unixPayloadDir 'probe-x86_64.bin') $unixAssets -Force
Copy-Item (Join-Path $unixPayloadDir 'probe-aarch64.bin') $unixAssets -Force

dotnet build $packerProject -c Release --nologo `
    ("-p:BaseOutputPath={0}" -f (Join-Path $workDir 'packer\bin\')) `
    ("-p:BaseIntermediateOutputPath={0}" -f (Join-Path $workDir 'packer\obj\'))
if ($LASTEXITCODE -ne 0) { throw 'Zstandard component packer build failed.' }

$packer = Join-Path $workDir 'packer\bin\Release\net462\PackComponents.exe'
if (-not (Test-Path -LiteralPath $packer)) {
    throw "Zstandard component packer not found: $packer"
}

$payloadOutputDir = Join-Path $payloadDir 'out'
$platformOutputDir = Join-Path $scriptDir '.build\platform'
New-Item -ItemType Directory -Force -Path $componentDir, $outDir | Out-Null

$components = @(
    @{
        Input = Join-Path $payloadOutputDir 'probe-x86_64.bin'
        Output = Join-Path $componentDir 'hello-x86_64.zst'
        Resource = 'MohabbatNext.Hello.x86_64.zst'
    },
    @{
        Input = Join-Path $payloadOutputDir 'probe-aarch64.bin'
        Output = Join-Path $componentDir 'hello-aarch64.zst'
        Resource = 'MohabbatNext.Hello.aarch64.zst'
    },
    @{
        Input = Join-Path $platformOutputDir 'platform-windows-x86_64.bin'
        Output = Join-Path $componentDir 'platform-windows-x86_64.zst'
        Resource = 'MohabbatNext.Platform.Windows.x86_64.zst'
    },
    @{
        Input = Join-Path $platformOutputDir 'platform-windows-aarch64.bin'
        Output = Join-Path $componentDir 'platform-windows-aarch64.zst'
        Resource = 'MohabbatNext.Platform.Windows.aarch64.zst'
    }
)

$packArguments = @()
foreach ($component in $components) {
    if (-not (Test-Path -LiteralPath $component.Input)) {
        throw "Component binary not found: $($component.Input)"
    }
    $packArguments += $component.Input
    $packArguments += $component.Output
}

& $packer $packArguments
if ($LASTEXITCODE -ne 0) { throw 'Zstandard component packing failed.' }

$resources = @()
$references = @()
$packerDependencies = Get-ChildItem -LiteralPath (Split-Path -Parent $packer) `
    -Filter '*.dll' -File
if ($packerDependencies.Count -eq 0) {
    throw 'No managed Zstandard runtime dependencies were built.'
}
foreach ($dependency in $packerDependencies) {
    $resourceName = 'MohabbatNext.Internal.' + $dependency.Name
    $resources += ('/resource:{0},{1}' -f $dependency.FullName, $resourceName)
    $references += ('/reference:{0}' -f $dependency.FullName)
}
foreach ($component in $components) {
    $resources += ('/resource:{0},{1}' -f $component.Output, $component.Resource)
}

& $compiler /nologo /target:exe /platform:anycpu `
    ("/out:{0}" -f $windowsHostExe) `
    ("/win32manifest:{0}" -f $manifest) `
    '/reference:System.dll' `
    $references `
    $resources `
    (Join-Path $scriptDir 'Program.cs')
if ($LASTEXITCODE -ne 0) { throw 'AnyCPU Windows launcher build failed.' }

if ($WindowsHostOnly) {
    Write-Output "Built the Windows-only test host: $windowsHostExe"
    return
}

$bootstrap = Join-Path $scriptDir '..\unix\bootstrap.sh.in'
& $packer --assemble $windowsHostExe $bootstrap $outputExe $unixAssets
if ($LASTEXITCODE -ne 0) { throw 'Single-file Windows/Unix assembly failed.' }

Write-Output "Built the single Windows/Unix deliverable: $outputExe"
