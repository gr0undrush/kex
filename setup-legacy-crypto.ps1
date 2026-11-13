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
if ($t -match '"aes192-cbc"|aes192cbcID|AES192CBC') {
    Write-Host 'This x/crypto version already knows aes192-cbc, no patch needed.'
} else {
    # Matches the aes128-cbc entry whatever the key is called in this version
    # (aes128cbcID in older releases, InsecureCipherAES128CBC in newer ones).
    $pattern = '\w+\s*:\s*\{\s*16\s*,\s*aes\.BlockSize\s*,\s*newAESCBCCipher\s*\}\s*,'
    $m = [regex]::Matches($t, $pattern)
    if ($m.Count -ne 1) {
        Write-Host "Lines mentioning CBC in ssh\cipher.go:"
        Select-String -Path $f -Pattern 'CBC' | ForEach-Object { Write-Host ("  {0}: {1}" -f $_.LineNumber, $_.Line.Trim()) }
        throw "Expected exactly one aes128-cbc entry in ssh\cipher.go, found $($m.Count). Send me the lines printed above."
    }
    $repl = '$0' + "`n`t" + '"aes192-cbc": {24, aes.BlockSize, newAESCBCCipher},' + "`n`t" + '"aes256-cbc": {32, aes.BlockSize, newAESCBCCipher},'
    $t = [regex]::Replace($t, $pattern, $repl)
    [System.IO.File]::WriteAllText($f, $t)
    Write-Host 'Patched ssh\cipher.go'
}

go mod edit -replace 'golang.org/x/crypto=./third_party/crypto'
go mod tidy
Write-Host 'Done. Now run: wails build'
