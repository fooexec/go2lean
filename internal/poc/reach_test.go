package poc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reachModule is shaped like grpc-go's MaxConnectionAge path (#224): a
// public option stores a public struct into unexported options, the
// server copies it into an internal package's config, and an internal
// helper divides by a field of it.
var reachModule = map[string]string{
	"go.mod": "module example.com/m\n\ngo 1.22\n",
	"keepalive/k.go": `package keepalive

type ServerParameters struct {
	Time   int
	MaxAge int
}
`,
	"internal/transport/t.go": `package transport

import "example.com/m/keepalive"

type ServerConfig struct{ Keepalive keepalive.ServerParameters }

func NewServerTransport(config *ServerConfig) int {
	kp := config.Keepalive
	if kp.MaxAge == 0 {
		kp.MaxAge = 100
	}
	kp.MaxAge += jitter(kp.MaxAge)
	return kp.MaxAge + checked(config.Keepalive.Time) + fixed(3)
}

func jitter(v int) int { return 1000 / (v / 10) }

func checked(t int) int {
	return 10 / t
}

func fixed(n int) int { return 10 / (n - 3) }
`,
	"server.go": `package m

import (
	"example.com/m/internal/transport"
	"example.com/m/keepalive"
)

type serverOptions struct{ kp keepalive.ServerParameters }

type ServerOption func(*serverOptions)

func KeepaliveParams(kp keepalive.ServerParameters) ServerOption {
	if kp.Time > 0 && kp.Time < 5 {
		kp.Time = 5
	}
	return func(o *serverOptions) { o.kp = kp }
}

type Server struct{ opts serverOptions }

func NewServer(opt ...ServerOption) *Server {
	s := &Server{}
	for _, o := range opt {
		o(&s.opts)
	}
	return s
}

func (s *Server) Serve() int {
	return transport.NewServerTransport(&transport.ServerConfig{Keepalive: s.opts.kp})
}
`,
}

func TestReachAPI(t *testing.T) {
	dir := t.TempDir()
	for name, src := range reachModule {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := newCallerIndex(dir)
	pkg := "example.com/m/internal/transport"

	reached, checks, ok := c.reachAPI(pkg, "jitter", map[string]string{"v": "5"})
	want := "m.KeepaliveParams(kp.MaxAge) → serverOptions.kp → ServerConfig.Keepalive → transport.jitter(v)"
	if !ok || len(checks) != 0 || len(reached) != 1 || reached[0] != want {
		t.Errorf("jitter: reached %v, checks %v, ok %v; want MaxAge reached with no check", reached, checks, ok)
	}
	// Time goes through KeepaliveParams' kp.Time > 0 && kp.Time < 5.
	reached, checks, ok = c.reachAPI(pkg, "checked", map[string]string{"t": "0"})
	if !ok || len(checks) == 0 || !strings.Contains(strings.Join(checks, " "), "KeepaliveParams") {
		t.Errorf("checked: reached %v, checks %v, ok %v; want reached through checks", reached, checks, ok)
	}
	// A constant argument reaches nothing.
	if reached, _, ok := c.reachAPI(pkg, "fixed", map[string]string{"n": "3"}); ok {
		t.Errorf("fixed: reached %v, want nothing", reached)
	}
}
