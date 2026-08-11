// Package signer mints time-limited URLs for stored objects.
//
// Signing is a local cryptographic operation: it makes no request to the
// storage provider and performs no permission check, so the signing key's own
// policy is the access control. Scope it to reads on the asset prefix.
package signer

import (
	"context"
	"time"
)

type URLSigner interface {
	Sign(ctx context.Context, key string, ttl time.Duration) (string, error)
}
