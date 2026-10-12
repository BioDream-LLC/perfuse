// Package chanscope narrows reads and writes on a context to some channels.
//
// It rides on the context rather than being a parameter of each query because there are dozens of queries across the
// message store and the delivery queue, and the limit is a property of who is asking, not of the question. The API sets
// it once per request for a person limited to some channel groups; every count, list, chart, search and queue action then
// agrees with the others, so nobody is told a total of forty failures and shown four.
package chanscope

import (
	"context"
	"strings"
)

type key struct{}

// With limits ctx to these channels. A nil slice means no limit is recorded as an empty one: no channels at all.
func With(ctx context.Context, channels []string) context.Context {
	if channels == nil {
		channels = []string{}
	}
	return context.WithValue(ctx, key{}, channels)
}

// From returns the scope on ctx, and whether there is one.
func From(ctx context.Context) ([]string, bool) {
	c, ok := ctx.Value(key{}).([]string)
	return c, ok
}

// Allows reports whether ctx may touch this channel. True when there is no scope.
func Allows(ctx context.Context, channel string) bool {
	c, ok := From(ctx)
	if !ok {
		return true
	}
	for _, v := range c {
		if v == channel {
			return true
		}
	}
	return false
}

// SQL is the clause to append to a WHERE, " AND <column> IN (?, ...)", and its arguments. Empty without a scope. A scope
// with no channels matches nothing rather than everything, which is the safe reading of "none".
func SQL(ctx context.Context, column string) (string, []any) {
	c, ok := From(ctx)
	if !ok {
		return "", nil
	}
	if len(c) == 0 {
		return " AND 0", nil
	}
	args := make([]any, len(c))
	for i, v := range c {
		args[i] = v
	}
	return " AND " + column + " IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(c)), ", ") + ")", args
}
