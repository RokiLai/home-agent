package githubsync

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	SSHBlockBegin = "# BEGIN HOMEAGENT GITHUB MANAGED"
	SSHBlockEnd   = "# END HOMEAGENT GITHUB MANAGED"
)

// UpdateSSHConfigManagedBlock 在已有的 SSH 配置文件内容中插入或替换 HomeAgent 托管的 GitHub 主机配置块。
func UpdateSSHConfigManagedBlock(existing []byte, identityFilePath string) ([]byte, error) {
	if identityFilePath == "" {
		identityFilePath = "~/.ssh/" + DefaultGitHubKeyFilename
	}

	blockLines := []string{
		SSHBlockBegin,
		"Host github.com",
		"    HostName github.com",
		"    User git",
		"    IdentityFile " + identityFilePath,
		"    IdentitiesOnly yes",
		SSHBlockEnd,
	}
	blockContent := strings.Join(blockLines, "\n")

	content := string(existing)
	beginIdx := strings.Index(content, SSHBlockBegin)
	endIdx := strings.Index(content, SSHBlockEnd)

	if beginIdx != -1 && endIdx != -1 && endIdx >= beginIdx {
		// Replace existing block
		endIdx += len(SSHBlockEnd)
		prefix := strings.TrimRight(content[:beginIdx], "\r\n")
		suffix := strings.TrimLeft(content[endIdx:], "\r\n")

		var res strings.Builder
		if len(prefix) > 0 {
			res.WriteString(prefix)
			res.WriteString("\n\n")
		}
		res.WriteString(blockContent)
		if len(suffix) > 0 {
			res.WriteString("\n\n")
			res.WriteString(suffix)
		}
		res.WriteString("\n")
		return []byte(res.String()), nil
	}

	// Append block to end
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return []byte(blockContent + "\n"), nil
	}
	return []byte(trimmed + "\n\n" + blockContent + "\n"), nil
}

// RemoveSSHConfigManagedBlock 从 ~/.ssh/config 文本中移除 HomeAgent 托管的配置块。
func RemoveSSHConfigManagedBlock(existing []byte) ([]byte, error) {
	content := string(existing)
	beginIdx := strings.Index(content, SSHBlockBegin)
	endIdx := strings.Index(content, SSHBlockEnd)

	if beginIdx == -1 || endIdx == -1 || endIdx < beginIdx {
		return existing, nil
	}

	endIdx += len(SSHBlockEnd)
	prefix := strings.TrimRight(content[:beginIdx], "\r\n")
	suffix := strings.TrimLeft(content[endIdx:], "\r\n")

	var res strings.Builder
	if len(prefix) > 0 {
		res.WriteString(prefix)
	}
	if len(prefix) > 0 && len(suffix) > 0 {
		res.WriteString("\n\n")
	}
	if len(suffix) > 0 {
		res.WriteString(suffix)
	}
	if res.Len() > 0 {
		res.WriteString("\n")
	}
	return []byte(res.String()), nil
}

// ApplySSHConfigFile 读取、更新并原子写入 ~/.ssh/config 配置文件。
func ApplySSHConfigFile(path, identityFilePath string) error {
	if path == "" {
		p, err := DefaultSSHConfigPath()
		if err != nil {
			return err
		}
		path = p
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read ssh config: %w", err)
	}

	updated, err := UpdateSSHConfigManagedBlock(existing, identityFilePath)
	if err != nil {
		return fmt.Errorf("update ssh config block: %w", err)
	}

	return AtomicWriteFile(path, updated, 0600)
}

// CleanSSHConfigFile 读取 ~/.ssh/config 并清除其中的托管块，完成原子回写。
func CleanSSHConfigFile(path string) error {
	if path == "" {
		p, err := DefaultSSHConfigPath()
		if err != nil {
			return err
		}
		path = p
	}
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read ssh config: %w", err)
	}

	updated, err := RemoveSSHConfigManagedBlock(existing)
	if err != nil {
		return fmt.Errorf("remove ssh config block: %w", err)
	}

	if len(bytes.TrimSpace(updated)) == 0 {
		_ = os.Remove(path)
		return nil
	}

	return AtomicWriteFile(path, updated, 0600)
}

// UpdateGHHostsContent 解析 hosts.yml 的 YAML 结构，更新或追加指定 Host 的凭据配置，并完整保留其他主机配置。
func UpdateGHHostsContent(existing []byte, host, user, token, gitProtocol string) ([]byte, error) {
	if host == "" {
		host = "github.com"
	}
	if gitProtocol == "" {
		gitProtocol = "ssh"
	}

	lines := strings.Split(string(existing), "\n")
	var resultLines []string
	inTargetHost := false
	hostReplaced := false

	hostHeader := host + ":"

	newHostBlock := []string{
		hostHeader,
		"    user: " + user,
		"    oauth_token: " + token,
		"    git_protocol: " + gitProtocol,
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, hostHeader) || trimmed == hostHeader {
			inTargetHost = true
			if !hostReplaced {
				resultLines = append(resultLines, newHostBlock...)
				hostReplaced = true
			}
			continue
		}

		if inTargetHost {
			// 在旧的目标 host 块内，跳过其缩进属性行
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || trimmed == "" {
				continue
			}
			// 遇到下一个顶级 key
			inTargetHost = false
		}

		if trimmed != "" || len(resultLines) > 0 {
			resultLines = append(resultLines, line)
		}
	}

	if !hostReplaced {
		if len(resultLines) > 0 && resultLines[len(resultLines)-1] != "" {
			resultLines = append(resultLines, "")
		}
		resultLines = append(resultLines, newHostBlock...)
	}

	output := strings.TrimSpace(strings.Join(resultLines, "\n"))
	return []byte(output + "\n"), nil
}

// RemoveGHHostsContent 从 hosts.yml 文本中移除指定主机的配置块。
func RemoveGHHostsContent(existing []byte, host string) ([]byte, error) {

	if host == "" {
		host = "github.com"
	}
	hostHeader := host + ":"
	lines := strings.Split(string(existing), "\n")
	var resultLines []string
	inTargetHost := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, hostHeader) || trimmed == hostHeader {
			inTargetHost = true
			continue
		}
		if inTargetHost {
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || trimmed == "" {
				continue
			}
			inTargetHost = false
		}
		resultLines = append(resultLines, line)
	}

	output := strings.TrimSpace(strings.Join(resultLines, "\n"))
	if output == "" {
		return []byte{}, nil
	}
	return []byte(output + "\n"), nil
}

// ApplyGHHostsFile 原子更新 ~/.config/gh/hosts.yml 配置文件。
func ApplyGHHostsFile(path, host, user, token, gitProtocol string) error {
	if path == "" {
		p, err := DefaultGitHubHostsPath()
		if err != nil {
			return err
		}
		path = p
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read hosts.yml: %w", err)
	}

	updated, err := UpdateGHHostsContent(existing, host, user, token, gitProtocol)
	if err != nil {
		return fmt.Errorf("update hosts.yml content: %w", err)
	}

	return AtomicWriteFile(path, updated, 0600)
}

// CleanGHHostsFile 从 ~/.config/gh/hosts.yml 中原子删除指定主机的凭据条目。
func CleanGHHostsFile(path, host string) error {
	if path == "" {
		p, err := DefaultGitHubHostsPath()
		if err != nil {
			return err
		}
		path = p
	}
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read hosts.yml: %w", err)
	}

	updated, err := RemoveGHHostsContent(existing, host)
	if err != nil {
		return fmt.Errorf("remove host from hosts.yml: %w", err)
	}

	if len(bytes.TrimSpace(updated)) == 0 {
		_ = os.Remove(path)
		return nil
	}

	return AtomicWriteFile(path, updated, 0600)
}

// AtomicWriteFile 将数据写入同目录下的临时文件并原子替换至目标文件路径，支持 Windows ACL 安全权限。
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if runtime.GOOS == "windows" {
		_ = applyWindowsACL(tmpName)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename to target path: %w", err)
	}
	ok = true
	return nil
}

func applyWindowsACL(path string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	user := os.Getenv("USERNAME")
	var cmd *exec.Cmd
	if user == "" || strings.HasSuffix(user, "$") || strings.EqualFold(user, "SYSTEM") || strings.EqualFold(user, "Administrator") {
		cmd = exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", "SYSTEM:F", "Administrators:F")
	} else {
		cmd = exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", user+":F", "SYSTEM:F", "Administrators:F")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set Windows ACL: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}
