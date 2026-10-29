package chrome

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"
)

// The test binary doubles as a fake Lightpanda: with KAGEMUSHA_FAKE_LIGHTPANDA set it serves
// /json/version on --port (mode "ok"), exits at once ("exit") or never serves ("hang").
func TestMain(m *testing.M) {
	if mode := os.Getenv("KAGEMUSHA_FAKE_LIGHTPANDA"); mode != "" {
		fakeLightpanda(mode)
		return
	}
	os.Exit(m.Run())
}

func fakeLightpanda(mode string) {
	args := os.Args[1:]
	port := ""
	if i := slices.Index(args, "--port"); i >= 0 && i+1 < len(args) {
		port = args[i+1]
	}
	ok := os.Getenv("LIGHTPANDA_DISABLE_TELEMETRY") == "true" && slices.Contains(args, "serve") && slices.Contains(args, "--user-agent-suffix")
	switch {
	case mode == "exit" || !ok || port == "":
		os.Exit(3)
	case mode == "hang":
		time.Sleep(time.Hour)
	}
	http.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws://127.0.0.1:" + port + "/"})
	})
	_ = http.ListenAndServe("127.0.0.1:"+port, nil)
}

func fakeEngine(t *testing.T, mode string) *Engine {
	t.Helper()
	t.Setenv("KAGEMUSHA_FAKE_LIGHTPANDA", mode)
	return NewLightpanda(os.Args[0], []string{"serve"}, nil)
}

func TestLightpanda_LaunchHealthyClose(t *testing.T) {
	e := fakeEngine(t, "ok")
	if e.Healthy(context.Background()) {
		t.Fatal("unhealthy before launch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Launch(ctx); err != nil {
		t.Fatalf("launch (telemetry off, suffix and serve passed): %v", err)
	}
	if !e.Healthy(context.Background()) {
		t.Fatal("healthy after launch")
	}
	tabCtx, tabCancel, ua, err := e.openTab()
	if err != nil || tabCtx == nil || ua != "" {
		t.Fatalf("tab: %v ua=%q", err, ua)
	}
	tabCancel()
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.Healthy(context.Background()) {
		t.Fatal("unhealthy after close")
	}
	if err := e.Close(context.Background()); err != nil {
		t.Fatal("closing twice is fine")
	}
}

func TestLightpanda_NotLaunched(t *testing.T) {
	e := NewLightpanda("/nonexistent", nil, nil)
	if _, _, _, err := e.openTab(); err != ErrNotLaunched {
		t.Fatalf("want ErrNotLaunched, got %v", err)
	}
}

func TestLightpanda_LaunchFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := NewLightpanda("/nonexistent/lightpanda", []string{"serve"}, nil).Launch(ctx); err == nil {
		t.Fatal("missing binary must fail")
	}
	if err := fakeEngine(t, "exit").Launch(ctx); err == nil {
		t.Fatal("a process that exits must fail")
	}
	short, cancelShort := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelShort()
	e := fakeEngine(t, "hang")
	if err := e.Launch(short); err == nil {
		t.Fatal("a process that never serves must fail at the deadline")
	}
}

func TestLightpanda_UnhealthyWhenProcessDies(t *testing.T) {
	e := fakeEngine(t, "ok")
	if err := e.Launch(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.process().kill()
	<-e.process().exited
	if e.Healthy(context.Background()) {
		t.Fatal("a dead process is unhealthy")
	}
	_ = e.Close(context.Background())
}
