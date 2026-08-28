$ErrorActionPreference = "Stop"
if (-not $env:HOMEAGENT_SERVER) { $env:HOMEAGENT_SERVER = "https://homeagent.rokilai.online" }
$token = if ($env:HOMEAGENT_CLAIM_TOKEN) { $env:HOMEAGENT_CLAIM_TOKEN } elseif ($env:HOMEAGENT_JOIN_TOKEN) { $env:HOMEAGENT_JOIN_TOKEN } else { $null }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}
$installDir = if ($env:HOMEAGENT_INSTALL_DIR) { $env:HOMEAGENT_INSTALL_DIR } else { Join-Path $env:ProgramFiles "HomeAgent" }
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$binary = Join-Path $installDir "homeagent-agent.exe"
$binaryTmp = Join-Path $installDir "homeagent-agent.tmp.exe"
$shaTmp = Join-Path $installDir "homeagent-agent.sha256"

$repo = if ($env:HOMEAGENT_GITHUB_REPO) { $env:HOMEAGENT_GITHUB_REPO } else { "RokiLai/home-agent" }
$version = if ($env:HOMEAGENT_AGENT_VERSION) { $env:HOMEAGENT_AGENT_VERSION } else { "v0.6.8" }

$url = if ($env:HOMEAGENT_DOWNLOAD_BASE_URL) {
    "$($env:HOMEAGENT_DOWNLOAD_BASE_URL.TrimEnd('/'))/homeagent-agent-windows-$arch.exe"
} elseif ($env:HOMEAGENT_SERVER -and ($env:HOMEAGENT_SERVER -ne "https://homeagent.rokilai.online")) {
    "$($env:HOMEAGENT_SERVER.TrimEnd('/'))/downloads/homeagent-agent-windows-$arch.exe"
} else {
    "https://github.com/$repo/releases/download/$version/homeagent-agent-windows-$arch.exe"
}
$shaUrl = "$url.sha256"

try {
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $binaryTmp
    Invoke-WebRequest -UseBasicParsing -Uri $shaUrl -OutFile $shaTmp
    $expectedSha = ((Get-Content -Path $shaTmp -Raw).Trim() -split '\s+')[0].ToLower()
    $actualSha = (Get-FileHash -Path $binaryTmp -Algorithm SHA256).Hash.ToLower()
    if (-not $expectedSha -or -not $actualSha -or ($expectedSha -ne $actualSha)) {
        throw "SHA256 checksum verification failed for $url. Expected: $expectedSha, Actual: $actualSha"
    }
    Move-Item -Path $binaryTmp -Destination $binary -Force
} finally {
    if (Test-Path $binaryTmp) { Remove-Item $binaryTmp -Force }
    if (Test-Path $shaTmp) { Remove-Item $shaTmp -Force }
}

$service = Get-Service sshd -ErrorAction SilentlyContinue
if (-not $service) { throw "Windows OpenSSH Server is required. Install the OpenSSH.Server capability first." }
if ($service.Status -ne "Running") { Start-Service sshd }
Set-Service sshd -StartupType Automatic

if ($token) {
    # 1. Claim device and persist dedicated Device Token locally
    & $binary claim --server $env:HOMEAGENT_SERVER --claim-token $token

    # 2. Setup background daemon service using persisted device configuration
    & $binary service install --server $env:HOMEAGENT_SERVER --binary $binary

    Write-Host "HomeAgent onboarding and daemon service setup completed successfully!"
} else {
    Write-Host ""
    Write-Host "HomeAgent Agent binary installed successfully to: $binary"
    Write-Host "To complete device onboarding, run:"
    Write-Host "  & `"$binary`" claim --server `"$($env:HOMEAGENT_SERVER)`" --claim-token `"<YOUR_CLAIM_TOKEN>`""
    Write-Host "  & `"$binary`" service install --binary `"$binary`""
    Write-Host ""
}
