package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// Lightpanda speaks CDP but differs from Chrome in how it is driven:
//   - it is a CDP server (`lightpanda serve`), not a browser printing a DevTools URL, so kagemusha
//     starts it on a free loopback port and attaches with chromedp/remote;
//   - it allows one browser context per CDP connection, so every render opens its own connection;
//   - it must not claim to be another browser, so the kagemusha marker goes through
//     --user-agent-suffix instead of overriding the user agent;
//   - telemetry is turned off (LIGHTPANDA_DISABLE_TELEMETRY), as kagemusha never phones home.

// lpProcess is a running Lightpanda CDP server.
type lpProcess struct {
	cmd    *exec.Cmd
	base   string // http://127.0.0.1:<port>
	ws     string // CDP WebSocket URL
	exited chan struct{}
}

// NewLightpanda creates an Engine that drives Lightpanda. args start with the subcommand ("serve").
func NewLightpanda(path string, args []string, log *slog.Logger) *Engine {
	e := New(path, args, log)
	e.lightpanda = true
	return e
}

func (e *Engine) launchLightpanda(ctx context.Context) error {
	port, err := freePort()
	if err != nil {
		return fmt.Errorf("lightpanda: launch: %w", err)
	}
	args := append(append([]string{}, e.args...), "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--user-agent-suffix", "kagemusha")
	cmd := exec.Command(e.path, args...)
	cmd.Env = append(os.Environ(), "LIGHTPANDA_DISABLE_TELEMETRY=true")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr // keep kagemusha's JSON stdout clean
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("lightpanda: launch: %w", err)
	}
	p := &lpProcess{cmd: cmd, base: "http://127.0.0.1:" + strconv.Itoa(port), exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.exited) }()

	for {
		if p.ws, err = lpVersion(ctx, p.base); err == nil {
			break
		}
		select {
		case <-p.exited:
			return fmt.Errorf("lightpanda: launch: process exited before serving CDP")
		case <-ctx.Done():
			p.kill()
			return fmt.Errorf("lightpanda: launch: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	e.mu.Lock()
	e.lp = p
	e.mu.Unlock()
	return nil
}

// lpVersion reads the CDP WebSocket URL from /json/version.
func lpVersion(ctx context.Context, base string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var v struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&v) != nil || v.WS == "" {
		return "", errors.New("lightpanda: no CDP endpoint")
	}
	return v.WS, nil
}

func (e *Engine) process() *lpProcess {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lp
}

func (e *Engine) openLightpandaTab() (context.Context, context.CancelFunc, string, error) {
	p := e.process()
	if p == nil {
		return nil, nil, "", ErrNotLaunched
	}
	allocCtx, allocCancel := remote.NewAllocator(context.Background(), p.ws)
	tabCtx, tabCancel := chromedp.NewContext(allocCtx, chromedp.WithNewBrowserContext(), chromedp.WithLogf(func(string, ...any) {}))
	return tabCtx, func() { tabCancel(); allocCancel() }, "", nil
}

// lightpandaHealthy reports whether the process runs and answers /json/version.
func (e *Engine) lightpandaHealthy(ctx context.Context) bool {
	p := e.process()
	if p == nil {
		return false
	}
	select {
	case <-p.exited:
		return false
	default:
	}
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := lpVersion(hctx, p.base)
	return err == nil
}

func (e *Engine) closeLightpanda(ctx context.Context) error {
	e.mu.Lock()
	p := e.lp
	e.lp = nil
	e.mu.Unlock()
	if p == nil {
		return nil
	}
	p.kill()
	select {
	case <-p.exited:
	case <-ctx.Done():
	}
	return nil
}

func (p *lpProcess) kill() { _ = p.cmd.Process.Kill() }

// freePort asks the kernel for an unused loopback port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
