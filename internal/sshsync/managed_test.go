package sshsync

import (
	"strings"
	"testing"
)

func TestManagedBlockPreservesUserKeysAndIsIdempotent(t *testing.T) {
	original := []byte("ssh-ed25519 PERSONAL mine\n")
	keys := []Key{{DeviceID: "a", PublicKey: "ssh-ed25519 AAAA old"}, {DeviceID: "duplicate", PublicKey: "ssh-ed25519 AAAA other"}, {DeviceID: "b", PublicKey: "ssh-ed25519 BBBB"}}
	one, err := UpdateManagedBlock(original, keys)
	if err != nil {
		t.Fatal(err)
	}
	two, err := UpdateManagedBlock(one, keys)
	if err != nil {
		t.Fatal(err)
	}
	if string(one) != string(two) {
		t.Fatalf("not idempotent:\n%s\n%s", one, two)
	}
	if !strings.Contains(string(one), "PERSONAL mine") || strings.Count(string(one), Begin) != 1 || strings.Count(string(one), "ssh-ed25519 AAAA") != 1 {
		t.Fatalf("bad output:\n%s", one)
	}
}

func TestManagedBlockRejectsMalformedInput(t *testing.T) {
	if _, err := UpdateManagedBlock([]byte(Begin+"\n"), nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestComputeKeySetHash(t *testing.T) {
	keys1 := []Key{{DeviceID: "a", PublicKey: "ssh-ed25519 AAAA"}, {DeviceID: "b", PublicKey: "ssh-ed25519 BBBB"}}
	keys2 := []Key{{DeviceID: "b", PublicKey: "ssh-ed25519 BBBB"}, {DeviceID: "a", PublicKey: "ssh-ed25519 AAAA"}}
	h1 := ComputeKeySetHash(keys1)
	h2 := ComputeKeySetHash(keys2)
	if h1 == "" || h1 != h2 {
		t.Fatalf("hashes should be deterministic: %q vs %q", h1, h2)
	}
}

