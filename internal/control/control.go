package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Path is the socket the running service listens on.
func Path(stateDB string) string {
	dir := filepath.Dir(stateDB)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "transarc.sock")
}

// Listen accepts one command per connection until ctx is canceled.
// The socket is removed when Listen returns.
func Listen(ctx context.Context, path string, handle func(string) string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// docker exec may be root or the service user. The socket is not on the network.
	_ = os.Chmod(path, 0o666)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	defer ln.Close()
	defer os.Remove(path)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func(conn net.Conn) {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
			line, err := bufio.NewReader(io.LimitReader(conn, 64)).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return
			}
			reply := handle(strings.TrimSpace(line))
			fmt.Fprint(conn, reply)
		}(conn)
	}
}

// Call sends one command and returns the reply.
func Call(path, cmd string) (string, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		return "", err
	}
	body, err := io.ReadAll(conn)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Down reports that no service is listening.
func Down(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) || errors.Is(err, os.ErrNotExist)
}
