package chrome

import (
	"context"
	"testing"
	"time"

	"github.com/otaqq/kagemusha/internal/render"
)

func TestEngine_ErrNotLaunchedBeforeLaunch(t *testing.T) {
	e := New("/nonexistent", nil, nil)
	if _, err := e.Render(context.Background(), render.Request{URL: "http://x/"}); err != ErrNotLaunched {
		t.Fatalf("want ErrNotLaunched, got %v", err)
	}
	if e.Healthy(context.Background()) {
		t.Fatal("must be unhealthy before launch")
	}
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEngine_LaunchFailsForMissingBinary(t *testing.T) {
	e := New("/nonexistent/chromium", []string{"--headless=new"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Launch(ctx); err == nil {
		t.Fatal("want error")
	}
}

func TestActivity_Idle(t *testing.T) {
	a := &activity{last: time.Now()}
	a.start()
	a.load()
	if a.idle(0) {
		t.Fatal("inflight request must block idle")
	}
	a.finish()
	a.finish() // a double finish never goes negative
	time.Sleep(5 * time.Millisecond)
	if !a.idle(time.Millisecond) {
		t.Fatal("must be idle")
	}
	if a.idle(time.Hour) {
		t.Fatal("quiet window not elapsed")
	}
}

func TestStatusCode_HTTPErrorWinsOverDeclaredStatus(t *testing.T) {
	cases := []struct{ http, declared, want int }{
		{0, 0, 200},     // status unknown, nothing declared
		{200, 0, 200},   // plain success
		{200, 404, 404}, // SPA declares not found on a 200 shell
		{401, 0, 401},   // Basic auth wall must not become a cacheable 200
		{500, 200, 500}, // the page cannot hide a server error
		{0, 410, 410},   // status unknown, declared wins
	}
	for _, c := range cases {
		if got := statusCode(c.http, c.declared); got != c.want {
			t.Errorf("statusCode(%d, %d) = %d, want %d", c.http, c.declared, got, c.want)
		}
	}
}
