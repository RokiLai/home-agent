package wol

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseAndValidateMAC(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantNorm    string
		expectError bool
		errContains string
	}{
		{
			name:     "standard colon lowercase",
			input:    "02:00:00:11:22:33",
			wantNorm: "02:00:00:11:22:33",
		},
		{
			name:     "standard colon uppercase",
			input:    "02:00:00:11:22:33",
			wantNorm: "02:00:00:11:22:33",
		},
		{
			name:     "hyphen separated",
			input:    "02-00-00-11-22-33",
			wantNorm: "02:00:00:11:22:33",
		},
		{
			name:     "cisco dot notation",
			input:    "0200.0011.2233",
			wantNorm: "02:00:00:11:22:33",
		},
		{
			name:     "with surrounding whitespace",
			input:    "  02:11:22:33:44:55  ",
			wantNorm: "02:11:22:33:44:55",
		},
		{
			name:        "empty MAC",
			input:       "",
			expectError: true,
			errContains: "empty",
		},
		{
			name:        "invalid hex string",
			input:       "zz:00:00:11:22:33",
			expectError: true,
			errContains: "invalid MAC",
		},
		{
			name:        "EUI-64 (8 bytes)",
			input:       "00:11:22:33:44:55:66:77",
			expectError: true,
			errContains: "6 bytes",
		},
		{
			name:        "all zeros",
			input:       "00:00:00:00:00:00",
			expectError: true,
			errContains: "all zeros",
		},
		{
			name:        "broadcast address",
			input:       "ff:ff:ff:ff:ff:ff",
			expectError: true,
			errContains: "broadcast address",
		},
		{
			name:        "multicast address IPv4 (01:00:5e:...)",
			input:       "01:00:5e:00:00:01",
			expectError: true,
			errContains: "multicast address",
		},
		{
			name:        "multicast address IPv6 (33:33:...)",
			input:       "33:33:00:00:00:01",
			expectError: true,
			errContains: "multicast address",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, norm, err := ParseAndValidateMAC(tc.input)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errContains)
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if norm != tc.wantNorm {
					t.Fatalf("expected normalized MAC %q, got %q", tc.wantNorm, norm)
				}
			}
		})
	}
}

func TestBuildMagicPacket(t *testing.T) {
	macStr := "02:00:00:11:22:33"
	expectedBytes := []byte{0x02, 0x00, 0x00, 0x11, 0x22, 0x33}

	packet, err := BuildMagicPacket(macStr)
	if err != nil {
		t.Fatalf("BuildMagicPacket failed: %v", err)
	}

	if len(packet) != 102 {
		t.Fatalf("expected packet length 102, got %d", len(packet))
	}

	// Verify first 6 bytes are 0xFF
	for i := 0; i < 6; i++ {
		if packet[i] != 0xFF {
			t.Fatalf("byte %d is not 0xFF, got 0x%02X", i, packet[i])
		}
	}

	// Verify 16 repetitions of MAC
	for i := 0; i < 16; i++ {
		chunk := packet[6+i*6 : 6+(i+1)*6]
		if !bytes.Equal(chunk, expectedBytes) {
			t.Fatalf("chunk %d does not match MAC: got %v, expected %v", i, chunk, expectedBytes)
		}
	}
}

func TestInferSubnetBroadcast(t *testing.T) {
	ips := []string{"192.168.50.10", "10.0.1.20", "127.0.0.1", "2001:db8::invalid"}
	res := InferSubnetBroadcast(ips, 9)

	expected := []string{"192.168.50.255:9", "10.0.1.255:9"}
	if len(res) != len(expected) {
		t.Fatalf("expected %d broadcast addrs, got %d (%v)", len(expected), len(res), res)
	}
	for i, exp := range expected {
		if res[i] != exp {
			t.Errorf("res[%d] = %q, want %q", i, res[i], exp)
		}
	}
}

func TestWakeWithLocalListener(t *testing.T) {
	// Listen on local UDP port
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Skipf("cannot bind local UDP port: %v", err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().String()
	macStr := "02:00:00:11:22:33"

	done := make(chan []byte, 3)
	go func() {
		buf := make([]byte, 256)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			data := make([]byte, n)
			copy(data, buf[:n])
			done <- data
		}
	}()

	opts := &Options{
		BroadcastAddrs: []string{localAddr},
		BurstCount:     2,
		BurstInterval:  20 * time.Millisecond,
	}

	if err := Wake(macStr, opts); err != nil {
		t.Fatalf("Wake failed: %v", err)
	}

	select {
	case received := <-done:
		if len(received) != 102 {
			t.Fatalf("expected 102 bytes received, got %d", len(received))
		}
		expectedPacket, _ := BuildMagicPacket(macStr)
		if !bytes.Equal(received, expectedPacket[:]) {
			t.Fatalf("received packet does not match expected WOL packet")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for packet on local UDP listener")
	}
}
