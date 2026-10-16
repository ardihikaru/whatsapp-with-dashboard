package upstream

import (
	"context"
	"fmt"
	"reflect"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// WaxumResult is one waxum call's outcome.
type WaxumResult struct {
	Host string
	Data any // the method's non-error return value(s); nil for error-only methods
}

// CallWaxum invokes the named *waxum.Client method by reflection (one
// generic caller for all exported methods, rather than 38 hand-written
// wrappers — sync_test.go still requires one Tool per method by name, via
// Tool.WaxumMethod, so nothing here lets a method go unwrapped). args must
// be ctx-exclusive, in the exact order the method declares them after ctx.
// GET-shaped methods (name not in writeWaxumMethods) fail over on dial
// error/timeout/5xx-ish errors; the rest only on a dial error.
func (p *Pool) CallWaxum(ctx context.Context, method string, write bool, target string, args ...any) (WaxumResult, error) {
	hosts, err := p.orderedHosts(target)
	if err != nil {
		return WaxumResult{}, err
	}
	var lastErr error
	for i, h := range hosts {
		last := i == len(hosts)-1
		data, callErr := callWaxumMethod(ctx, h.waxum, method, args...)
		if callErr != nil {
			if !last && shouldFailoverWaxumErr(write, callErr) {
				lastErr = fmt.Errorf("host %s: %w", h.Name, callErr)
				continue
			}
			return WaxumResult{}, fmt.Errorf("host %s: %w", h.Name, callErr)
		}
		return WaxumResult{Host: h.Name, Data: data}, nil
	}
	return WaxumResult{}, fmt.Errorf("all hosts failed: %w", lastErr)
}

// shouldFailoverWaxumErr applies the same read/write rule as shouldFailover
// but to a Go error from the waxum package (no HTTP status is visible here
// — waxum.APIError carries one, everything else is a sentinel or a
// transport error).
func shouldFailoverWaxumErr(write bool, err error) bool {
	if isDialError(err) {
		return true
	}
	if !write && isTimeout(err) {
		return true
	}
	if !write {
		var apiErr *waxum.APIError
		if ae, ok := err.(*waxum.APIError); ok {
			apiErr = ae
		}
		if apiErr != nil && apiErr.StatusCode >= 500 {
			return true
		}
	}
	return false
}

// callWaxumMethod calls client.<method>(ctx, args...) by reflection and
// returns its last non-error return value (nil if the method returns only
// an error), or the error if the last return value is a non-nil error.
func callWaxumMethod(ctx context.Context, client *waxum.Client, method string, args ...any) (any, error) {
	v := reflect.ValueOf(client).MethodByName(method)
	if !v.IsValid() {
		return nil, fmt.Errorf("waxum: no such method %q", method)
	}
	in := make([]reflect.Value, 0, len(args)+1)
	in = append(in, reflect.ValueOf(ctx))
	mt := v.Type()
	for i, a := range args {
		argIdx := i + 1
		if argIdx >= mt.NumIn() {
			return nil, fmt.Errorf("waxum: %s takes fewer arguments than given", method)
		}
		want := mt.In(argIdx)
		rv := reflect.ValueOf(a)
		if !rv.IsValid() {
			rv = reflect.Zero(want)
		} else if rv.Type() != want && rv.Type().ConvertibleTo(want) {
			rv = rv.Convert(want)
		}
		in = append(in, rv)
	}
	out := v.Call(in)
	if len(out) == 0 {
		return nil, nil
	}
	last := out[len(out)-1]
	if err, ok := last.Interface().(error); ok && err != nil {
		return nil, err
	}
	if len(out) == 1 {
		return nil, nil // error-only method, already nil-checked above
	}
	return out[0].Interface(), nil
}
