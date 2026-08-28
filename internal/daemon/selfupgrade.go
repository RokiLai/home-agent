package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"homeagent/internal/version"
)

// UpgradeOptions 配置 Agent 目标二进制下载、校验、冒烟测试及重启回调。
type UpgradeOptions struct {
	TargetVersion   string
	URL             string
	SHA256          string
	Force           bool
	ExecutablePath  string
	HTTPClient      *http.Client
	SkipSmoke       bool
	SmokeSubcommand string
	RestartCallback func() error
	Logger          *slog.Logger
}

// UpgradeResult 记录自升级执行的结果与版本变化。
type UpgradeResult struct {
	PreviousVersion string `json:"previous_version"`
	TargetVersion   string `json:"target_version"`
	Updated         bool   `json:"updated"`
	Message         string `json:"message"`
}

// UpgradePayload 表示服务端通过 SSE 下发的 upgrade 升级指令载荷。
type UpgradePayload struct {
	Version       string `json:"version,omitempty"`
	TargetVersion string `json:"target_version,omitempty"`
	URL           string `json:"url"`
	SHA256        string `json:"sha256,omitempty"`
	Force         bool   `json:"force,omitempty"`
}

// PerformSelfUpgrade 执行完整的客户端自升级流程：流式下载目标二进制文件、SHA256 完整性校验、
// 启动前置冒烟测试（执行 info 子命令验证可运行性），并安全地原子替换当前正在运行的可执行文件。
func PerformSelfUpgrade(ctx context.Context, opts UpgradeOptions) (*UpgradeResult, error) {

	currentVer := version.Get()
	targetVer := strings.TrimSpace(opts.TargetVersion)
	url := strings.TrimSpace(opts.URL)
	expectedSHA := strings.ToLower(strings.TrimSpace(opts.SHA256))

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	if !opts.Force && targetVer != "" && targetVer == currentVer {
		log.Info("self_upgrade_skipped_already_latest", "current_version", currentVer, "target_version", targetVer)
		return &UpgradeResult{
			PreviousVersion: currentVer,
			TargetVersion:   targetVer,
			Updated:         false,
			Message:         fmt.Sprintf("already up to date (%s)", currentVer),
		}, nil
	}

	if url == "" {
		return nil, errors.New("upgrade URL is required")
	}

	execPath := opts.ExecutablePath
	if execPath == "" {
		p, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate running executable: %w", err)
		}
		realPath, err := filepath.EvalSymlinks(p)
		if err == nil {
			execPath = realPath
		} else {
			execPath = p
		}
	}

	binDir := filepath.Dir(execPath)
	if err := os.MkdirAll(binDir, 0755); err != nil {
		binDir = os.TempDir()
	}

	// 1. Download to temporary file
	tmpFile, err := os.CreateTemp(binDir, ".homeagent-agent-upgrade-*.tmp")
	if err != nil {
		// Fallback to os.TempDir if directory is not writable
		binDir = os.TempDir()
		tmpFile, err = os.CreateTemp(binDir, ".homeagent-agent-upgrade-*.tmp")
		if err != nil {
			return nil, fmt.Errorf("create temporary download file: %w", err)
		}
	}
	tmpPath := tmpFile.Name()
	cleanedUp := false
	cleanupTmp := func() {
		if !cleanedUp {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
			cleanedUp = true
		}
	}
	defer cleanupTmp()

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}

	log.Info("downloading_agent_binary", "url", url, "dest", tmpPath)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with HTTP %d", resp.StatusCode)
	}

	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)
	if _, err := io.Copy(writer, resp.Body); err != nil {
		return nil, fmt.Errorf("write downloaded binary: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return nil, fmt.Errorf("sync downloaded binary: %w", err)
	}
	_ = tmpFile.Close()

	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	log.Info("download_completed", "sha256", actualSHA)

	// 2. SHA256 integrity verification
	if expectedSHA != "" && !strings.EqualFold(actualSHA, expectedSHA) {
		return nil, fmt.Errorf("SHA256 checksum mismatch: expected %s, got %s", expectedSHA, actualSHA)
	}

	// 3. Set executable permissions
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, 0755); err != nil {
			return nil, fmt.Errorf("chmod binary: %w", err)
		}
	}

	// 4. Smoke preflight test
	if !opts.SkipSmoke {
		subcmd := opts.SmokeSubcommand
		if subcmd == "" {
			subcmd = "info"
		}
		smokeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		cmd := exec.CommandContext(smokeCtx, tmpPath, subcmd)
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("smoke preflight test (%s %s) failed: %w (output: %s)", tmpPath, subcmd, err, strings.TrimSpace(string(out)))
		}
		var candidate struct {
			AgentVersion string `json:"agent_version"`
			OS           string `json:"os"`
			Arch         string `json:"arch"`
		}
		if err := json.Unmarshal(out, &candidate); err != nil {
			return nil, fmt.Errorf("smoke preflight returned invalid agent info: %w", err)
		}
		candidate.AgentVersion = strings.TrimSpace(candidate.AgentVersion)
		candidate.OS = strings.ToLower(strings.TrimSpace(candidate.OS))
		candidate.Arch = strings.ToLower(strings.TrimSpace(candidate.Arch))
		if candidate.AgentVersion == "" || candidate.OS == "" || candidate.Arch == "" {
			return nil, errors.New("smoke preflight returned incomplete agent identity")
		}
		if targetVer != "" && candidate.AgentVersion != targetVer {
			return nil, fmt.Errorf("candidate version mismatch: got %s, want %s", candidate.AgentVersion, targetVer)
		}
		if candidate.OS != runtime.GOOS || candidate.Arch != runtime.GOARCH {
			return nil, fmt.Errorf("candidate platform mismatch: got %s/%s, want %s/%s", candidate.OS, candidate.Arch, runtime.GOOS, runtime.GOARCH)
		}
		log.Info("smoke_preflight_passed", "subcommand", subcmd)
	}

	// 5. Cross-platform atomic replacement
	oldPath := execPath + ".old"
	_ = os.Remove(oldPath) // clean previous leftover .old

	// If binary is in same filesystem/directory:
	if filepath.Dir(tmpPath) == filepath.Dir(execPath) {
		if err := atomicReplaceSameDir(execPath, tmpPath, oldPath); err != nil {
			return nil, err
		}
	} else {
		if err := atomicReplaceCrossDir(execPath, tmpPath, oldPath); err != nil {
			return nil, err
		}
	}

	cleanedUp = true // Don't remove tmpPath since it was renamed
	log.Info("agent_binary_atomically_replaced", "path", execPath)

	// 6. Trigger restart
	if opts.RestartCallback != nil {
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = opts.RestartCallback()
		}()
	}

	return &UpgradeResult{
		PreviousVersion: currentVer,
		TargetVersion:   targetVer,
		Updated:         true,
		Message:         fmt.Sprintf("successfully upgraded from %s to %s", currentVer, targetVer),
	}, nil
}

func atomicReplaceSameDir(targetPath, newPath, oldPath string) error {
	if _, err := os.Stat(targetPath); err == nil {
		if err := os.Rename(targetPath, oldPath); err != nil {
			return fmt.Errorf("backup existing executable to %s: %w", oldPath, err)
		}
	}

	if err := os.Rename(newPath, targetPath); err != nil {
		// Rollback
		if _, statErr := os.Stat(oldPath); statErr == nil {
			_ = os.Rename(oldPath, targetPath)
		}
		return fmt.Errorf("replace executable: %w", err)
	}

	// Apply Windows ACL if on Windows
	if runtime.GOOS == "windows" {
		_ = applyWindowsACL(targetPath)
	}

	// Apply macOS Ad-hoc codesign if on Darwin
	if runtime.GOOS == "darwin" {
		_ = applyDarwinCodesign(targetPath)
	}

	_ = os.Remove(oldPath)
	return nil
}

func applyDarwinCodesign(targetPath string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	// Check if targetPath is inside an App bundle (e.g. /Applications/HomeAgent.app/Contents/MacOS/homeagent-agent)
	signTarget := targetPath
	dir := filepath.Dir(targetPath)
	for dir != "/" && dir != "." {
		if strings.HasSuffix(dir, ".app") {
			signTarget = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	cmd := exec.Command("codesign", "-s", "-", "--force", "--deep", signTarget)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign %s: %w: %s", signTarget, err, bytes.TrimSpace(out))
	}
	return nil
}

func atomicReplaceCrossDir(targetPath, srcNewPath, oldPath string) error {
	destDir := filepath.Dir(targetPath)
	tempInDest, err := os.CreateTemp(destDir, ".homeagent-agent-staged-*.tmp")
	if err != nil {
		return fmt.Errorf("stage binary in destination directory: %w", err)
	}
	tempInDestPath := tempInDest.Name()
	defer func() {
		_ = tempInDest.Close()
		_ = os.Remove(tempInDestPath)
	}()

	srcFile, err := os.Open(srcNewPath)
	if err != nil {
		return fmt.Errorf("open downloaded binary: %w", err)
	}
	defer srcFile.Close()

	if _, err := io.Copy(tempInDest, srcFile); err != nil {
		return fmt.Errorf("copy staged binary: %w", err)
	}
	if err := tempInDest.Sync(); err != nil {
		return fmt.Errorf("sync staged binary: %w", err)
	}
	_ = tempInDest.Close()

	if runtime.GOOS != "windows" {
		_ = os.Chmod(tempInDestPath, 0755)
	}

	return atomicReplaceSameDir(targetPath, tempInDestPath, oldPath)
}
