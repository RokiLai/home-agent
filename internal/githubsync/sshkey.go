package githubsync

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	DefaultGitHubKeyFilename = "homeagent_github_id_ed25519"
)

// DefaultSSHDir 返回当前用户的标准 ~/.ssh 目录路径。
func DefaultSSHDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return filepath.Join(home, ".ssh"), nil
}

// ComputeFingerprint 计算 OpenSSH 公钥的 SHA256 指纹（格式遵循 OpenSSH 规范："SHA256:<base64-without-padding>"）。
func ComputeFingerprint(pubKeyStr string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(pubKeyStr))
	if len(fields) < 2 {
		return "", errors.New("invalid public key format: expected at least key-type and base64-data")
	}
	rawBytes, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", fmt.Errorf("decode public key base64: %w", err)
	}
	sum := sha256.Sum256(rawBytes)
	fp := "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
	return fp, nil
}

// EnsureEd25519KeyPair 检查指定私钥路径是否存在 Ed25519 密钥对；若不存在则自动创建（私钥 0600，公钥 0644）。
// 返回公钥文本与 SHA256 指纹。
func EnsureEd25519KeyPair(privatePath, comment string) (publicKeyStr string, fingerprint string, created bool, err error) {

	if strings.TrimSpace(privatePath) == "" {
		sshDir, err := DefaultSSHDir()
		if err != nil {
			return "", "", false, err
		}
		privatePath = filepath.Join(sshDir, DefaultGitHubKeyFilename)
	}
	publicPath := privatePath + ".pub"

	// Check if already exists
	if pubBytes, err := os.ReadFile(publicPath); err == nil && len(pubBytes) > 0 {
		pubStr := strings.TrimSpace(string(pubBytes))
		fp, err := ComputeFingerprint(pubStr)
		if err == nil {
			return pubStr, fp, false, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(privatePath), 0700); err != nil {
		return "", "", false, fmt.Errorf("create ssh directory: %w", err)
	}

	if comment == "" {
		comment = "homeagent-github"
	}

	// First try using ssh-keygen CLI if present
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-f", privatePath, "-N", "", "-C", comment)
	if out, err := cmd.CombinedOutput(); err == nil {
		_ = os.Chmod(privatePath, 0600)
		_ = os.Chmod(publicPath, 0644)
		pubBytes, err := os.ReadFile(publicPath)
		if err != nil {
			return "", "", false, fmt.Errorf("read generated public key: %w", err)
		}
		pubStr := strings.TrimSpace(string(pubBytes))
		fp, err := ComputeFingerprint(pubStr)
		if err != nil {
			return "", "", false, err
		}
		return pubStr, fp, true, nil
	} else {
		// Fallback to pure Go Ed25519 key generation
		pubKey, privKey, genErr := ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return "", "", false, fmt.Errorf("generate ed25519 key: %w (ssh-keygen failed: %s)", genErr, bytes.TrimSpace(out))
		}

		pubStr := formatOpenSSHEd25519PublicKey(pubKey, comment)
		privPEM := formatOpenSSHEd25519PrivateKey(privKey)

		if err := os.WriteFile(privatePath, privPEM, 0600); err != nil {
			return "", "", false, fmt.Errorf("write private key: %w", err)
		}
		if err := os.WriteFile(publicPath, []byte(pubStr+"\n"), 0644); err != nil {
			return "", "", false, fmt.Errorf("write public key: %w", err)
		}

		fp, err := ComputeFingerprint(pubStr)
		if err != nil {
			return "", "", false, err
		}
		return pubStr, fp, true, nil
	}
}

// RemoveEd25519KeyPair removes the private and public key files if they exist.
func RemoveEd25519KeyPair(privatePath string) error {
	if privatePath == "" {
		sshDir, err := DefaultSSHDir()
		if err != nil {
			return err
		}
		privatePath = filepath.Join(sshDir, DefaultGitHubKeyFilename)
	}
	publicPath := privatePath + ".pub"

	_ = os.Remove(privatePath)
	_ = os.Remove(publicPath)
	return nil
}

func formatOpenSSHEd25519PublicKey(pubKey ed25519.PublicKey, comment string) string {
	// OpenSSH wire format: string "ssh-ed25519" + string key_bytes
	var buf bytes.Buffer
	writeSSHString(&buf, "ssh-ed25519")
	writeSSHBytes(&buf, pubKey)
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	if comment != "" {
		return fmt.Sprintf("ssh-ed25519 %s %s", b64, comment)
	}
	return fmt.Sprintf("ssh-ed25519 %s", b64)
}

func formatOpenSSHEd25519PrivateKey(privKey ed25519.PrivateKey) []byte {
	// Standard OpenSSL PKCS#8 or OpenSSH PEM representation
	block := &pem.Block{
		Type:  "OPENSSH PRIVATE KEY",
		Bytes: buildOpenSSHPrivateKeyBlob(privKey),
	}
	return pem.EncodeToMemory(block)
}

func buildOpenSSHPrivateKeyBlob(privKey ed25519.PrivateKey) []byte {
	var buf bytes.Buffer
	buf.WriteString("openssh-key-v1\x00")
	writeSSHString(&buf, "none")  // cipher
	writeSSHString(&buf, "none")  // kdfname
	writeSSHString(&buf, "")      // kdfoptions
	buf.Write([]byte{0, 0, 0, 1}) // number of keys (1)

	// Public key blob
	var pubBuf bytes.Buffer
	writeSSHString(&pubBuf, "ssh-ed25519")
	writeSSHBytes(&pubBuf, privKey[32:])
	writeSSHBytes(&buf, pubBuf.Bytes())

	// Private key blob
	var privBuf bytes.Buffer
	checkInt := uint32(0x12345678)
	privBuf.Write([]byte{byte(checkInt >> 24), byte(checkInt >> 16), byte(checkInt >> 8), byte(checkInt)})
	privBuf.Write([]byte{byte(checkInt >> 24), byte(checkInt >> 16), byte(checkInt >> 8), byte(checkInt)})
	writeSSHString(&privBuf, "ssh-ed25519")
	writeSSHBytes(&privBuf, privKey[32:])
	writeSSHBytes(&privBuf, privKey)
	writeSSHString(&privBuf, "homeagent-github")

	// Padding to multiple of block size (8 bytes for 'none')
	padLen := 8 - (privBuf.Len() % 8)
	if padLen == 8 {
		padLen = 0
	}
	for i := 1; i <= padLen; i++ {
		privBuf.WriteByte(byte(i))
	}

	writeSSHBytes(&buf, privBuf.Bytes())
	return buf.Bytes()
}

func writeSSHString(buf *bytes.Buffer, s string) {
	writeSSHBytes(buf, []byte(s))
}

func writeSSHBytes(buf *bytes.Buffer, b []byte) {
	length := uint32(len(b))
	buf.Write([]byte{byte(length >> 24), byte(length >> 16), byte(length >> 8), byte(length)})
	buf.Write(b)
}

// DefaultGitHubHostsPath 返回 Unix/macOS 或 Windows 系统上的标准 ~/.config/gh/hosts.yml 文件路径。
func DefaultGitHubHostsPath() (string, error) {
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "GitHub CLI", "hosts.yml"), nil
	}
	// Unix / macOS
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "gh", "hosts.yml"), nil
}

// DefaultSSHConfigPath 返回当前用户的 ~/.ssh/config 路径。
func DefaultSSHConfigPath() (string, error) {

	sshDir, err := DefaultSSHDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(sshDir, "config"), nil
}
