// SPDX-License-Identifier: Apache-2.0

package media

import (
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// retryDelay is how long to wait after the n-th attempt: base * 2^(n-1), at most
// retryMaxDelay. With the defaults that is 300, 600, 1200, 2400 and then 3600
// seconds.
func retryDelay(attempts int) time.Duration {
	base := max(time.Second, retryBaseDelay)
	ceiling := max(base, retryMaxDelay)
	d := base
	for i := 1; i < attempts && d < ceiling; i++ {
		d *= 2
	}
	return min(d, ceiling)
}

// retryResult decides what a temporary failure turns into: pending again after
// a delay, or failed once the attempts are used up. A failure that did not cost
// an attempt (a provider cooldown: nothing was sent) leaves retry_count alone
// and can wait as long as it likes, but never forever.
func retryResult(item Item, meta string, e *domain.MediaRetryableError, errText string, now time.Time) Result {
	count := item.RetryCount
	if e.CountsAsAttempt {
		count++
	}
	if e.CountsAsAttempt && count >= max(1, retryMaxAttempts) {
		return Result{Status: domain.MediaFailed, Description: meta, Error: errText, RetryCount: &count}
	}
	delay := max(retryDelay(max(1, count)), e.RetryAfter)
	return Result{
		Status:      domain.MediaPending,
		Description: meta,
		Error:       errText,
		RetryCount:  &count,
		NextRetryAt: now.Add(delay),
	}
}
