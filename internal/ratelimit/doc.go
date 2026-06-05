// Package ratelimit provides a shared exponential-backoff HTTP helper
// that honours X-RateLimit-* and Retry-After headers, with a 3-attempt
// and 60-second cumulative cap.
package ratelimit
