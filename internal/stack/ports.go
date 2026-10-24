package stack

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// freePort returns preferred when nothing holds it, otherwise a free port picked by the operating system.
func freePort(preferred int) (int, error) {
	if portFree(preferred) {
		return preferred, nil
	}
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, fmt.Errorf("pick a free port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func portFree(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	listener.Close()
	for _, host := range []string{"127.0.0.1", "::1"} {
		connection, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 200*time.Millisecond)
		if err == nil {
			connection.Close()
			return false
		}
	}
	return true
}
