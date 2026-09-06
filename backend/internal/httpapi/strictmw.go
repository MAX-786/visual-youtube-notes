package httpapi

import (
	"context"
	"net/http"
)

type strictContextKey int

const (
	writerKey strictContextKey = iota
	requestKey
)

// exposeHTTP is the one strict middleware: it lets a handler reach the
// ResponseWriter and Request behind its typed arguments, which is what
// setting cookies and reading the client address need.
func exposeHTTP(f StrictHandlerFunc, _ string) StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		ctx = context.WithValue(ctx, writerKey, w)
		ctx = context.WithValue(ctx, requestKey, r)
		return f(ctx, w, r, request)
	}
}

func writerFrom(ctx context.Context) http.ResponseWriter {
	w, _ := ctx.Value(writerKey).(http.ResponseWriter)
	return w
}

func requestFrom(ctx context.Context) *http.Request {
	r, _ := ctx.Value(requestKey).(*http.Request)
	return r
}
