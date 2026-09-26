[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
$ErrorActionPreference = 'Stop'
$dir = '__DIR__'
$tar = Join-Path $env:SystemRoot 'System32\tar.exe'
if (-not (Test-Path -LiteralPath $tar -PathType Leaf)) {
  [Console]::Error.WriteLine('this Windows has no tar.exe; Windows 10 version 1803 or later is required')
  exit 4
}
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$st = Join-Path $dir ('.quil-staging-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $st | Out-Null
Write-Output '__quil_prepare__'
Write-Output $st
Write-Output $tar
exit 0
