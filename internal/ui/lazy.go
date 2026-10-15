package ui

import (
	"gioui.org/layout"
	"gioui.org/op"
)

// bigWork is the input size from which views prepare their content in the
// background rather than holding up the frame.
const bigWork = 32 << 10

// lazy holds a value computed for a key, such as a decoded body, and
// computes it again when the key changes: at once for small inputs, in
// the background for big ones.
type lazy[T any] struct {
	key  string
	val  T
	ok   bool
	want string
}

// get returns the value for key, or false while it is being computed.
// compute must only read data that does not change, as it may run on
// another goroutine.
func (z *lazy[T]) get(a *App, key string, big bool, compute func() T) (T, bool) {
	if z.ok && z.key == key {
		return z.val, true
	}
	if !big {
		z.key, z.val, z.ok, z.want = key, compute(), true, ""
		return z.val, true
	}
	if z.want != key {
		z.want = key
		go func() {
			v := compute()
			a.Post(func() {
				if z.want == key {
					z.key, z.val, z.ok, z.want = key, v, true, ""
				}
			})
		}()
	}
	var zero T
	return zero, false
}

// loading fills the space with a spinner, to show that content is on its
// way.
func (th *Theme) loading(gtx layout.Context) layout.Dimensions {
	size := gtx.Constraints.Max
	gtx.Constraints = layout.Exact(size)
	gtx.Execute(op.InvalidateCmd{})
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return th.spinner(gtx, 22)
	})
}
