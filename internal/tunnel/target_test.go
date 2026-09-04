package tunnel

import (
	"net"
	"testing"
)

func TestDialTargetRewritesGatewayToLocalhost(t *testing.T) {
	got := dialTarget(net.ParseIP("198.18.0.1"), 8081)
	if got != "127.0.0.1:8081" {
		t.Fatalf("got %q", got)
	}
}

func TestDialTargetLeavesInternetAddresses(t *testing.T) {
	got := dialTarget(net.ParseIP("1.2.3.4"), 443)
	if got != "1.2.3.4:443" {
		t.Fatalf("got %q", got)
	}
}
