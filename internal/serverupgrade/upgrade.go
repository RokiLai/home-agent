// Package serverupgrade implements in-place self-upgrade for homeagent-server.
package serverupgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"homeagent/internal/githubrelease"
	"homeagent/internal/version"
)

// Options 定义服务端自升级参数。
type Options struct {
	TargetVersion   string                  // 目标版本号（例如 "v0.7.0"），若为空则自动获取最新版本
	URL             string                  // 可选显式下载 URL
	SHA256          string                  // 可选显式 SHA256
	Repo            string                  // GitHub 仓库（默认 "RokiLai/home-agent"）
	MirrorPrefix    string                  // 可选 GitHub 加速镜像前缀
	Force           bool                    // 是否强制覆盖相同版本
	ExecutablePath  string                  // 自身可执行文件路径（默认 os.Executable()）
	SkipSmoke       bool                    // 是否跳过冒烟测试
	RestartCallback func() error            // 重启回调钩子
	Client          *githubrelease.Client   // GitHub 客户端
	HTTPClient      *http.Client            // HTTP 客户端
}

// Result 定义自升级执行结果。
type Result struct {
	PreviousVersion string `json:"previous_version"`
	TargetVersion   string `json:"target_version"`
	Updated         bool   `json:"updated"`
	BinaryPath      string `json:"binary_path"`
}

// PerformServerSelfUpgrade 执行服务端就地自升级。
func PerformServerSelfUpgrade(ctx context.Context, opts Options) (Result, error) {
	currentVer := version.Get()

	ghClient := opts.Client
	if ghClient == nil {
		ghClient = githubrelease.NewClient(githubrelease.Config{
			Repo:         opts.Repo,
			MirrorPrefix: opts.MirrorPrefix,
			HTTPClient:   opts.HTTPClient,
		})
	}

	targetVer := strings.TrimSpace(opts.TargetVersion)
	if targetVer == "" {
		rel, err := ghClient.GetLatestRelease(ctx, true)
		if err != nil {
			return Result{}, fmt.Errorf("query latest release: %w", err)
		}
		targetVer = rel.TagName
	}

	if !opts.Force && currentVer == targetVer {
		return Result{
			PreviousVersion: currentVer,
			TargetVersion:   targetVer,
			Updated:         false,
		}, nil
	}

	// 1. 确定自身可执行文件路径
	exePath := opts.ExecutablePath
	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			return Result{}, fmt.Errorf("resolve current executable: %w", err)
		}
	}
	exePath, err := filepath.Abs(exePath)
	if err != nil {
		return Result{}, fmt.Errorf("resolve executable absolute path: %w", err)
	}

	// 2. 构造资产名称与下载 URL / SHA256
	osName := runtime.GOOS
	archName := runtime.GOARCH
	binaryName := fmt.Sprintf("homeagent-server-%s-%s", osName, archName)
	if osName == "windows" {
		binaryName = fmt.Sprintf("homeagent-server-windows-%s.exe", archName)
	}

	downloadURL := opts.URL
	expectedSHA := strings.ToLower(strings.TrimSpace(opts.SHA256))

	if downloadURL == "" {
		downloadURL = ghClient.BuildAssetDownloadURL(targetVer, binaryName)
	}
	if expectedSHA == "" {
		var err error
		expectedSHA, err = ghClient.FetchAssetSHA256(ctx, targetVer, binaryName)
		if err != nil {
			return Result{}, fmt.Errorf("resolve sha256 for %s: %w", binaryName, err)
		}
	}

	// 3. 下载新二进制到同目录临时文件
	dir := filepath.Dir(exePath)
	tmpFile, err := os.CreateTemp(dir, "homeagent-server-upgrade-*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("create temp upgrade file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		// 若未成功替换，自动清理临时文件
		_ = os.Remove(tmpPath)
	}()

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		_ = tmpFile.Close()
		return Result{}, fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("User-Agent", "HomeAgent-Server")

	resp, err := httpClient.Do(req)
	if err != nil {
		_ = tmpFile.Close()
		return Result{}, fmt.Errorf("download server binary from %s: %w", downloadURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_ = tmpFile.Close()
		return Result{}, fmt.Errorf("download server binary failed with status %d from %s", resp.StatusCode, downloadURL)
	}

	hasher := sha256.New()
	multiWriter := io.MultiWriter(tmpFile, hasher)
	if _, err := io.Copy(multiWriter, resp.Body); err != nil {
		_ = tmpFile.Close()
		return Result{}, fmt.Errorf("write binary content: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return Result{}, fmt.Errorf("sync temp file: %w", err)
	}
	_ = tmpFile.Close()

	// 4. 校验 SHA256
	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if actualSHA != expectedSHA {
		return Result{}, fmt.Errorf("sha256 mismatch: expected %s, got %s", expectedSHA, actualSHA)
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		return Result{}, fmt.Errorf("chmod executable: %w", err)
	}

	// 5. 冒烟自检
	if !opts.SkipSmoke {
		smokeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		cmd := exec.CommandContext(smokeCtx, tmpPath, "version")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			return Result{}, fmt.Errorf("smoke preflight failed: %w, output: %s", err, out.String())
		}
		if !strings.Contains(out.String(), "homeagent-server") {
			return Result{}, fmt.Errorf("smoke preflight output invalid: %s", out.String())
		}
	}

	// 6. 备份当前旧版本
	bakPath := exePath + ".bak"
	_ = os.Remove(bakPath)
	if err := copyFile(exePath, bakPath); err != nil {
		// 备份失败不阻断，继续尝试原子替换
		_ = os.Remove(bakPath)
	}

	// 7. 原子替换自身可执行文件
	if err := atomicReplace(tmpPath, exePath); err != nil {
		return Result{}, fmt.Errorf("atomic replace executable failed: %w", err)
	}

	// 8. 触发重启回调
	if opts.RestartCallback != nil {
		go func() {
			time.Sleep(500 * time.Millisecond)
			_ = opts.RestartCallback()
		}()
	}

	return Result{
		PreviousVersion: currentVer,
		TargetVersion:   targetVer,
		Updated:         true,
		BinaryPath:      exePath,
	}, nil
}

func atomicReplace(src, dst string) error {
	if runtime.GOOS == "windows" {
		oldDst := dst + ".old." + fmt.Sprintf("%d", time.Now().UnixNano())
		_ = os.Rename(dst, oldDst)
		if err := os.Rename(src, dst); err != nil {
			_ = os.Rename(oldDst, dst)
			return err
		}
		_ = os.Remove(oldDst)
		return nil
	}
	return os.Rename(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
