package service

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cryptofyre/transarc/internal/config"
	"github.com/cryptofyre/transarc/internal/control"
	"github.com/cryptofyre/transarc/internal/logx"
	"github.com/cryptofyre/transarc/internal/state"
)

func TestPlanAndStartReachTheRunningService(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "transarc.db")
	store, err := state.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	r := &Runner{
		Cfg: config.Config{
			Schedule: "0 0 1 1 *",
			Timezone: "UTC",
			StateDB:  db,
			TempDir:  dir,
			Rules:    config.Rules{MinSavingsPercent: 10, SampleSeconds: -1},
		},
		Log:     logx.New(io.Discard),
		Store:   store,
		Backend: "svt",
		GPUs:    []string{"cpu"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- r.Serve(ctx) }()

	sock := control.Path(db)
	deadline := time.Now().Add(3 * time.Second)
	var reply string
	for {
		reply, err = control.Call(sock, "plan")
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "IDLE") || !strings.Contains(reply, "PLAN") {
		t.Fatalf("plan\n%s", reply)
	}
	reply, err = control.Call(sock, "start")
	if err != nil || !strings.HasPrefix(reply, "ok") {
		t.Fatalf("start %q err %v", reply, err)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("service did not stop")
	}
}
