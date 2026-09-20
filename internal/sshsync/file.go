package sshsync

import (
	"fmt"
	"os"
	"path/filepath"
)

// ApplyManagedFile 将 KeySet 安全写入 authorized_keys，并返回是否发生内容变化。
// 该适配器不跟随符号链接，临时文件与目标位于同一目录，替换使用 os.Rename。
func ApplyManagedFile(path string, keys []Key) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
		info = nil
	}
	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("refuse to follow symlink: %s", path)
	}
	var original []byte
	if info != nil {
		original, err = os.ReadFile(path)
		if err != nil {
			return false, err
		}
	}
	updated, err := UpdateManagedBlock(original, keys)
	if err != nil {
		return false, err
	}
	if string(updated) == string(original) {
		return false, nil
	}
	mode := os.FileMode(0600)
	if info != nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".authorized-keys-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(updated)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	if info != nil {
		backup := path + ".homeagent.bak"
		if err := os.WriteFile(backup, original, mode); err != nil {
			return false, err
		}
		if err := os.Chmod(backup, mode); err != nil {
			return false, err
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	return true, nil
}
