$ErrorActionPreference = "Stop"
if (-not $env:HOMEAGENT_SERVER) { throw "HOMEAGENT_SERVER is required" }
if (-not $env:HOMEAGENT_JOIN_TOKEN) { throw "HOMEAGENT_JOIN_TOKEN is required" }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}
$installDir = if ($env:HOMEAGENT_INSTALL_DIR) { $env:HOMEAGENT_INSTALL_DIR } else { Join-Path $env:ProgramFiles "HomeAgent" }
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$binary = Join-Path $installDir "homeagent-agent.exe"
$url = "$($env:HOMEAGENT_SERVER.TrimEnd('/'))/downloads/homeagent-agent-windows-$arch.exe"
Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $binary

$service = Get-Service sshd -ErrorAction SilentlyContinue
if (-not $service) { throw "Windows OpenSSH Server is required. Install the OpenSSH.Server capability first." }
if ($service.Status -ne "Running") { Start-Service sshd }
Set-Service sshd -StartupType Automatic

# 1. Register device
& $binary join --server $env:HOMEAGENT_SERVER --token $env:HOMEAGENT_JOIN_TOKEN

# 2. Setup background daemon service
& $binary service install --server $env:HOMEAGENT_SERVER --token $env:HOMEAGENT_JOIN_TOKEN --binary $binary

Write-Host "HomeAgent installation and daemon service setup completed successfully!"
