//go:build race

package health

import "time"

const maxEvaluateAll100DevicesDuration = 5 * time.Second
