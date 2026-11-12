# Install the cody CLI from GitHub Releases on Windows.
#
#   irm https://github.com/ajhous44/cody/releases/latest/download/install.ps1 | iex
#
# Environment (all optional):
#   CODY_VERSION      release tag to install, e.g. v3.0.0 (default: latest)
#   CODY_INSTALL_DIR  destination directory (default: %LOCALAPPDATA%\Programs\cody)
#   CODY_RELEASES     releases base URL, for mirrors and tests
#                     (default: https://github.com/ajhous44/cody/releases)

# A script block keeps these settings out of the caller's session under iex.
& {
	$ErrorActionPreference = 'Stop'
	# Windows PowerShell's progress bar slows downloads severalfold.
	$ProgressPreference = 'SilentlyContinue'
	[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

	$releases = if ($env:CODY_RELEASES) { $env:CODY_RELEASES.TrimEnd('/') } else { 'https://github.com/ajhous44/cody/releases' }
	$dir = if ($env:CODY_INSTALL_DIR) { $env:CODY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\cody' }

	# OSArchitecture sees through x64 emulation on ARM64; the variables cover older .NET.
	$cpu = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
	try { $cpu = [string][Runtime.InteropServices.RuntimeInformation]::OSArchitecture } catch {}
	$arch = @{ AMD64 = 'amd64'; X64 = 'amd64'; ARM64 = 'arm64' }[$cpu]
	if (-not $arch) { throw "cody install: unsupported CPU $cpu" }

	$version = $env:CODY_VERSION
	if (-not $version -or $version -eq 'latest') {
		# /releases/latest redirects to /releases/tag/<tag>: no API token or JSON.
		$req = [Net.WebRequest]::Create("$releases/latest")
		$req.Method = 'HEAD'
		$resp = $req.GetResponse()
		$version = $resp.ResponseUri.Segments[-1]
		$resp.Close()
	}
	if ($version -notmatch '^v') { $version = "v$version" }
	if ($version -notmatch '^v\d') { throw "cody install: could not find a release (got '$version')" }
	$asset = "cody_$($version.Substring(1))_windows_$arch.zip"
	$url = "$releases/download/$version"

	$tmp = Join-Path ([IO.Path]::GetTempPath()) ("cody-install-" + [guid]::NewGuid())
	New-Item -ItemType Directory -Path $tmp | Out-Null
	try {
		Write-Host "downloading cody $version (windows/$arch)"
		Invoke-WebRequest -UseBasicParsing -Uri "$url/$asset" -OutFile (Join-Path $tmp $asset)
		Invoke-WebRequest -UseBasicParsing -Uri "$url/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

		$want = Get-Content (Join-Path $tmp 'checksums.txt') |
			ForEach-Object { $f = -split $_; if ($f[1] -eq $asset) { $f[0] } } |
			Select-Object -First 1
		if (-not $want) { throw "cody install: $asset is not listed in checksums.txt" }
		$got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash
		if ($got -ne $want) { throw "cody install: checksum mismatch for $asset; nothing was installed" }

		Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath (Join-Path $tmp 'x') -Force
		New-Item -ItemType Directory -Force -Path $dir | Out-Null
		$exe = Join-Path $dir 'cody.exe'
		# A running cody.exe cannot be overwritten, but it can be renamed aside.
		if (Test-Path $exe) { Move-Item -Force $exe "$exe.old" }
		Copy-Item (Join-Path $tmp 'x\cody.exe') $exe
		Remove-Item "$exe.old" -ErrorAction SilentlyContinue
	} finally {
		Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
	}

	$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
	if (($userPath -split ';') -notcontains $dir) {
		$newPath = if ($userPath) { "$userPath;$dir" } else { $dir }
		[Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
		$env:Path = "$env:Path;$dir"
		Write-Host "added $dir to your user PATH (new terminals pick it up)"
	}
	Write-Host "installed $(& $exe version) to $exe"
}
