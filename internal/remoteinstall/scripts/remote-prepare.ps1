[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
$ErrorActionPreference = 'Stop'
$dir = '__DIR__'
$tar = Join-Path $env:SystemRoot 'System32\tar.exe'
if (-not (Test-Path -LiteralPath $tar -PathType Leaf)) {
  [Console]::Error.WriteLine('this Windows has no tar.exe; Windows 10 version 1803 or later is required')
  exit 4
}
# CreateDirectory, not New-Item -Path: -Path treats [ and ] as wildcards.
[IO.Directory]::CreateDirectory($dir) | Out-Null
# Staging dirs a failed or interrupted install left behind.
Get-ChildItem -LiteralPath $dir -Directory -Filter '.quil-staging-*' | ForEach-Object { try { Remove-Item -LiteralPath $_.FullName -Recurse -Force } catch {} }
$st = Join-Path $dir ('.quil-staging-' + [guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($st) | Out-Null
Write-Output '__quil_prepare__'
Write-Output $st
Write-Output $tar
exit 0
