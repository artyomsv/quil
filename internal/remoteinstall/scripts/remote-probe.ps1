[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
$OutputEncoding = [Console]::OutputEncoding
$ErrorActionPreference = 'SilentlyContinue'
Write-Output '__quil_probe_win__'
$la = $env:LOCALAPPDATA
if (-not $la) { $la = '-' }
Write-Output $la
Write-Output 'windows'
$arch = $env:PROCESSOR_ARCHITEW6432
if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
if (-not $arch) { $arch = '-' }
Write-Output $arch
$found = $null
if ($env:LOCALAPPDATA) {
  $p = Join-Path $env:LOCALAPPDATA 'Programs\quil\quil.exe'
  if (Test-Path -LiteralPath $p -PathType Leaf) { $found = $p }
}
if (-not $found) {
  $c = Get-Command quil -CommandType Application | Where-Object { $_.Source -like '*.exe' } | Select-Object -First 1
  if ($c) { $found = $c.Source }
}
if (-not $found) {
  Write-Output '-'
  Write-Output '-'
} else {
  Write-Output $found
  $d = Split-Path -Parent $found
  $rw = 'ro'
  try {
    $t = Join-Path $d ('.quil-probe-' + [guid]::NewGuid().ToString('N'))
    [IO.File]::WriteAllText($t, '')
    Remove-Item -LiteralPath $t -Force
    $open = @((Get-Acl -LiteralPath $d).Access | Where-Object {
      $_.AccessControlType -eq 'Allow' -and
      -not ($_.PropagationFlags -band [Security.AccessControl.PropagationFlags]::InheritOnly) -and
      (([int64]$_.FileSystemRights -band 0x50000116) -ne 0) -and
      (@('S-1-1-0','S-1-5-32-545','S-1-5-11','S-1-5-4') -contains $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value)
    })
    if ($open.Count -eq 0) { $rw = 'rw' }
  } catch { $rw = 'ro' }
  Write-Output $rw
}
$sh = (Get-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\OpenSSH' -Name DefaultShell).DefaultShell
if (-not $sh) { $sh = '-' }
Write-Output $sh
exit 0
