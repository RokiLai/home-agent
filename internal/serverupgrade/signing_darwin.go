//go:build darwin

package serverupgrade

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type darwinSigner struct {
	config SignerConfig
}

func newDarwinSigner(cfg SignerConfig) *darwinSigner {
	return &darwinSigner{config: cfg}
}

func (s *darwinSigner) Preflight(ctx context.Context) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "/usr/bin/security", "find-identity", "-v", "-p", "codesigning")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("preflight codesign identity check failed: %w", err)
	}

	targetFP := strings.ToUpper(s.config.Fingerprint)
	lines := strings.Split(stdout.String(), "\n")
	found := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 {
			candidateFP := strings.Trim(fields[1], "\"")
			if strings.EqualFold(candidateFP, targetFP) {
				found = true
				break
			}
		}
	}
	if !found {
		return fmt.Errorf("configured codesign fingerprint %s not found in valid codesigning identities", targetFP)
	}
	return nil
}

func (s *darwinSigner) Sign(ctx context.Context, targetPath string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx,
		"/usr/bin/codesign",
		"--force",
		"--sign", s.config.Fingerprint,
		"--identifier", s.config.Identifier,
		"--timestamp=none",
		targetPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("codesign execution failed: %w, output: %s", err, stderr.String())
	}
	return nil
}

func (s *darwinSigner) VerifyCandidate(ctx context.Context, targetPath string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// 1. 验证签名结构完整性
	verifyCmd := exec.CommandContext(cmdCtx, "/usr/bin/codesign", "-v", targetPath)
	var verifyErr bytes.Buffer
	verifyCmd.Stderr = &verifyErr
	if err := verifyCmd.Run(); err != nil {
		return fmt.Errorf("candidate signature verification failed: %w, output: %s", err, verifyErr.String())
	}

	// 2. 读取元信息确认非 adhoc，且 identifier 匹配
	infoCmd := exec.CommandContext(cmdCtx, "/usr/bin/codesign", "-d", "--verbose=4", targetPath)
	var infoErr bytes.Buffer
	infoCmd.Stderr = &infoErr
	if err := infoCmd.Run(); err != nil {
		return fmt.Errorf("candidate read signature info failed: %w, output: %s", err, infoErr.String())
	}
	output := infoErr.String()
	if strings.Contains(output, "Signature=adhoc") {
		return fmt.Errorf("candidate signature is adhoc; expected valid certificate signature")
	}
	expectedIdent := "Identifier=" + s.config.Identifier
	if !strings.Contains(output, expectedIdent) {
		return fmt.Errorf("candidate signature identifier mismatch: expected %s", expectedIdent)
	}

	// 3. 验证 leaf 证书指纹匹配
	expectedReq := fmt.Sprintf("identifier %q and certificate leaf = H%q", s.config.Identifier, strings.ToLower(s.config.Fingerprint))
	reqCmd := exec.CommandContext(cmdCtx, "/usr/bin/codesign", "-v", "-R="+expectedReq, targetPath)
	var reqErr bytes.Buffer
	reqCmd.Stderr = &reqErr
	if err := reqCmd.Run(); err != nil {
		return fmt.Errorf("candidate requirement check failed: %w, output: %s", err, reqErr.String())
	}

	return nil
}

func (s *darwinSigner) ReadDesignatedRequirement(ctx context.Context, targetPath string) (string, bool, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "/usr/bin/codesign", "-d", "-r-", "--verbose=4", targetPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("read designated requirement failed: %w, output: %s", err, stderr.String())
	}

	isAdhoc := strings.Contains(stderr.String(), "Signature=adhoc")
	outStr := stdout.String()
	var req string
	for _, line := range strings.Split(outStr, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# designated =>") {
			req = strings.TrimSpace(strings.TrimPrefix(trimmed, "# designated =>"))
			break
		}
		if strings.HasPrefix(trimmed, "designated =>") {
			req = strings.TrimSpace(strings.TrimPrefix(trimmed, "designated =>"))
			break
		}
	}
	return req, isAdhoc, nil
}

func (s *darwinSigner) VerifyRequirement(ctx context.Context, targetPath, requirement string) error {
	if strings.TrimSpace(requirement) == "" {
		return nil
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "/usr/bin/codesign", "-v", "-R="+requirement, targetPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("verify designated requirement failed: %w, output: %s", err, stderr.String())
	}
	return nil
}

func newSigner(cfg SignerConfig) Signer {
	return newDarwinSigner(cfg)
}
