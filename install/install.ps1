# mote installer for Windows (PowerShell 5.1 or 7).
#
# Downloads one released mote binary from GitHub over HTTPS, verifies it
# against the release's SHA256SUMS, installs it to %LOCALAPPDATA%\Programs\mote
# (no administrator rights), adds that folder to the user PATH, then runs
# `mote setup`. `mote setup` explains and verifies each later download.
#
# Environment:
#   MOTE_SOURCE    set to 1 to build the current source from GitHub instead
#                  of downloading a release (needs Go; see MOTE_VERSION)
#   MOTE_VERSION   release tag to install, or with MOTE_SOURCE the branch,
#                  tag or commit to build (default: latest, main from source)
#   MOTE_PREFIX    install directory
#   MOTE_NO_SETUP  set to 1 to skip `mote setup`
#   MOTE_BASE_URL  alternative release location (https:// URL or local folder), for mirrors and tests
# Arguments are passed to `mote setup`.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Set-StrictMode -Version 3

$Repo = 'jgalego/mote'
$Version = if ($env:MOTE_VERSION) { $env:MOTE_VERSION } else { 'latest' }
$Prefix = if ($env:MOTE_PREFIX) { $env:MOTE_PREFIX } else { Join-Path $env:LOCALAPPDATA 'Programs\mote' }

function Say($msg) { Write-Host "mote-install: $msg" }
# throw instead of exit: under `irm | iex` exit would close the user's shell.
function Die($msg) { throw "mote-install: error: $msg" }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { Die "unsupported CPU architecture $($env:PROCESSOR_ARCHITECTURE) (need AMD64 or ARM64)" }
}

$fromSource = $env:MOTE_SOURCE -eq '1'
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("mote-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    New-Item -ItemType Directory -Force -Path $Prefix | Out-Null
    if ($fromSource) {
        if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
            Die 'building from source needs Go: https://go.dev/dl/'
        }
        $ref = if ($Version -eq 'latest') { 'main' } else { $Version }
        $gov = (((go version) -split ' ')[2])
        Say "building github.com/$Repo/cmd/mote@$ref with $gov"
        # Go's module proxy caches what a branch points at for a few
        # minutes, so fetch straight from the repository unless a proxy is
        # already configured.
        $proxy = $env:GOPROXY
        $env:GOBIN = $tmp
        if (-not $proxy) { $env:GOPROXY = 'direct' }
        # A direct fetch looks the module up under several paths at once,
        # sharing one git clone, and one lookup can unshallow it while
        # another reads it ("shallow file has changed"). A second try finds
        # the clone complete.
        try {
            & go install "github.com/$Repo/cmd/mote@$ref"
            if ($LASTEXITCODE -ne 0) {
                Say 'retrying the build'
                & go install "github.com/$Repo/cmd/mote@$ref"
            }
        }
        finally {
            Remove-Item Env:GOBIN -ErrorAction SilentlyContinue
            if (-not $proxy) { Remove-Item Env:GOPROXY -ErrorAction SilentlyContinue }
        }
        if ($LASTEXITCODE -ne 0) { Die "build failed; check that $ref exists in https://github.com/$Repo" }
        Copy-Item -Force (Join-Path $tmp 'mote.exe') (Join-Path $Prefix 'mote.exe')
    }
    else {
        if ($env:MOTE_BASE_URL) { $base = $env:MOTE_BASE_URL }
        elseif ($Version -eq 'latest') { $base = "https://github.com/$Repo/releases/latest/download" }
        else { $base = "https://github.com/$Repo/releases/download/$Version" }
        $local = Test-Path -LiteralPath $base -PathType Container
        if (-not $local -and -not $base.StartsWith('https://')) { Die "refusing non-HTTPS download location: $base" }

        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        function Fetch($name, $dest) {
            if ($local) { Copy-Item -LiteralPath (Join-Path $base $name) -Destination $dest }
            else { Invoke-WebRequest -UseBasicParsing -Uri "$base/$name" -OutFile $dest }
        }

        $asset = "mote_windows_$arch.zip"
        Say "downloading $asset ($Version) from $base"
        Fetch $asset (Join-Path $tmp $asset)
        Fetch 'SHA256SUMS' (Join-Path $tmp 'SHA256SUMS')

        $want = $null
        foreach ($line in Get-Content (Join-Path $tmp 'SHA256SUMS')) {
            $parts = $line -split '\s+', 2
            if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $asset) { $want = $parts[0].ToLower() }
        }
        if (-not $want) { Die "$asset is not listed in SHA256SUMS" }
        $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash.ToLower()
        if ($want -ne $got) { Die "checksum mismatch for $asset (want $want, got $got)" }
        Say 'sha256 verified'

        Expand-Archive -LiteralPath (Join-Path $tmp $asset) -DestinationPath (Join-Path $tmp 'x') -Force
        Copy-Item -Force (Join-Path $tmp 'x\mote.exe') (Join-Path $Prefix 'mote.exe')
    }
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

$exe = Join-Path $Prefix 'mote.exe'
Say "installed $exe ($(& $exe version))"

# Add the install folder to the user PATH once; nothing else is changed.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath) { $userPath = '' }
if (($userPath -split ';') -notcontains $Prefix) {
    [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ";$Prefix").TrimStart(';'), 'User')
    Say "added $Prefix to your user PATH (open a new terminal to use 'mote')"
}
$env:Path = "$Prefix;$env:Path"

if ($env:MOTE_NO_SETUP -eq '1') { return }
& $exe setup @args
