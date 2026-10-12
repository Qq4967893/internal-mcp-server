package auth

import "context"

type Identity struct {
	ServiceID   string
	UserID      string
	SessionID   string
	RequestID   string
	Permissions []string
}

type contextKey struct{}

func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, identity)
}

func FromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(contextKey{}).(Identity)
	return identity, ok
}

func (i Identity) HasPermission(permission string) bool {
	for _, item := range i.Permissions {
		if item == permission {
			return true
		}
	}
	return false
}
