param(
    [string]$HostPath = (Join-Path $PSScriptRoot '..\hosts\windows\out\MohabbatProbe.exe')
)

$ErrorActionPreference = 'Stop'
if (-not (Test-Path -LiteralPath $HostPath)) {
    throw "Windows probe not found: $HostPath. Run hosts\windows\build.ps1 first."
}

function Invoke-ProbeCase(
    [string]$Arguments,
    [string]$InputText,
    [string]$ExpectedOutputPattern,
    [int]$ExpectedExit
) {
    $startInfo = New-Object System.Diagnostics.ProcessStartInfo
    $startInfo.FileName = $HostPath
    $startInfo.Arguments = $Arguments
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true

    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $startInfo
    if (-not $process.Start()) {
        throw 'Could not start the Windows probe.'
    }

    $process.StandardInput.Write($InputText)
    $process.StandardInput.Flush()
    $process.StandardInput.Close()
    if (-not $process.WaitForExit(10000)) {
        $process.Kill()
        throw 'Windows probe timed out.'
    }
    $stdout = $process.StandardOutput.ReadToEnd()
    $stderr = $process.StandardError.ReadToEnd()
    $actual = $stdout -replace "`r`n", "`n"
    if ($process.ExitCode -ne $ExpectedExit -or $actual -notmatch $ExpectedOutputPattern) {
        throw "Windows probe failed (exit $($process.ExitCode), expected $ExpectedExit).`nstdout: $stdout`nstderr: $stderr"
    }
}

Invoke-ProbeCase '--verify-components' '' `
    '^Verified all four compressed components\.\n$' 0
Invoke-ProbeCase '' "alpha`r`nbeta`n`n" `
    '^Anonymous-memory probe ready \(compiled .+\)\. Enter an empty line to exit\.\n> echo: alpha\n> echo: beta\n> $' 0
Invoke-ProbeCase '' '' `
    '^Anonymous-memory probe ready \(compiled .+\)\. Enter an empty line to exit\.\n> $' 0
Invoke-ProbeCase '' (('x' * 257) + "`n") `
    '^Anonymous-memory probe ready \(compiled .+\)\. Enter an empty line to exit\.\n> $' 2

Write-Output 'Windows probe acceptance passed.'
