package chat

import (
	"context"
	"errors"
	"net"
	"testing"
)

type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "net/http: timeout awaiting response headers" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

func TestIsTransportFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadline", context.DeadlineExceeded, true},
		{"net timeout", fakeTimeout{}, true},
		{"header timeout text", errors.New("net/http: timeout awaiting response headers"), true},
		{"refused", errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), true},
		{"upstream 429 is not transport", errors.New("upstream error: status 429"), false},
	}
	for _, c := range cases {
		if got := isTransportFailure(c.err); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	var ne net.Error = fakeTimeout{}
	if !ne.Timeout() {
		t.Fatal("fake must satisfy net.Error.Timeout")
	}
}
