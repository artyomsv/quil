[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
$ErrorActionPreference = 'Stop'
$dir = '__DIR__'
$st = '__STAGING__'
$want = @{__HASHES__}
function Fail($m) {
  [Console]::Error.WriteLine($m)
  Remove-Item -LiteralPath $st -Recurse -Force -ErrorAction SilentlyContinue
  exit 3
}
$got = @(Get-ChildItem -LiteralPath $st -File -Recurse)
if ($got.Count -ne $want.Count) { Fail ('archive has ' + $got.Count + ' files, expected ' + $want.Count) }
foreach ($f in $got) {
  if ($f.DirectoryName -ne $st -or -not $want.ContainsKey($f.Name)) { Fail ('unexpected file in archive: ' + $f.Name) }
  $h = (Get-FileHash -LiteralPath $f.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($h -ne $want[$f.Name]) { Fail ('checksum mismatch: ' + $f.Name) }
}
foreach ($f in $got) {
  $dst = Join-Path $dir $f.Name
  if (Test-Path -LiteralPath $dst) {
    $bak = $dst + '.old'
    $n = 1
    while ((Test-Path -LiteralPath $bak) -and $n -le 20) {
      try { Remove-Item -LiteralPath $bak -Force; break } catch { $bak = $dst + '.old.' + $n; $n++ }
    }
    Rename-Item -LiteralPath $dst -NewName (Split-Path -Leaf $bak)
  }
  Move-Item -LiteralPath $f.FullName -Destination $dst
}
Remove-Item -LiteralPath $st -Recurse -Force -ErrorAction SilentlyContinue
Get-ChildItem -LiteralPath $dir -File | Where-Object { $_.Name -match '^(quil|quild|quil-activate)\.exe\.old(\.\d+)?$' } | ForEach-Object {
  try { Remove-Item -LiteralPath $_.FullName -Force } catch {}
}
Write-Output '__quil_install__'
Write-Output (Join-Path $dir 'quil.exe')
exit 0
