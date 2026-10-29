package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Probe asks a running kagemusha for /healthz. It backs `kagemusha healthcheck`, so container
// health checks work on images without a shell or curl (distroless).
func Probe(ctx context.Context, listenHost string, port int) error {
	switch listenHost {
	case "", "0.0.0.0":
		listenHost = "127.0.0.1"
	case "::":
		listenHost = "::1"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	u := "http://" + net.JoinHostPort(listenHost, strconv.Itoa(port)) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}
