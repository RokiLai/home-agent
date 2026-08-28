// Package alerting 提供告警通道抽象接口。
package alerting

import (
	"context"
	"time"
)

// Channel 定义可插拔的告警投递通道契约。
type Channel interface {
	ID() string
	Type() string
	Deliver(ctx context.Context, notification Notification) DeliveryResult
}

// DeliveryResult 封装单次通道投递的结构化结果。
type DeliveryResult struct {
	ProviderMessageID string
	Retryable         bool
	RetryAfter        time.Duration
	StatusCode        int
	ErrorCode         string
	ErrorMessage      string
}

