param(
    [string]$PackerPath = (
        Join-Path $PSScriptRoot '..\hosts\windows\.build\packer\bin\Release\net462\PackComponents.exe'
    ),
    [string]$WindowsHostPath = (
        Join-Path $PSScriptRoot '..\hosts\windows\.build\MohabbatWindowsHost.exe'
    ),
    [string]$OutputPath,
    [switch]$UseRealLinuxAarch64Assets
)

$ErrorActionPreference = 'Stop'
if (-not (Test-Path -LiteralPath $PackerPath)) {
    throw "Component packer not found: $PackerPath"
}
if (-not (Test-Path -LiteralPath $WindowsHostPath)) {
    throw "Windows test host not found: $WindowsHostPath"
}

$workDir = Join-Path ([IO.Path]::GetTempPath()) (
    'MohabbatNext-polyglot-' + [Guid]::NewGuid().ToString('N')
)
$assetsDir = Join-Path $workDir 'assets'
$bootstrap = Join-Path $workDir 'bootstrap.sh'
$bootstrapToUse = $bootstrap
$bundle = Join-Path $workDir 'probe.exe'
if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
    $bundle = [IO.Path]::GetFullPath($OutputPath)
    if (Test-Path -LiteralPath $bundle) {
        throw "Refusing to overwrite existing output: $bundle"
    }
    $bundleDirectory = Split-Path -Parent $bundle
    New-Item -ItemType Directory -Force -Path $bundleDirectory | Out-Null
}
New-Item -ItemType Directory -Force -Path $assetsDir | Out-Null

try {
    $assetNames = @(
        'brot-x86_64-unknown-linux-gnu',
        'brot-aarch64-unknown-linux-gnu',
        'brot-aarch64-linux-android',
        'brot-x86_64-apple-darwin',
        'brot-aarch64-apple-darwin',
        'platform-linux-x86_64.bin',
        'platform-linux-aarch64.bin',
        'platform-darwin-x86_64.bin',
        'platform-darwin-aarch64.bin',
        'probe-x86_64.bin',
        'probe-aarch64.bin'
    )
        if ($UseRealLinuxAarch64Assets) {
            $sourceAssets = Join-Path $PSScriptRoot '..\hosts\unix\out'
            $linuxArmRunner = Join-Path $sourceAssets `
                'brot-aarch64-unknown-linux-gnu'
            if (-not (Test-Path -LiteralPath $linuxArmRunner)) {
                throw "Linux AArch64 Brot runner not found: $linuxArmRunner"
            }
            foreach ($assetName in $assetNames) {
                $source = Join-Path $sourceAssets $assetName
                if (-not (Test-Path -LiteralPath $source)) {
                    if (-not $assetName.StartsWith('brot-')) {
                        throw "Unix component not found: $source"
                    }
                    $source = $linuxArmRunner
                }
                Copy-Item -LiteralPath $source `
                    -Destination (Join-Path $assetsDir $assetName) -Force
            }
            $bootstrapToUse = Join-Path $PSScriptRoot `
                '..\hosts\unix\bootstrap.sh.in'
        }
        else {
            foreach ($assetName in $assetNames) {
                [IO.File]::WriteAllBytes(
                    (Join-Path $assetsDir $assetName),
                    [byte[]](1, 2, 3, 4)
                )
            }

            $bootstrapText = @(
                ':; [ -f "$1" ] || exit 21'
                ':; [ "$2" = "polyglot-argument" ] || exit 22'
                ':; exit 23'
            ) -join "`n"
            [IO.File]::WriteAllText(
                $bootstrap,
                $bootstrapText,
                ([System.Text.UTF8Encoding]::new($false))
            )
        }

        & $PackerPath --assemble $WindowsHostPath $bootstrapToUse $bundle $assetsDir
    if ($LASTEXITCODE -ne 0) {
        throw 'Polyglot assembler smoke test failed.'
    }

    $assembled = [IO.File]::ReadAllBytes($bundle)
    $peOffset = [BitConverter]::ToInt32($assembled, 0x3c)
    if ($assembled[0] -ne 0x4d -or $assembled[1] -ne 0x5a -or
        $peOffset -ne 128 -or
        $assembled[$peOffset] -ne 0x50 -or
        $assembled[$peOffset + 1] -ne 0x45) {
        throw 'Assembly did not preserve the MZ/PE header.'
    }

    $wsl = Get-Command wsl.exe -ErrorAction SilentlyContinue
    if ($null -ne $wsl) {
        $buildScript = Join-Path $PSScriptRoot '..\hosts\unix\build.sh'
        $normalizedBuild = Join-Path $workDir 'build.sh'
        $buildText = [IO.File]::ReadAllText($buildScript)
        $buildText = $buildText.Replace("`r`n", "`n").Replace("`r", "`n")
        [IO.File]::WriteAllText(
            $normalizedBuild,
            $buildText,
            ([System.Text.UTF8Encoding]::new($false))
        )
        $drive = $workDir.Substring(0, 1).ToLowerInvariant()
        $unixBuild = '/mnt/' + $drive + '/' +
            $normalizedBuild.Substring(3).Replace('\', '/')
        & $wsl.Source -- bash -n $unixBuild
        if ($LASTEXITCODE -ne 0) {
            throw 'Unix build script syntax validation failed.'
        }
    }

    if ($UseRealLinuxAarch64Assets) {
        if ($null -eq $wsl) {
            throw 'WSL is required for the Linux AArch64 runtime acceptance test.'
        }
        & (Join-Path $PSScriptRoot 'test-windows.ps1') -HostPath $bundle
        if ($LASTEXITCODE -ne 0) {
            throw 'Windows acceptance failed on the assembled polyglot.'
        }
    }
    if ($null -ne $wsl) {
        $drive = $workDir.Substring(0, 1).ToLowerInvariant()
        $unixBundle = '/mnt/' + $drive + '/' +
            $bundle.Substring(3).Replace('\', '/')
        if ($UseRealLinuxAarch64Assets) {
            $unixAcceptance = '/mnt/' + $drive + '/' +
                (Join-Path $PSScriptRoot 'test_probe.py').Substring(3).Replace('\', '/')
            $wslCommand = "python3 `"$unixAcceptance`" bash `"$unixBundle`""
        }
        else {
            $wslCommand = "bash `"$unixBundle`" polyglot-argument"
        }
        & $wsl.Source -- bash -lc $wslCommand
        if ($UseRealLinuxAarch64Assets) {
            if ($LASTEXITCODE -ne 0) {
                throw "Linux AArch64 acceptance failed with exit $LASTEXITCODE."
            }
        }
        elseif ($LASTEXITCODE -ne 23) {
            throw "Bash trampoline returned $LASTEXITCODE instead of 23."
        }
    }

    if ($UseRealLinuxAarch64Assets) {
        Write-Output 'Assembled polyglot passed Windows and Linux AArch64 acceptance.'
    }
    else {
        Write-Output 'Polyglot assembly and Bash trampoline acceptance passed.'
    }

}
finally {
    if (Test-Path -LiteralPath $workDir) {
        Remove-Item -LiteralPath $workDir -Recurse -Force
    }
}
