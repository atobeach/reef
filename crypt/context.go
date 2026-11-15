package crypt

import (
	"context"
	"errors"
)

// ErrNoEncrypter is an encrypted column used on a context without an
// encrypter: failing loudly rather than storing the value in the clear.
var ErrNoEncrypter = errors.New("crypt: no encrypter bound to context (use crypt.With)")

type contextKey struct{}

// With binds e to ctx, for encrypted columns to seal and open values with.
func With(ctx context.Context, e *Encrypter) context.Context {
	return context.WithValue(ctx, contextKey{}, e)
}

// FromContext is the encrypter bound to ctx.
func FromContext(ctx context.Context) (*Encrypter, error) {
	e, _ := ctx.Value(contextKey{}).(*Encrypter)
	if e == nil {
		return nil, ErrNoEncrypter
	}

	return e, nil
}
