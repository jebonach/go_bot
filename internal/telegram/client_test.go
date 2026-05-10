package telegram

import (
	"errors"
	"testing"
)

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		err      error
		expected int
		ok       bool
	}{
		{errors.New("Too Many Requests: retry after 7"), 7, true},
		{errors.New("flood control: retry after 30"), 30, true},
		{errors.New("Too Many Requests: retry after 0"), 0, true},
		{errors.New("network error"), 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := parseRetryAfter(c.err)
		if ok != c.ok || got != c.expected {
			t.Fatalf("parseRetryAfter(%v) = (%d, %v), want (%d, %v)", c.err, got, ok, c.expected, c.ok)
		}
	}
}

func TestIsRetryable_Permanent(t *testing.T) {
	c := &Client{}
	permanent := []error{
		errors.New("Forbidden: bot was blocked"),
		errors.New("Unauthorized: invalid token"),
	}
	for _, err := range permanent {
		if c.isRetryable(err) {
			t.Fatalf("expected non-retryable for %q", err)
		}
	}
}

func TestIsRetryable_Transient(t *testing.T) {
	c := &Client{}
	transient := []error{
		errors.New("Too Many Requests: retry after 5"),
		errors.New("connection reset"),
		errors.New("server error"),
	}
	for _, err := range transient {
		if !c.isRetryable(err) {
			t.Fatalf("expected retryable for %q", err)
		}
	}
}
