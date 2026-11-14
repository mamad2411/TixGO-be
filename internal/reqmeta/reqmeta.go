// Package reqmeta membawa metadata request (IP, UA, device, request-id) lewat context.Context
// agar service dapat mengisi audit log tanpa bergantung pada Gin.
package reqmeta

import "context"

type Meta struct {
	IP        string
	UserAgent string
	DeviceID  string
	RequestID string
}

type ctxKey struct{}

func With(ctx context.Context, m Meta) context.Context { return context.WithValue(ctx, ctxKey{}, m) }

func From(ctx context.Context) Meta {
	m, _ := ctx.Value(ctxKey{}).(Meta)
	return m
}
