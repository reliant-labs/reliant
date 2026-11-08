# install.ps1 - install the forge CLI from a GitHub release, no Go toolchain needed.
#
#   irm https://raw.githubusercontent.com/reliant-labs/forge/main/install.ps1 | iex
#
# Non-interactive: detects the CPU, downloads the matching release archive,
# verifies its sha256 against the release's checksums.txt, and installs forge.exe.
# It does not edit PATH; it prints the one line that does.
#
# Environment:
#   FORGE_VERSION      release to install, e.g. v1.0.0 or 1.0.0 (default: latest)
#   FORGE_INSTALL_DIR  directory to install into (default: $HOME\.local\bin)
#
# Testing seam: FORGE_RELEASE_BASE_URL replaces https://github.com/reliant-labs/forge/releases.
#
# Asset names come from scripts/build-release-assets.sh; keep this in step with it.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

function Fail($msg) { throw "forge install: $msg" }

$repo = 'reliant-labs/forge'
$base = if ($env:FORGE_RELEASE_BASE_URL) { $env:FORGE_RELEASE_BASE_URL.TrimEnd('/') } else { "https://github.com/$repo/releases" }

switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
  'X64'   { $arch = 'amd64' }
  'Arm64' { $arch = 'arm64' }
  default { Fail "unsupported CPU: $([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) (releases cover amd64 and arm64)" }
}

if ($env:FORGE_VERSION) {
  $tag = 'v' + $env:FORGE_VERSION.TrimStart('v')
  if ($tag -notmatch '^v[0-9A-Za-z.+-]+$') { Fail "FORGE_VERSION must look like v1.0.0 (got: $($env:FORGE_VERSION))" }
  $releaseUrl = "$base/download/$tag"
  $wanted = $tag
} else {
  $releaseUrl = "$base/latest/download"
  $wanted = 'the latest release'
}

$destDir = if ($env:FORGE_INSTALL_DIR) { $env:FORGE_INSTALL_DIR } else { Join-Path $HOME '.local\bin' }
$asset = "forge_windows_$arch.tar.gz"

if (-not (Get-Command tar -ErrorAction SilentlyContinue)) { Fail 'need tar.exe (ships with Windows 10 1803 and later) to unpack the release' }

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("forge-install-" + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Installing forge ($wanted, windows/$arch) into $destDir"

  try { Invoke-WebRequest -UseBasicParsing -Uri "$releaseUrl/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') }
  catch { Fail "could not download $releaseUrl/checksums.txt - $wanted may not exist or may not have binaries attached yet. Pin a published version with `$env:FORGE_VERSION='vX.Y.Z'`, or build from source: go install github.com/$repo/cmd/forge@latest" }
  try { Invoke-WebRequest -UseBasicParsing -Uri "$releaseUrl/$asset" -OutFile (Join-Path $tmp $asset) }
  catch { Fail "could not download $releaseUrl/$asset - this release has no build for windows/$arch" }

  $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match "^[0-9a-fA-F]{64}\s+\*?$([regex]::Escape($asset))\s*$" } | Select-Object -First 1
  if (-not $line) { Fail "checksums.txt has no entry for $asset" }
  $expected = ($line -split '\s+')[0].ToLower()
  $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash.ToLower()
  if ($expected -ne $actual) { Fail "sha256 mismatch for $asset (expected $expected, got $actual); nothing was installed" }

  $unpacked = Join-Path $tmp 'unpacked'
  New-Item -ItemType Directory -Path $unpacked | Out-Null
  & tar -xzf (Join-Path $tmp $asset) -C $unpacked
  if ($LASTEXITCODE -ne 0) { Fail "could not unpack $asset" }
  if (-not (Test-Path (Join-Path $unpacked 'forge.exe'))) { Fail "$asset does not contain forge.exe" }

  New-Item -ItemType Directory -Force -Path $destDir | Out-Null
  Copy-Item -Force (Join-Path $unpacked 'forge.exe') (Join-Path $destDir 'forge.exe')
  Write-Host "Installed $(Join-Path $destDir 'forge.exe')"
  & (Join-Path $destDir 'forge.exe') version

  $onPath = ($env:PATH -split ';') -contains $destDir
  if (-not $onPath) {
    Write-Host ''
    Write-Host "$destDir is not on your PATH. Add it for this session and future ones:"
    Write-Host "  `$env:PATH = `"$destDir;`$env:PATH`""
    Write-Host "  [Environment]::SetEnvironmentVariable('PATH', `"$destDir;`" + [Environment]::GetEnvironmentVariable('PATH', 'User'), 'User')"
  }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
