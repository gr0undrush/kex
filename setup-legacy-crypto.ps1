# Vendors golang.org/x/crypto into third_party\crypto and adds aes192-cbc / aes256-cbc,
# which the upstream ssh package does not implement (it only has aes128-cbc and 3des-cbc).
# Run once from the project root:  powershell -ExecutionPolicy Bypass -File .\setup-legacy-crypto.ps1
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

go mod tidy
$src = (go list -m -f '{{.Dir}}' golang.org/x/crypto).Trim()
if (-not $src) { throw 'golang.org/x/crypto not found in module graph' }

$dst = Join-Path $PSScriptRoot 'third_party\crypto'
if (Test-Path $dst) { Remove-Item $dst -Recurse -Force }
New-Item -ItemType Directory -Force (Split-Path $dst) | Out-Null
Copy-Item $src $dst -Recurse
Get-ChildItem $dst -Recurse -Force | Where-Object { -not $_.PSIsContainer } | ForEach-Object { $_.IsReadOnly = $false }

$f = Join-Path $dst 'ssh\cipher.go'
$t = [System.IO.File]::ReadAllText($f)
if ($t -match '"aes192-cbc"|aes192cbcID') {
    Write-Host 'This x/crypto version already knows aes192-cbc, no patch needed.'
} else {
    $pattern = 'aes128cbcID:\s*\{16,\s*aes\.BlockSize,\s*newAESCBCCipher\},'
    if ($t -notmatch $pattern) { throw 'Could not find the aes128-cbc entry in ssh\cipher.go - see README for the manual patch.' }
    $repl = '$0' + "`n`t" + '"aes192-cbc": {24, aes.BlockSize, newAESCBCCipher},' + "`n`t" + '"aes256-cbc": {32, aes.BlockSize, newAESCBCCipher},'
    $t = [regex]::Replace($t, $pattern, $repl)
    [System.IO.File]::WriteAllText($f, $t)
    Write-Host 'Patched ssh\cipher.go'
}

go mod edit -replace 'golang.org/x/crypto=./third_party/crypto'
go mod tidy
Write-Host 'Done. Now run: wails build'
