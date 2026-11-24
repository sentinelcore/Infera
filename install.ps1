# Infera node installer for Windows (PowerShell):
#   irm https://raw.githubusercontent.com/sentinelcore/Infera/main/install.ps1 | iex
$ErrorActionPreference = "Stop"
$repo = if ($env:INFERA_REPO) { $env:INFERA_REPO } else { "sentinelcore/Infera" }
$dest = Join-Path $env:USERPROFILE ".infera\bin"
New-Item -ItemType Directory -Force -Path $dest | Out-Null
$zip = Join-Path $env:TEMP "infera.zip"
Write-Host "Downloading infera-windows-x64 ..."
Invoke-WebRequest "https://github.com/$repo/releases/download/test-build/infera-windows-x64.zip" -OutFile $zip
Expand-Archive -Force $zip -DestinationPath $dest
Remove-Item $zip
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$dest*") { [Environment]::SetEnvironmentVariable("Path", "$userPath;$dest", "User") }
Write-Host "Installed to $dest\infera.exe (open a new terminal to use 'infera')"
Write-Host "Next: infera pair <code> --server <dashboard url>, then infera engine ..., then infera run"
