package main

import "testing"

func TestSelectLinuxDiskMountUsesWritableManagedRuntimeFilesystem(t *testing.T) {
	mountInfo := []byte(`24 1 0:22 / / ro,relatime - squashfs /dev/root ro
31 24 0:28 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw
42 24 0:39 / /data rw,relatime - ubifs ubi1_0 rw
`)

	mount, ok := selectLinuxDiskMount("/tmp/homeagent/homeagent-agent", mountInfo)
	if !ok || mount != "/tmp" {
		t.Fatalf("expected writable /tmp mount, got mount=%q ok=%v", mount, ok)
	}
}

func TestSelectLinuxDiskMountRejectsReadOnlyManagedRuntimeFilesystem(t *testing.T) {
	mountInfo := []byte(`24 1 0:22 / / ro,relatime - squashfs /dev/root ro
42 24 0:39 / /data rw,relatime - ubifs ubi1_0 rw
`)

	if mount, ok := selectLinuxDiskMount("/usr/sbin/homeagent-agent", mountInfo); ok {
		t.Fatalf("expected read-only managed runtime filesystem to be omitted, got %q", mount)
	}
}

func TestSelectLinuxDiskMountUsesDeepestMatchingWritableMount(t *testing.T) {
	mountInfo := []byte(`24 1 0:22 / / rw,relatime - ext4 /dev/root rw
42 24 0:39 / /opt rw,relatime - ext4 /dev/data rw
43 42 0:40 / /opt/homeagent rw,relatime - ext4 /dev/agent rw
`)

	mount, ok := selectLinuxDiskMount("/opt/homeagent/bin/homeagent-agent", mountInfo)
	if !ok || mount != "/opt/homeagent" {
		t.Fatalf("expected deepest managed mount, got mount=%q ok=%v", mount, ok)
	}
}
