// Package aireserve provides pointer helper functions for the AI Reserve SDK,
// mirroring the aws-sdk-go-v2 root package conventions.
package aireserve

// String returns a pointer to the given string value.
func String(v string) *string { return &v }

// Int32 returns a pointer to the given int32 value.
func Int32(v int32) *int32 { return &v }

// Float32 returns a pointer to the given float32 value.
func Float32(v float32) *float32 { return &v }

// Float64 returns a pointer to the given float64 value.
func Float64(v float64) *float64 { return &v }

// Bool returns a pointer to the given bool value.
func Bool(v bool) *bool { return &v }
