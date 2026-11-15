package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCallStartAndPlan(t *testing.T) {
	dir := t.TempDir()
	path := Path(filepath.Join(dir, "transarc.db"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Listen(ctx, path, func(cmd string) string {
			switch cmd {
			case "start":
				return "ok\npass started\n"
			case "plan":
				return "ok\nIDLE\n"
			default:
				return "unknown\n"
			}
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	var err error
	for {
		_, err = Call(path, "plan")
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Call(path, "plan")
	if err != nil || !strings.Contains(plan, "IDLE") {
		t.Fatalf("plan %q err %v", plan, err)
	}
	start, err := Call(path, "start")
	if err != nil || !strings.Contains(start, "pass started") {
		t.Fatalf("start %q err %v", start, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not stop")
	}
}

func TestCallWhenNothingIsListening(t *testing.T) {
	err := error(nil)
	_, err = Call(filepath.Join(t.TempDir(), "missing.sock"), "plan")
	if !Down(err) {
		t.Fatalf("err %v", err)
	}
}
