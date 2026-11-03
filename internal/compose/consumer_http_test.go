package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

const (
	consumerContainer = "aaaaaaaaaaaa"
	targetContainer   = "bbbbbbbbbbbb"
	testKey           = "sentinel-key-value"
)

type consumerFixture struct {
	runner   *systemtest.Runner
	compose  *Compose
	networks map[string]string
	transfer func() string
	emitted  []string
}

func inspectJSON(service, id, ip string, networks int) string {
	attached := map[string]any{}
	for index := 0; index < networks; index++ {
		name := fmt.Sprintf("selfhost_net%d", index)
		attached[name] = map[string]any{"NetworkID": "net" + fmt.Sprint(index), "IPAddress": ip, "GlobalIPv6Address": "", "Aliases": []string{service, id}}
	}
	raw, _ := json.Marshal(map[string]any{
		"id": id, "image": "sha256:image", "running": true,
		"labels": map[string]string{"com.docker.compose.project": "selfhost", "com.docker.compose.service": service, "com.docker.compose.oneoff": "False", "com.docker.compose.container-number": "1"},
		"mode":   "selfhost_net0", "networks": attached,
	})
	return string(raw)
}

func newConsumerFixture(t *testing.T) *consumerFixture {
	t.Helper()
	f := &consumerFixture{networks: map[string]string{}}
	deps := systemtest.NewDeps(t)
	f.runner = deps.Runner.(*systemtest.Runner)
	f.compose = New(deps, events.New(events.SinkFunc(func(e events.Event) {
		if e.Event == events.Command {
			f.emitted = append(f.emitted, e.Command)
		}
	})), t.TempDir(), "selfhost")
	f.runner.OnFunc("docker compose --project-directory "+f.compose.root+" -p selfhost ps -q bazarr", func(system.Cmd) system.Result {
		return system.Result{Stdout: []byte(consumerContainer + "\n")}
	})
	f.runner.OnFunc("docker compose --project-directory "+f.compose.root+" -p selfhost ps -q sonarr", func(system.Cmd) system.Result {
		return system.Result{Stdout: []byte(targetContainer + "\n")}
	})
	f.networks["consumer"], f.networks["target"] = inspectJSON("bazarr", consumerContainer, "172.18.0.2", 1), inspectJSON("sonarr", targetContainer, "172.18.0.3", 1)
	f.runner.OnFunc("docker container inspect", func(cmd system.Cmd) system.Result {
		if cmd.Args[len(cmd.Args)-1] == consumerContainer {
			return system.Result{Stdout: []byte(f.networks["consumer"])}
		}
		return system.Result{Stdout: []byte(f.networks["target"])}
	})
	f.runner.On("docker network inspect", system.Result{Stdout: []byte(`{"driver":"bridge","scope":"local","ipv6":false,"labels":{"com.docker.compose.project":"selfhost"},"attached":2}`)})
	f.runner.On("docker exec -T "+consumerContainer+" s6-setuidgid abc sh -eu -c", system.Result{Stdout: []byte("1000\ntimeout (GNU coreutils) 9.5\ncurl 8.11.1 (x86_64-alpine-linux-musl)\nProtocols: dict file ftp http https\n")})
	f.runner.On("docker exec -T "+consumerContainer+" s6-setuidgid abc timeout -k 1 3 python3", system.Result{Stdout: []byte("172.18.0.3\n")})
	f.transfer = func() string { return "{\"ok\":true}\n--selfhost-v1 200 172.18.0.3 8989\n" }
	f.runner.OnFunc("docker exec -T -e "+consumerKeyEnv+" "+consumerContainer, func(system.Cmd) system.Result {
		return system.Result{Stdout: []byte(f.transfer())}
	})
	return f
}

func (f *consumerFixture) get() (int, []byte, error) {
	return f.compose.ConsumerGET(context.Background(), "bazarr", "sonarr", 8989, "/api/v3/system/status", testKey)
}

func (f *consumerFixture) transfers() int {
	count := 0
	for _, call := range f.runner.Cmds() {
		if strings.Contains(strings.Join(call.Args, " "), "--expand-header") {
			count++
		}
	}
	return count
}

func TestConsumerGETReadsFromTheConsumerWithoutPuttingTheKeyInArguments(t *testing.T) {
	f := newConsumerFixture(t)
	status, body, err := f.get()
	if err != nil || status != 200 || string(body) != "{\"ok\":true}" {
		t.Fatalf("status=%d body=%q err=%v", status, body, err)
	}
	for _, cmd := range f.runner.Cmds() {
		line := system.Cmd{Name: cmd.Name, Args: cmd.Args}
		if strings.Contains(strings.Join(line.Args, " "), testKey) {
			t.Fatal("key appeared in argv")
		}
		if strings.Contains(strings.Join(cmd.Args, " "), "--expand-header") {
			if len(cmd.Env) != 1 || cmd.Env[0] != consumerKeyEnv+"="+testKey || cmd.WaitDelay == 0 {
				t.Fatal("key was not handed over only through the environment with a bounded wait")
			}
		}
	}
	for _, line := range f.emitted {
		if strings.Contains(line, testKey) {
			t.Fatal("key appeared in command events")
		}
	}
}

func TestConsumerGETRefusesBeforeAnyKeyHandoff(t *testing.T) {
	cases := map[string]func(*consumerFixture){
		"extra network": func(f *consumerFixture) {
			f.networks["target"] = inspectJSON("sonarr", targetContainer, "172.18.0.3", 2)
		},
		"foreign dns": func(f *consumerFixture) {
			f.runner.On("docker exec -T "+consumerContainer+" s6-setuidgid abc timeout -k 1 3 python3", system.Result{Stdout: []byte("172.18.0.3\n172.18.0.9\n")})
		},
		"old curl": func(f *consumerFixture) {
			f.runner.On("docker exec -T "+consumerContainer+" s6-setuidgid abc sh -eu -c", system.Result{Stdout: []byte("1000\ntimeout (GNU coreutils) 9.5\ncurl 8.3.0 (x)\nProtocols: http\n")})
		},
		"root identity": func(f *consumerFixture) {
			f.runner.On("docker exec -T "+consumerContainer+" s6-setuidgid abc sh -eu -c", system.Result{Stdout: []byte("0\ntimeout (GNU coreutils) 9.5\ncurl 8.11.1 (x)\nProtocols: http\n")})
		},
		"foreign project": func(f *consumerFixture) {
			f.networks["target"] = strings.Replace(f.networks["target"], `"selfhost"`, `"other"`, 1)
		},
		"external network": func(f *consumerFixture) {
			f.runner.On("docker network inspect", system.Result{Stdout: []byte(`{"driver":"bridge","scope":"local","ipv6":false,"labels":{},"attached":2}`)})
		},
		"ipv6 network": func(f *consumerFixture) {
			f.runner.On("docker network inspect", system.Result{Stdout: []byte(`{"driver":"bridge","scope":"local","ipv6":true,"labels":{"com.docker.compose.project":"selfhost"},"attached":2}`)})
		},
		"two replicas": func(f *consumerFixture) {
			f.runner.On("docker compose --project-directory "+f.compose.root+" -p selfhost ps -q sonarr", system.Result{Stdout: []byte(targetContainer + "\ncccccccccccc\n")})
		},
		"oversized network": func(f *consumerFixture) {
			f.runner.On("docker network inspect", system.Result{Stdout: []byte(`{"driver":"bridge","scope":"local","ipv6":false,"labels":{"com.docker.compose.project":"selfhost"},"attached":65}`)})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newConsumerFixture(t)
			mutate(f)
			_, _, err := f.get()
			if !errors.Is(err, errors.ErrUnsupported) || f.transfers() != 0 {
				t.Fatalf("expected refusal without handoff; err=%v transfers=%d", err, f.transfers())
			}
		})
	}
}

func TestConsumerGETRejectsInvalidRequestsBeforeAnyCommand(t *testing.T) {
	f := newConsumerFixture(t)
	routes := []string{"", "api", "/a//b", "/a/../b", "/a/./b", "/a?x=1", "/a#x", "/a%2f", "http://evil/a", "/a\nb", `/a\b`}
	for _, route := range routes {
		if _, _, err := f.compose.ConsumerGET(context.Background(), "bazarr", "sonarr", 8989, route, testKey); !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("route %q was accepted", route)
		}
	}
	for _, key := range []string{"", "has space", "line\nbreak", strings.Repeat("k", 513), "ünï"} {
		if _, _, err := f.compose.ConsumerGET(context.Background(), "bazarr", "sonarr", 8989, "/api", key); !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("key %q was accepted", key)
		}
	}
	if _, _, err := f.compose.ConsumerGET(context.Background(), "bazarr", "sonarr", 0, "/api", testKey); err == nil {
		t.Fatal("invalid port was accepted")
	}
	if len(f.runner.Cmds()) != 0 {
		t.Fatal("an invalid request ran a command")
	}
}

func TestConsumerGETNeverTrustsIncompleteOverflowingOrForgedTransfers(t *testing.T) {
	cases := map[string]string{
		"missing trailer":  `{"a":1}`,
		"wrong peer":       "{}\n--selfhost-v1 200 172.18.0.9 8989\n",
		"wrong port":       "{}\n--selfhost-v1 200 172.18.0.3 80\n",
		"no response":      "\n--selfhost-v1 000 172.18.0.3 8989\n",
		"trailing garbage": "{}\n--selfhost-v1 200 172.18.0.3 8989\nextra\n",
		"overflow":         strings.Repeat("x", consumerBodyLimit+1) + "\n--selfhost-v1 200 172.18.0.3 8989\n",
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			f := newConsumerFixture(t)
			f.transfer = func() string { return output }
			if _, _, err := f.get(); err == nil {
				t.Fatal("untrusted transfer was accepted")
			}
		})
	}
	t.Run("forged success after failure", func(t *testing.T) {
		f := newConsumerFixture(t)
		f.runner.OnStreamIO("docker exec -T -e "+consumerKeyEnv, func(_ context.Context, _ system.Cmd, _ io.Reader, stdout, _ io.Writer) error {
			io.WriteString(stdout, "{}\n--selfhost-v1 200 172.18.0.3 8989\n")
			return errors.New("exit status 28")
		})
		if _, _, err := f.get(); err == nil {
			t.Fatal("a nonzero exit with a forged trailer passed")
		}
	})
	t.Run("huge stdout is capped", func(t *testing.T) {
		f := newConsumerFixture(t)
		f.runner.OnStreamIO("docker exec -T -e "+consumerKeyEnv, func(_ context.Context, _ system.Cmd, _ io.Reader, stdout, _ io.Writer) error {
			_, err := stdout.Write([]byte(strings.Repeat("x", consumerBodyLimit+4096)))
			return err
		})
		if _, _, err := f.get(); err == nil {
			t.Fatal("oversized output was accepted")
		}
	})
}

func TestConsumerGETDiscardsResultsWhenThePeerChangesDuringTheTransfer(t *testing.T) {
	f := newConsumerFixture(t)
	f.transfer = func() string {
		f.networks["target"] = inspectJSON("sonarr", targetContainer, "172.18.0.8", 1)
		return "{}\n--selfhost-v1 200 172.18.0.3 8989\n"
	}
	if _, _, err := f.get(); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal("a changed endpoint kept its result")
	}
}

func TestConsumerGETFailsClosedWithoutBoundedRunner(t *testing.T) {
	f := newConsumerFixture(t)
	f.compose.deps.Runner = plainRunner{f.compose.deps.Runner}
	if _, _, err := f.get(); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal("an unbounded runner was used")
	}
}

type plainRunner struct{ system.Runner }
