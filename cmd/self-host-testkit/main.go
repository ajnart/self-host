package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ajnart/self-host/testkit"
)

const maxMessage = 64 * 1024
const cleanupTimeout = 45 * time.Second

type request struct {
	Version int             `json:"version"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type params struct {
	App       string `json:"app"`
	TimeoutMS int64  `json:"timeoutMs"`
	Image     string `json:"image"`
	Name      string `json:"name"`
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type response struct {
	Version int            `json:"version"`
	ID      int64          `json:"id"`
	Result  any            `json:"result,omitempty"`
	Error   *protocolError `json:"error,omitempty"`
}

type fixture interface {
	URL() string
	GetAPIKey(context.Context, string) (string, error)
	Credentials(context.Context) (testkit.Credentials, error)
	Info(context.Context) (testkit.Info, error)
	Close(context.Context) error
}

type bridge struct {
	app      fixture
	started  bool
	manifest string
	owner    string
	start    func(context.Context, testkit.Options) (fixture, error)
}

type protocolWriter struct {
	ctx    context.Context
	output io.Writer
}

func (w protocolWriter) Write(data []byte) (int, error) {
	type written struct {
		n   int
		err error
	}
	done := make(chan written, 1)
	go func() {
		n, err := w.output.Write(data)
		done <- written{n: n, err: err}
	}()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.n, result.err
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	case <-timer.C:
		return 0, errors.New("transport write timed out")
	}
}

func main() {
	output := os.Stdout
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		os.Exit(1)
	}
	os.Stdout, os.Stderr = null, null
	flags := flag.NewFlagSet("self-host-testkit", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	manifest := flags.String("manifest", "", "")
	cleanup := flags.String("cleanup-manifest", "", "")
	owner := flags.String("owner", "", "")
	if flags.Parse(os.Args[1:]) != nil || flags.NArg() != 0 {
		os.Exit(2)
	}
	if *cleanup != "" {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if cleanupManifestOwned(ctx, *cleanup, *owner, cleanupRoot) != nil {
			os.Exit(1)
		}
		return
	}
	if *manifest == "" || !validOwner(*owner) {
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, deadlineCancel := context.WithTimeout(ctx, 14*time.Minute+15*time.Second)
	defer deadlineCancel()
	b := &bridge{manifest: *manifest, owner: *owner, start: func(ctx context.Context, options testkit.Options) (fixture, error) {
		app, err := testkit.Start(ctx, options)
		if err != nil {
			return nil, err
		}
		return app, nil
	}}
	if b.serve(ctx, os.Stdin, protocolWriter{ctx: ctx, output: output}) != nil {
		os.Exit(1)
	}
}

func cleanupRoot(ctx context.Context, root string) error {
	for {
		if testkit.Cleanup(ctx, root) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("fixture cleanup incomplete")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (b *bridge) serve(ctx context.Context, input io.Reader, output io.Writer) (result error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if b.app != nil {
			result = errors.Join(result, b.app.Close(cleanupCtx))
		}
	}()
	lines := make(chan []byte, 1)
	readErrors := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), maxMessage)
		defer func() {
			readErrors <- scanner.Err()
			close(lines)
			cancel()
		}()
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
	lastID := int64(0)
	for {
		select {
		case <-ctx.Done():
			select {
			case err := <-readErrors:
				return err
			default:
				return nil
			}
		case line, ok := <-lines:
			if !ok {
				return <-readErrors
			}
			var req request
			if json.Unmarshal(line, &req) != nil || req.Version != 1 || req.ID <= lastID {
				_ = writeResponse(output, response{Version: 1, ID: req.ID, Error: failure("PROTOCOL")})
				return errors.New("invalid protocol")
			}
			lastID = req.ID
			res := b.handle(ctx, req)
			if err := writeResponse(output, res); err != nil {
				return errors.New("transport failed")
			}
			if req.Method == "close" {
				return nil
			}
		}
	}
}

func writeResponse(output io.Writer, res response) error {
	data, err := json.Marshal(res)
	if err != nil || len(data) >= maxMessage {
		data, err = json.Marshal(response{Version: 1, ID: res.ID, Error: failure("PROTOCOL")})
		if err != nil {
			return errors.New("response encoding failed")
		}
	}
	_, err = output.Write(append(data, '\n'))
	return err
}

func failure(code string) *protocolError {
	messages := map[string]string{
		"PROTOCOL":    "Invalid testkit protocol.",
		"STATE":       "Fixture is not available.",
		"START":       "Fixture startup failed.",
		"CREDENTIALS": "Fixture credentials are not available.",
		"INFO":        "Fixture information is not available.",
		"CLEANUP":     "Fixture cleanup failed; run manifest cleanup.",
		"TIMEOUT":     "Fixture operation timed out.",
	}
	return &protocolError{Code: code, Message: messages[code]}
}

func (b *bridge) handle(ctx context.Context, req request) response {
	res := response{Version: 1, ID: req.ID}
	var p params
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &p) != nil {
		res.Error = failure("PROTOCOL")
		return res
	}
	if req.Method == "spinup" {
		if b.started || p.App == "" || p.TimeoutMS < 0 || p.TimeoutMS > 840000 {
			res.Error = failure("PROTOCOL")
			return res
		}
		b.started = true
		parent, err := registerParent(ctx, b.manifest, b.owner)
		if err != nil {
			res.Error = failure("START")
			return res
		}
		timeout := 12 * time.Minute
		if p.TimeoutMS > 0 {
			timeout = time.Duration(p.TimeoutMS) * time.Millisecond
		}
		startCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		b.app, err = b.start(startCtx, testkit.Options{App: p.App, Timeout: timeout, RootParent: parent, Image: p.Image})
		if err != nil {
			res.Error = failure("START")
			if errors.Is(startCtx.Err(), context.DeadlineExceeded) {
				res.Error = failure("TIMEOUT")
			}
			return res
		}
		res.Result = map[string]string{"url": b.app.URL()}
		return res
	}
	if req.Method == "close" {
		if b.app != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()
			if b.app.Close(closeCtx) != nil {
				res.Error = failure("CLEANUP")
				return res
			}
			b.app = nil
		}
		res.Result = map[string]bool{"closed": true}
		return res
	}
	if req.Method != "getApiKey" && req.Method != "credentials" && req.Method != "info" {
		res.Error = failure("PROTOCOL")
		return res
	}
	if b.app == nil {
		res.Error = failure("STATE")
		return res
	}
	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch req.Method {
	case "getApiKey":
		key, err := b.app.GetAPIKey(opCtx, p.Name)
		if err != nil {
			res.Error = failure("CREDENTIALS")
		} else {
			res.Result = map[string]string{"apiKey": key}
		}
	case "credentials":
		value, err := b.app.Credentials(opCtx)
		if err != nil {
			res.Error = failure("CREDENTIALS")
		} else {
			res.Result = map[string]string{"username": value.Username, "password": value.Password, "email": value.Email, "apiKey": value.APIKey}
		}
	case "info":
		value, err := b.app.Info(opCtx)
		if err != nil {
			res.Error = failure("INFO")
		} else {
			digests := value.RepoDigests
			if digests == nil {
				digests = []string{}
			}
			res.Result = map[string]any{"id": value.ID, "url": value.URL, "image": value.Image, "imageId": value.ImageID, "repoDigests": digests}
		}
	}
	return res
}
