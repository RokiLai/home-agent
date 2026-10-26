$ErrorActionPreference = "Stop"
if (-not $env:HOMEAGENT_SERVER) { $env:HOMEAGENT_SERVER = "https://homeagent.rokilai.online" }
$token = if ($env:HOMEAGENT_CLAIM_TOKEN) { $env:HOMEAGENT_CLAIM_TOKEN } elseif ($env:HOMEAGENT_JOIN_TOKEN) { $env:HOMEAGENT_JOIN_TOKEN } else { $null }
$repo = if ($env:HOMEAGENT_GITHUB_REPO) { $env:HOMEAGENT_GITHUB_REPO } else { "RokiLai/home-agent" }
$version = $env:HOMEAGENT_AGENT_VERSION
$tagPattern = '^(agent-)?v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\z'
if (-not $env:HOMEAGENT_DOWNLOAD_BASE_URL -and $version -and $version -cnotmatch $tagPattern) { throw "invalid Agent release tag" }
if ($repo -cnotmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\z' -or ($repo.Split('/') -contains '..')) { throw "invalid GitHub repository" }
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}
$asset = "homeagent-agent-windows-$arch.exe"

# ConvertFrom-Json alone silently overwrites duplicate keys on Windows PowerShell
# 5.1. Validate JSON grammar and decoded object keys before invoking it.
if (-not ('HomeAgentInstallerJson' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Text;
public sealed class HomeAgentInstallerJson {
    readonly string text; int pos;
    HomeAgentInstallerJson(string value) { text=value; }
    void Fail() { throw new FormatException("invalid GitHub release response"); }
    void Space() { while(pos<text.Length && " \t\r\n".IndexOf(text[pos])>=0) pos++; }
    char Peek() { return pos<text.Length ? text[pos] : '\0'; }
    void Take(char c) { if(Peek()!=c) Fail(); pos++; }
    string Str() {
        Take('"'); var result=new StringBuilder();
        while(pos<text.Length) {
            char c=text[pos++]; if(c=='"') return result.ToString();
            if(c<32) Fail();
            if(c=='\\') {
                if(pos>=text.Length) Fail(); c=text[pos++];
                if(c=='u') {
                    if(pos+4>text.Length) Fail(); int number=0;
                    for(int i=0;i<4;i++) {
                        char h=text[pos++]; int n="0123456789abcdef".IndexOf(Char.ToLowerInvariant(h));
                        if(n<0) Fail(); number=number*16+n;
                    }
                    c=(char)number;
                } else if(c=='b') c='\b'; else if(c=='f') c='\f';
                else if(c=='n') c='\n'; else if(c=='r') c='\r'; else if(c=='t') c='\t';
                else if(c!='"' && c!='\\' && c!='/') Fail();
            }
            result.Append(c);
        }
        Fail(); return null;
    }
    void Value(int depth) {
        if(depth>128) Fail(); Space(); char c=Peek();
        if(c=='{' || c=='[') {
            pos++; Space(); char end=c=='{'?'}':']';
            var keys=new HashSet<string>(StringComparer.Ordinal);
            if(Peek()==end) {pos++;return;}
            while(true) {
                Space();
                if(c=='{') {string key=Str(); if(!keys.Add(key)) Fail(); Space(); Take(':');}
                Value(depth+1); Space(); if(Peek()!=',') break; pos++;
            }
            Take(end);
        } else if(c=='"') Str();
        else if(c=='t' || c=='f' || c=='n') {
            string word=c=='t'?"true":c=='f'?"false":"null";
            if(pos+word.Length>text.Length || text.Substring(pos,word.Length)!=word) Fail(); pos+=word.Length;
        } else {
            if(c=='-') {pos++;c=Peek();}
            if(c=='0') pos++;
            else {if(c<'1' || c>'9') Fail(); while(Peek()>='0' && Peek()<='9') pos++;}
            if(Peek()=='.') {pos++;if(Peek()<'0'||Peek()>'9') Fail();while(Peek()>='0'&&Peek()<='9')pos++;}
            if(Peek()=='e'||Peek()=='E') {pos++;if(Peek()=='+'||Peek()=='-')pos++;if(Peek()<'0'||Peek()>'9')Fail();while(Peek()>='0'&&Peek()<='9')pos++;}
        }
    }
    public static void Validate(string text) {
        var parser=new HomeAgentInstallerJson(text); parser.Space();
        if(parser.Peek()!='[') parser.Fail(); parser.Value(0); parser.Space();
        if(parser.pos!=text.Length) parser.Fail();
    }
}
'@
}

function Get-InstallerFile([string]$Uri, [string]$Path) {
    $deadline = [DateTime]::UtcNow.AddSeconds(120)
    $current = [Uri]$Uri
    for ($redirect = 0; $redirect -le 10; $redirect++) {
        if ($current.Scheme -notin @('http', 'https')) { throw "invalid download URL" }
        $request = [System.Net.HttpWebRequest]::Create($current)
        $request.UserAgent = 'HomeAgent-Installer'
        $request.AllowAutoRedirect = $false
        $request.Timeout = 10000
        $request.ReadWriteTimeout = 10000
        $response = $null
        $pending = $null
        try {
            $remaining = [int]($deadline - [DateTime]::UtcNow).TotalMilliseconds
            if ($remaining -le 0) { throw "HTTP download timeout" }
            $pending = $request.GetResponseAsync()
            if (-not $pending.Wait([Math]::Min(10000, $remaining))) {
                $request.Abort()
                throw "HTTP download timeout"
            }
            $response = $pending.GetAwaiter().GetResult()
            $status = [int]$response.StatusCode
            if ($status -in @(301, 302, 303, 307, 308)) {
                if (-not $response.Headers['Location']) { throw "invalid HTTP redirect" }
                $current = [Uri]::new($current, $response.Headers['Location'])
                continue
            }
            if ($status -lt 200 -or $status -ge 300) { throw "HTTP download failed" }
            $inputStream = $response.GetResponseStream()
            $outputStream = [IO.File]::Create($Path)
            try {
                $buffer = New-Object byte[] 65536
                while ($true) {
                    $remaining = [int]($deadline - [DateTime]::UtcNow).TotalMilliseconds
                    if ($remaining -le 0) { throw "HTTP download timeout" }
                    $readPending = $inputStream.ReadAsync($buffer, 0, $buffer.Length)
                    if (-not $readPending.Wait([Math]::Min(10000, $remaining))) {
                        $request.Abort()
                        throw "HTTP download timeout"
                    }
                    $read = $readPending.GetAwaiter().GetResult()
                    if ($read -eq 0) { break }
                    $outputStream.Write($buffer, 0, $read)
                }
            } finally {
                $outputStream.Dispose()
                $inputStream.Dispose()
            }
            return $response.Headers['Link']
        } catch {
            throw "HTTP download failed (network, timeout or non-2xx response)"
        } finally {
            if ($response) { $response.Close() }
            $request.Abort()
        }
    }
    throw "invalid HTTP redirect chain"
}

function Compare-InstallerVersion([string]$Left, [string]$Right) {
    $a = $Left.Substring(7).Split('.')
    $b = $Right.Substring(7).Split('.')
    for ($i = 0; $i -lt 3; $i++) {
        if ($a[$i].Length -ne $b[$i].Length) { return $a[$i].Length.CompareTo($b[$i].Length) }
        $comparison = [String]::CompareOrdinal($a[$i], $b[$i])
        if ($comparison -ne 0) { return $comparison }
    }
    return 0
}

$tempDir = Join-Path ([IO.Path]::GetTempPath()) ("homeagent-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempDir | Out-Null
$binaryTmp = Join-Path $tempDir 'agent.exe'
$shaTmp = Join-Path $tempDir 'checksum'
try {
    if ($env:HOMEAGENT_DOWNLOAD_BASE_URL) {
        $url = "$($env:HOMEAGENT_DOWNLOAD_BASE_URL.TrimEnd('/'))/$asset"
    } else {
        if (-not $version) {
            $best = $null
            $complete = $false
            $pageFile = Join-Path $tempDir 'page.json'
            for ($page = 1; $page -le 10; $page++) {
                $link = Get-InstallerFile "https://api.github.com/repos/$repo/releases?per_page=100&page=$page" $pageFile
                try {
                    $json = [IO.File]::ReadAllText($pageFile, [Text.UTF8Encoding]::new($false, $true))
                    [HomeAgentInstallerJson]::Validate($json)
                    $releases = @(ConvertFrom-Json -InputObject $json)
                } catch { throw "invalid GitHub release response" }
                foreach ($release in $releases) {
                    if ($release -isnot [PSCustomObject] -or $release.tag_name -isnot [string] -or $release.draft -isnot [bool] -or $release.prerelease -isnot [bool] -or $release.assets -isnot [Array]) { throw "invalid GitHub release response" }
                    foreach ($entry in $release.assets) {
                        if ($entry -isnot [PSCustomObject] -or $entry.name -isnot [string]) { throw "invalid GitHub release response" }
                    }
                    if ($release.draft -or $release.prerelease -or $release.tag_name -cnotmatch '^agent-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\z') { continue }
                    if (-not $best -or (Compare-InstallerVersion $release.tag_name $best.tag_name) -gt 0) { $best = $release }
                    elseif ($release.tag_name -ceq $best.tag_name) { throw "Agent release assets duplicated across records" }
                }
                if ($releases.Count -lt 100 -and $link -notmatch 'rel="?next"?') { $complete = $true; break }
            }
            if (-not $complete) { throw "incomplete GitHub release scan" }
            if (-not $best) { throw "no stable Agent release" }
            $binaries = @($best.assets | Where-Object { $_.name -ceq $asset })
            $checksums = @($best.assets | Where-Object { $_.name -ceq "$asset.sha256" })
            if ($binaries.Count -ne 1 -or $checksums.Count -ne 1) { throw "Agent release assets missing or duplicated" }
            $version = $best.tag_name
        }
        $url = "https://github.com/$repo/releases/download/$version/$asset"
    }
    $null = Get-InstallerFile $url $binaryTmp
    $null = Get-InstallerFile "$url.sha256" $shaTmp
    $lines = @([IO.File]::ReadAllLines($shaTmp))
    if ($lines.Count -ne 1) { throw "invalid SHA256 checksum file" }
    $parts = $lines[0].Trim() -split '\s+'
    if ($parts.Count -gt 2 -or $parts[0] -notmatch '^[0-9a-fA-F]{64}$' -or ($parts.Count -eq 2 -and $parts[1] -cne $asset -and $parts[1] -cne "*$asset")) { throw "invalid SHA256 checksum file" }
    $expectedSha = $parts[0].ToLowerInvariant()
    $actualSha = (Get-FileHash -Path $binaryTmp -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($expectedSha -ne $actualSha) { throw "SHA256 checksum mismatch" }
    $installDir = if ($env:HOMEAGENT_INSTALL_DIR) { $env:HOMEAGENT_INSTALL_DIR } else { Join-Path $env:ProgramFiles "HomeAgent" }
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    $binary = Join-Path $installDir "homeagent-agent.exe"
    Move-Item -Path $binaryTmp -Destination $binary -Force
} finally {
    Remove-Item -LiteralPath $tempDir -Recurse -Force
}

$service = Get-Service sshd -ErrorAction SilentlyContinue
if (-not $service) { throw "Windows OpenSSH Server is required. Install the OpenSSH.Server capability first." }
if ($service.Status -ne "Running") { Start-Service sshd }
Set-Service sshd -StartupType Automatic

if ($token) {
    # 1. Claim device and persist dedicated Device Token locally
    $claimArgs = @("claim", "--server", $env:HOMEAGENT_SERVER, "--claim-token", $token)
    if ($env:HOMEAGENT_SSH_USER) {
        $claimArgs += @("--ssh-user", $env:HOMEAGENT_SSH_USER)
    }
    if ($env:HOMEAGENT_SSH_PORT) {
        $claimArgs += @("--ssh-port", $env:HOMEAGENT_SSH_PORT)
    }
    & $binary $claimArgs
    if ($LASTEXITCODE -ne 0) { throw "Agent claim failed (exit $LASTEXITCODE)" }

    # 2. Setup background daemon service using persisted device configuration
    & $binary service install --server $env:HOMEAGENT_SERVER --binary $binary
    if ($LASTEXITCODE -ne 0) { throw "Agent service install failed (exit $LASTEXITCODE)" }

    Write-Host "HomeAgent onboarding and daemon service setup completed successfully!"
} else {
    Write-Host ""
    Write-Host "HomeAgent Agent binary installed successfully to: $binary"
    Write-Host "To complete device onboarding, run:"
    Write-Host "  & `"$binary`" claim --server `"$($env:HOMEAGENT_SERVER)`" --claim-token `"<YOUR_CLAIM_TOKEN>`""
    Write-Host "  & `"$binary`" service install --binary `"$binary`""
    Write-Host ""
}
