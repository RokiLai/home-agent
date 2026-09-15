//go:build !race

package health

import "time"

const maxEvaluateAll100DevicesDuration = 1 * time.Second
