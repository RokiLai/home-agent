package command

import (
	"errors"
	"time"
)

// DeliveryRepository 持久化投递记录，并以 revision 实现恢复过程的 CAS 保护。
type DeliveryRepository interface {
	Create(Delivery) error
	Get(string) (Delivery, error)
	Save(Delivery, uint64) error
	ListPending(string, time.Time, int) ([]Delivery, error)
}

// DeliveryStatus 是一次 Command 投递尝试的独立状态，不替代 Command.Status。
type DeliveryStatus string

const (
	DeliveryPending   DeliveryStatus = "pending"
	DeliveryLeased    DeliveryStatus = "leased"
	DeliverySent      DeliveryStatus = "sent"
	DeliveryAccepted  DeliveryStatus = "accepted"
	DeliveryCompleted DeliveryStatus = "completed"
	DeliveryExpired   DeliveryStatus = "expired"
	DeliveryCanceled  DeliveryStatus = "canceled"
)

var (
	ErrDeliveryExpired = errors.New("delivery expired")
	ErrDeliveryLease   = errors.New("delivery lease is not active")
	ErrDeliveryDevice  = errors.New("delivery device mismatch")
)

// Delivery 保存一次投递尝试的事实，所有时间由服务端记录。
type Delivery struct {
	ID          string         `json:"delivery_id"`
	CommandID   ID             `json:"command_id"`
	DeviceID    string         `json:"device_id"`
	Attempt     uint32         `json:"attempt"`
	Status      DeliveryStatus `json:"status"`
	LeaseUntil  *time.Time     `json:"lease_until,omitempty"`
	SentAt      *time.Time     `json:"sent_at,omitempty"`
	AcceptedAt  *time.Time     `json:"accepted_at,omitempty"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	LastEventID string         `json:"last_event_id,omitempty"`
	ErrorCode   string         `json:"error_code,omitempty"`
	Revision    uint64         `json:"revision"`
}

func (d Delivery) Terminal() bool {
	return d.Status == DeliveryCompleted || d.Status == DeliveryExpired || d.Status == DeliveryCanceled
}

// Lease 判断投递租约是否仍由当前服务端持有。
func (d Delivery) Lease(now time.Time) bool {
	return d.Status == DeliveryLeased && d.LeaseUntil != nil && d.LeaseUntil.After(now)
}

// CanSend 对任务终态、设备身份和 Delivery TTL 做统一的副作用前检查。
func CanSend(c Command, d Delivery, deviceID string, now time.Time) error {
	if d.DeviceID != deviceID || c.DeviceID != deviceID {
		return ErrDeliveryDevice
	}
	if c.Terminal() || d.Terminal() {
		if d.Status == DeliveryExpired {
			return ErrDeliveryExpired
		}
		return ErrDeliveryLease
	}
	if d.Status == DeliveryLeased && !d.Lease(now) {
		return ErrDeliveryExpired
	}
	return nil
}

// RenewLease 只允许未终态投递延长租约，调用方必须在持久化前做 CAS。
func (d Delivery) RenewLease(now time.Time, lease time.Duration) (Delivery, error) {
	if d.Terminal() || lease <= 0 {
		return Delivery{}, ErrDeliveryLease
	}
	if d.Status != DeliveryPending && !d.Lease(now) {
		return Delivery{}, ErrDeliveryExpired
	}
	d.Status = DeliveryLeased
	until := now.Add(lease)
	d.LeaseUntil = &until
	d.Attempt++
	return d, nil
}

func (d Delivery) MarkSent(now time.Time, eventID string) (Delivery, error) {
	if !d.Lease(now) {
		return Delivery{}, ErrDeliveryLease
	}
	d.Status = DeliverySent
	d.SentAt = &now
	d.LastEventID = eventID
	return d, nil
}

func (d Delivery) MarkAccepted(now time.Time) (Delivery, error) {
	if d.Status != DeliverySent && d.Status != DeliveryAccepted {
		return Delivery{}, ErrDeliveryLease
	}
	d.Status = DeliveryAccepted
	d.AcceptedAt = &now
	return d, nil
}

func (d Delivery) Complete(now time.Time, errCode string) Delivery {
	d.Status = DeliveryCompleted
	d.CompletedAt = &now
	d.ErrorCode = errCode
	d.LeaseUntil = nil
	return d
}
