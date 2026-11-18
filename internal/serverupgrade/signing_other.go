//go:build !darwin

package serverupgrade

import (
	"context"
	"fmt"
)

type otherSigner struct{}

func newOtherSigner() *otherSigner {
	return &otherSigner{}
}

func (s *otherSigner) Preflight(ctx context.Context) error {
	return fmt.Errorf("code signing not supported on this platform")
}

func (s *otherSigner) Sign(ctx context.Context, targetPath string) error {
	return fmt.Errorf("code signing not supported on this platform")
}

func (s *otherSigner) VerifyCandidate(ctx context.Context, targetPath string) error {
	return fmt.Errorf("code signing not supported on this platform")
}

func (s *otherSigner) ReadDesignatedRequirement(ctx context.Context, targetPath string) (string, bool, error) {
	return "", false, fmt.Errorf("code signing not supported on this platform")
}

func (s *otherSigner) VerifyRequirement(ctx context.Context, targetPath, requirement string) error {
	return fmt.Errorf("code signing not supported on this platform")
}

func newSigner(cfg SignerConfig) Signer {
	return newOtherSigner()
}
