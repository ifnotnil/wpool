package wpool

import (
	"errors"
	"strings"

	"github.com/stretchr/testify/require"
)

func ExpectedErrorChecks(expected ...func(require.TestingT, error)) func(require.TestingT, error, ...interface{}) {
	return func(t require.TestingT, err error, msgAndArgs ...interface{}) {
		if h, ok := t.(interface{ Helper() }); ok {
			h.Helper()
		}
		for _, fn := range expected {
			fn(t, err)
		}
	}
}

func ErrorIs(allExpectedErrors ...error) func(require.TestingT, error, ...interface{}) {
	return func(t require.TestingT, err error, msgAndArgs ...interface{}) {
		if h, ok := t.(interface{ Helper() }); ok {
			h.Helper()
		}
		if err == nil {
			t.Errorf("expected error but none received")
			return
		}
		for _, expected := range allExpectedErrors {
			if !errors.Is(err, expected) {
				t.Errorf("error unexpected.\nExpected error: %T(%s) \nGot           : %T(%s)", expected, expected.Error(), err, err.Error())
			}
		}
	}
}

func ErrorOfType[T error](assertsOfType ...func(require.TestingT, T)) func(require.TestingT, error, ...interface{}) {
	return func(t require.TestingT, err error, msgAndArgs ...interface{}) {
		if h, ok := t.(interface{ Helper() }); ok {
			h.Helper()
		}

		if err == nil {
			t.Errorf("expected error but none received")
			return
		}

		var wantErr T
		if !errors.As(err, &wantErr) {
			var tErr T
			t.Errorf("Error type check failed.\nExpected error type: %T\nGot                : %T(%s)", tErr, err, err)
		} else {
			for _, e := range assertsOfType {
				e(t, wantErr)
			}
		}
	}
}

func ErrorStringContains(s string) func(require.TestingT, error, ...interface{}) {
	return func(t require.TestingT, err error, msgAndArgs ...interface{}) {
		if h, ok := t.(interface{ Helper() }); ok {
			h.Helper()
		}

		if err == nil {
			t.Errorf("expected error but none received")
			return
		}

		if !strings.Contains(err.Error(), s) {
			t.Errorf("error string check failed. \nExpected to contain: %s\nGot                : %s\n", s, err.Error())
		}
	}
}
