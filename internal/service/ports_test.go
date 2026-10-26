package service

import (
	"net"
	"testing"
)

// holdPort listens on address and returns the port it got.
func holdPort(t *testing.T, network, address string) int {
	t.Helper()
	listener, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener.Addr().(*net.TCPAddr).Port
}

func TestFreePortKeepsPreferredWhenFree(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	preferred := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	port, err := freePort(preferred)
	if err != nil {
		t.Fatal(err)
	}
	if port != preferred {
		t.Fatalf("port = %d, want %d", port, preferred)
	}
}

func TestFreePortSkipsHeldPort(t *testing.T) {
	cases := []struct {
		name, network, address string
	}{
		{"all interfaces", "tcp", ":0"},
		{"IPv4 loopback only", "tcp4", "127.0.0.1:0"},
		{"IPv4 all interfaces only", "tcp4", "0.0.0.0:0"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			held := holdPort(t, testCase.network, testCase.address)
			port, err := freePort(held)
			if err != nil {
				t.Fatal(err)
			}
			if port == held || port == 0 {
				t.Fatalf("port = %d, want a port other than %d", port, held)
			}
		})
	}
}
