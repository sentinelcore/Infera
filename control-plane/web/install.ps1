# Infera node installer for Windows (PowerShell). One command does everything: installs Infera
# and Ollama, pairs, picks and downloads the right models (plus speech and video on NVIDIA GPUs),
# benchmarks and starts at login. The dashboard shows the line with your code:
#   irm "https://infera.valanor.network/install.ps1?code=123456" | iex
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"   # much faster downloads
if (-not (Get-Variable -Name Code -ErrorAction SilentlyContinue)) { $Code = $env:INFERA_CODE }
$repo = if ($env:INFERA_REPO) { $env:INFERA_REPO } else { "sentinelcore/Infera" }
$dest = Join-Path $env:USERPROFILE ".infera\bin"
New-Item -ItemType Directory -Force -Path $dest | Out-Null
$zip = Join-Path $env:TEMP "infera.zip"
Write-Host "Downloading Infera ..."
Get-Process infera -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Invoke-WebRequest "https://github.com/$repo/releases/download/test-build/infera-windows-x64.zip" -OutFile $zip
Expand-Archive -Force $zip -DestinationPath $dest
Remove-Item $zip
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$dest*") { [Environment]::SetEnvironmentVariable("Path", "$userPath;$dest", "User") }
$env:Path = "$env:Path;$dest"
Write-Host "Installed to $dest\infera.exe"
if ($Code) {
  & (Join-Path $dest "infera.exe") setup $Code
} else {
  Write-Host "Next: get a pairing code on the dashboard (Contribute tab), then run:  infera setup CODE"
}
