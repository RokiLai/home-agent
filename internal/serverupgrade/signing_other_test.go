//go:build !darwin

package serverupgrade

import (
	"context"
	"testing"
)

func TestOtherSignerRejects(t *testing.T) {
	s := newOtherSigner()
	ctx := context.Background()

	if err := s.Preflight(ctx); err == nil {
		t.Fatalf("expected preflight to fail on non-darwin")
	}
	if err := s.Sign(ctx, "some-path"); err == nil {
		t.Fatalf("expected sign to fail on non-darwin")
	}
}
