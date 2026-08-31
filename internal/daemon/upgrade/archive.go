package upgrade

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxArchiveCompressedBytes   = 256 * 1024 * 1024 // 256 MiB
	MaxArchiveUncompressedBytes = 512 * 1024 * 1024 // 512 MiB
	MaxArchiveFiles             = 20000             // 20,000 files
	MaxArchiveSingleFileBytes   = 128 * 1024 * 1024 // 128 MiB
	MaxPathLength               = 1024              // 1024 bytes UTF-8
)

// CanonicalMode 计算文件的规范权限模式（目录固定 0755，普通文件若含执行位则为 0755，否则 0644）。
func CanonicalMode(isDir bool, origMode os.FileMode) os.FileMode {
	if isDir {
		return 0755
	}
	if origMode&0111 != 0 {
		return 0755
	}
	return 0644
}

type bundleItem struct {
	relPath  string
	isDir    bool
	mode     os.FileMode
	size     int64
	fullPath string
}

// ComputeBundleDigest 按照规范解包视图计算 App Bundle 的全局唯一 SHA256 摘要。
func ComputeBundleDigest(appDir string) (string, error) {
	appDir = filepath.Clean(appDir)
	baseName := filepath.Base(appDir)
	if baseName != "HomeAgent.app" {
		return "", fmt.Errorf("expected bundle directory name 'HomeAgent.app', got %q", baseName)
	}

	var items []bundleItem
	err := filepath.Walk(appDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(filepath.Dir(appDir), path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeDevice != 0 || info.Mode()&os.ModeNamedPipe != 0 || info.Mode()&os.ModeSocket != 0 {
			return fmt.Errorf("special file %q is forbidden in macos-app-archive-v2", rel)
		}

		isDir := info.IsDir()
		mode := CanonicalMode(isDir, info.Mode())
		size := int64(0)
		if !isDir {
			size = info.Size()
		}

		items = append(items, bundleItem{
			relPath:  rel,
			isDir:    isDir,
			mode:     mode,
			size:     size,
			fullPath: path,
		})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk bundle dir: %w", err)
	}

	// 路径按规范 UTF-8 字节升序排序
	sort.Slice(items, func(i, j int) bool {
		return items[i].relPath < items[j].relPath
	})

	hasher := sha256.New()
	for _, item := range items {
		// 1 byte type (0x01 directory, 0x02 regular file)
		if item.isDir {
			hasher.Write([]byte{0x01})
		} else {
			hasher.Write([]byte{0x02})
		}

		// 4 bytes path length + relPath
		pathBytes := []byte(item.relPath)
		lenBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(lenBuf, uint32(len(pathBytes)))
		hasher.Write(lenBuf)
		hasher.Write(pathBytes)

		// 4 bytes mode
		modeBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(modeBuf, uint32(item.mode.Perm()))
		hasher.Write(modeBuf)

		// 8 bytes size
		sizeBuf := make([]byte, 8)
		binary.BigEndian.PutUint64(sizeBuf, uint64(item.size))
		hasher.Write(sizeBuf)

		// file content
		if !item.isDir {
			f, err := os.Open(item.fullPath)
			if err != nil {
				return "", fmt.Errorf("open %s for digest: %w", item.fullPath, err)
			}
			if _, err := io.Copy(hasher, f); err != nil {
				_ = f.Close()
				return "", fmt.Errorf("read %s for digest: %w", item.fullPath, err)
			}
			_ = f.Close()
		}
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// PackAppArchive 将已完成签名的 HomeAgent.app 打包为 macos-app-archive-v2 规范 ZIP 并返回 Bundle 摘要。
func PackAppArchive(appDir string, destZipPath string) (string, error) {
	digest, err := ComputeBundleDigest(appDir)
	if err != nil {
		return "", fmt.Errorf("compute bundle digest: %w", err)
	}

	appDir = filepath.Clean(appDir)
	parentDir := filepath.Dir(appDir)

	zipFile, err := os.Create(destZipPath)
	if err != nil {
		return "", fmt.Errorf("create zip archive: %w", err)
	}
	defer zipFile.Close()

	w := zip.NewWriter(zipFile)
	defer w.Close()

	err = filepath.Walk(appDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(parentDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if info.IsDir() {
			if !strings.HasSuffix(rel, "/") {
				rel += "/"
			}
		}

		fh, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		fh.Name = rel
		fh.Method = zip.Deflate

		mode := CanonicalMode(info.IsDir(), info.Mode())
		fh.SetMode(mode)

		writer, err := w.CreateHeader(fh)
		if err != nil {
			return err
		}

		if !info.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(writer, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("pack zip: %w", err)
	}

	if err := w.Close(); err != nil {
		return "", fmt.Errorf("close zip writer: %w", err)
	}

	return digest, nil
}

// UnpackAppArchive 安全解包 macos-app-archive-v2 ZIP 文件并进行严格完整性与沙箱校验。
func UnpackAppArchive(zipPath string, destDir string, expectedBundleDigest string) error {
	info, err := os.Stat(zipPath)
	if err != nil {
		return fmt.Errorf("stat archive: %w", err)
	}
	if info.Size() > MaxArchiveCompressedBytes {
		return fmt.Errorf("archive size %d exceeds maximum allowed %d bytes", info.Size(), MaxArchiveCompressedBytes)
	}

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer r.Close()

	if len(r.File) > MaxArchiveFiles {
		return fmt.Errorf("archive file count %d exceeds maximum %d", len(r.File), MaxArchiveFiles)
	}

	var totalUncompressed uint64
	caseFoldSeen := make(map[string]bool)

	for _, f := range r.File {
		name := f.Name
		if len(name) > MaxPathLength {
			return fmt.Errorf("path length %d exceeds maximum %d", len(name), MaxPathLength)
		}
		if !utf8.ValidString(name) {
			return fmt.Errorf("path %q is not valid UTF-8", name)
		}
		if strings.Contains(name, "\x00") || strings.Contains(name, "\\") {
			return fmt.Errorf("path %q contains invalid characters", name)
		}
		if strings.HasPrefix(name, "/") || strings.Contains(name, "../") || strings.Contains(name, "/..") {
			return fmt.Errorf("path %q contains directory traversal", name)
		}

		cleanPath := filepath.Clean(filepath.FromSlash(name))
		if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
			return fmt.Errorf("cleaned path %q is outside destination", cleanPath)
		}

		// 检查 APFS 大小写折叠重复
		folded := strings.ToLower(cleanPath)
		if caseFoldSeen[folded] {
			return fmt.Errorf("duplicate path after case-folding: %q", cleanPath)
		}
		caseFoldSeen[folded] = true

		// 唯一顶层目录校验：必须以 HomeAgent.app/ 开头
		topPart := strings.Split(filepath.ToSlash(cleanPath), "/")[0]
		if topPart != "HomeAgent.app" {
			return fmt.Errorf("archive top-level element %q must be 'HomeAgent.app'", topPart)
		}

		// 禁止 AppleDouble 资源分支及元数据文件
		base := filepath.Base(cleanPath)
		if strings.HasPrefix(base, "._") || strings.Contains(name, "__MACOSX") {
			return fmt.Errorf("archive contains forbidden macOS metadata file %q", name)
		}

		if f.UncompressedSize64 > MaxArchiveSingleFileBytes {
			return fmt.Errorf("file %q uncompressed size %d exceeds %d bytes", name, f.UncompressedSize64, MaxArchiveSingleFileBytes)
		}
		totalUncompressed += f.UncompressedSize64
		if totalUncompressed > MaxArchiveUncompressedBytes {
			return fmt.Errorf("total uncompressed size %d exceeds maximum %d bytes", totalUncompressed, MaxArchiveUncompressedBytes)
		}
	}

	// 执行解包写入
	for _, f := range r.File {
		targetPath := filepath.Join(destDir, filepath.FromSlash(f.Name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", targetPath, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return fmt.Errorf("mkdir parent %s: %w", filepath.Dir(targetPath), err)
		}

		mode := CanonicalMode(false, f.Mode())
		outFile, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return fmt.Errorf("create file %s: %w", targetPath, err)
		}

		rc, err := f.Open()
		if err != nil {
			_ = outFile.Close()
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}

		written, err := io.Copy(outFile, rc)
		_ = rc.Close()
		if err != nil {
			_ = outFile.Close()
			return fmt.Errorf("write %s: %w", targetPath, err)
		}
		if uint64(written) != f.UncompressedSize64 {
			_ = outFile.Close()
			return fmt.Errorf("file %s size mismatch: wrote %d, expected %d", f.Name, written, f.UncompressedSize64)
		}

		if err := outFile.Sync(); err != nil {
			_ = outFile.Close()
			return fmt.Errorf("sync %s: %w", targetPath, err)
		}
		_ = outFile.Close()

		if err := os.Chmod(targetPath, mode); err != nil {
			return fmt.Errorf("chmod %s: %w", targetPath, err)
		}
	}

	// 校验解包后的 Bundle 摘要
	extractedApp := filepath.Join(destDir, "HomeAgent.app")
	actualDigest, err := ComputeBundleDigest(extractedApp)
	if err != nil {
		return fmt.Errorf("compute extracted bundle digest: %w", err)
	}

	if expectedBundleDigest != "" && !strings.EqualFold(actualDigest, expectedBundleDigest) {
		return fmt.Errorf("bundle digest mismatch: expected %s, got %s", expectedBundleDigest, actualDigest)
	}

	return nil
}
