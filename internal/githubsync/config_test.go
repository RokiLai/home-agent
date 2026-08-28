package githubsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHConfig_ManagedBlock(t *testing.T) {
	// 1. New file / empty content
	res, err := UpdateSSHConfigManagedBlock(nil, "~/.ssh/homeagent_github_id_ed25519")
	if err != nil {
		t.Fatalf("UpdateSSHConfigManagedBlock failed: %v", err)
	}
	str := string(res)
	if !strings.Contains(str, SSHBlockBegin) || !strings.Contains(str, SSHBlockEnd) {
		t.Fatalf("missing block markers: %s", str)
	}
	if !strings.Contains(str, "IdentityFile ~/.ssh/homeagent_github_id_ed25519") {
		t.Fatalf("missing identity file: %s", str)
	}

	// 2. Existing unrelated config
	existing := []byte("Host custom-server\n    HostName 10.0.0.1\n    User admin\n")
	res, err = UpdateSSHConfigManagedBlock(existing, "/custom/path/key")
	if err != nil {
		t.Fatalf("UpdateSSHConfigManagedBlock failed: %v", err)
	}
	str = string(res)
	if !strings.Contains(str, "Host custom-server") {
		t.Fatalf("lost existing config: %s", str)
	}
	if !strings.Contains(str, "IdentityFile /custom/path/key") {
		t.Fatalf("missing updated identity file: %s", str)
	}

	// 3. Idempotent replacement
	res2, err := UpdateSSHConfigManagedBlock(res, "/updated/path/key")
	if err != nil {
		t.Fatalf("UpdateSSHConfigManagedBlock replace failed: %v", err)
	}
	str2 := string(res2)
	if strings.Count(str2, SSHBlockBegin) != 1 {
		t.Fatalf("expected exactly 1 begin marker, got: %s", str2)
	}
	if !strings.Contains(str2, "IdentityFile /updated/path/key") {
		t.Fatalf("missing new identity file: %s", str2)
	}
	if strings.Contains(str2, "/custom/path/key") {
		t.Fatalf("old key still present: %s", str2)
	}

	// 4. Remove block
	removed, err := RemoveSSHConfigManagedBlock(res2)
	if err != nil {
		t.Fatalf("RemoveSSHConfigManagedBlock failed: %v", err)
	}
	strRemoved := string(removed)
	if strings.Contains(strRemoved, SSHBlockBegin) || strings.Contains(strRemoved, "IdentityFile") {
		t.Fatalf("block was not removed: %s", strRemoved)
	}
	if !strings.Contains(strRemoved, "Host custom-server") {
		t.Fatalf("lost original content during remove: %s", strRemoved)
	}
}

func TestGHHosts_ContentManipulation(t *testing.T) {
	// 1. From scratch
	res, err := UpdateGHHostsContent(nil, "github.com", "exampleuser", "gho_token123", "ssh")
	if err != nil {
		t.Fatalf("UpdateGHHostsContent failed: %v", err)
	}
	str := string(res)
	if !strings.Contains(str, "github.com:") || !strings.Contains(str, "user: exampleuser") || !strings.Contains(str, "oauth_token: gho_token123") {
		t.Fatalf("unexpected hosts content: %s", str)
	}

	// 2. Existing other host
	existing := []byte("enterprise.internal:\n    user: workuser\n    oauth_token: ghp_work\n    git_protocol: https\n")
	res, err = UpdateGHHostsContent(existing, "github.com", "exampleuser", "gho_token123", "ssh")
	if err != nil {
		t.Fatalf("UpdateGHHostsContent failed: %v", err)
	}
	str = string(res)
	if !strings.Contains(str, "enterprise.internal:") || !strings.Contains(str, "user: workuser") {
		t.Fatalf("lost existing enterprise host: %s", str)
	}
	if !strings.Contains(str, "github.com:") || !strings.Contains(str, "user: exampleuser") {
		t.Fatalf("missing github.com host: %s", str)
	}

	// 3. Update existing github.com
	res, err = UpdateGHHostsContent(res, "github.com", "newuser", "gho_token_updated", "ssh")
	if err != nil {
		t.Fatalf("UpdateGHHostsContent failed: %v", err)
	}
	str = string(res)
	if strings.Count(str, "github.com:") != 1 {
		t.Fatalf("duplicate github.com entries: %s", str)
	}
	if !strings.Contains(str, "user: newuser") || !strings.Contains(str, "oauth_token: gho_token_updated") {
		t.Fatalf("failed to update github.com: %s", str)
	}

	// 4. Remove github.com
	removed, err := RemoveGHHostsContent(res, "github.com")
	if err != nil {
		t.Fatalf("RemoveGHHostsContent failed: %v", err)
	}
	strRemoved := string(removed)
	if strings.Contains(strRemoved, "github.com:") {
		t.Fatalf("github.com was not removed: %s", strRemoved)
	}
	if !strings.Contains(strRemoved, "enterprise.internal:") {
		t.Fatalf("enterprise host was lost during removal: %s", strRemoved)
	}
}

func TestConfig_FileOperations(t *testing.T) {
	tempDir := t.TempDir()
	sshConfig := filepath.Join(tempDir, "ssh_config")
	hostsYaml := filepath.Join(tempDir, "hosts.yml")

	// Apply SSH Config
	if err := ApplySSHConfigFile(sshConfig, "~/.ssh/id_test"); err != nil {
		t.Fatalf("ApplySSHConfigFile failed: %v", err)
	}
	b, err := os.ReadFile(sshConfig)
	if err != nil || !strings.Contains(string(b), "IdentityFile ~/.ssh/id_test") {
		t.Fatalf("ApplySSHConfigFile content invalid: %v, %s", err, string(b))
	}

	// Clean SSH Config
	if err := CleanSSHConfigFile(sshConfig); err != nil {
		t.Fatalf("CleanSSHConfigFile failed: %v", err)
	}

	// Apply GH Hosts
	if err := ApplyGHHostsFile(hostsYaml, "github.com", "user1", "gho_abc", "ssh"); err != nil {
		t.Fatalf("ApplyGHHostsFile failed: %v", err)
	}
	b, err = os.ReadFile(hostsYaml)
	if err != nil || !strings.Contains(string(b), "user: user1") {
		t.Fatalf("ApplyGHHostsFile content invalid: %v, %s", err, string(b))
	}

	// Clean GH Hosts
	if err := CleanGHHostsFile(hostsYaml, "github.com"); err != nil {
		t.Fatalf("CleanGHHostsFile failed: %v", err)
	}
	if _, err := os.Stat(hostsYaml); !os.IsNotExist(err) {
		b, _ := os.ReadFile(hostsYaml)
		if len(strings.TrimSpace(string(b))) != 0 {
			t.Fatalf("expected hostsYaml to be empty or deleted, got: %s", string(b))
		}
	}
}
