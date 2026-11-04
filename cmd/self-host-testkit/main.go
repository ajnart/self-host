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
	App                 string            `json:"app"`
	TimeoutMS           int64             `json:"timeoutMs"`
	Image               string            `json:"image"`
	Name                string            `json:"name"`
	Consumer            string            `json:"consumer"`
	Target              string            `json:"target"`
	Order               string            `json:"order"`
	Images              map[string]string `json:"images"`
	SonarrFilterWitness bool              `json:"sonarrFilterWitness"`
}

type protocolError struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	Reason        string `json:"reason,omitempty"`
	CleanupFailed bool   `json:"cleanupFailed,omitempty"`
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

type fixtureView interface {
	URL() string
	GetAPIKey(context.Context, string) (string, error)
	Credentials(context.Context) (testkit.Credentials, error)
	Info(context.Context) (testkit.Info, error)
}

type pairFixture interface {
	View(string) (fixtureView, error)
	Initial() testkit.PairInspection
	Inspect(context.Context) (testkit.PairInspection, error)
	Setup(context.Context, string) (testkit.PairInspection, error)
	Remove(context.Context, string) (testkit.PairInspection, error)
	Install(context.Context, string) (testkit.PairInspection, error)
	CreateSonarrFilterWitness(context.Context) (testkit.SonarrFilterWitness, error)
	ReadSonarrFilterWitness(context.Context) (testkit.SonarrFilterWitness, error)
	Close(context.Context) error
}

type pairAdapter struct{ *testkit.Pair }

func (p pairAdapter) View(id string) (fixtureView, error) { return p.Pair.App(id) }

type bridge struct {
	app                 fixture
	appCancel           context.CancelFunc
	started             bool
	manifest            string
	owner               string
	start               func(context.Context, testkit.Options) (fixture, error)
	pair                pairFixture
	pairFailed          bool
	sonarrFilterWitness bool
	startPair           func(context.Context, testkit.PairOptions) (pairFixture, error)
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
	b.startPair = func(ctx context.Context, options testkit.PairOptions) (pairFixture, error) {
		pair, err := testkit.StartPair(ctx, options)
		if err != nil {
			return nil, err
		}
		return pairAdapter{Pair: pair}, nil
	}
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
		if b.appCancel != nil {
			defer b.appCancel()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if b.app != nil {
			result = errors.Join(result, b.app.Close(cleanupCtx))
		}
		if b.pair != nil {
			result = errors.Join(result, b.pair.Close(cleanupCtx))
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

func startFailure(ctx context.Context, err error) *protocolError {
	result := failure("START")
	result.Reason = string(testkit.StartupReasonSetup)
	var safe *testkit.StartupError
	if errors.As(err, &safe) {
		result.Reason = safe.Reason()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result = failure("TIMEOUT")
	} else if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		result = failure("CANCELED")
	}
	result.CleanupFailed = errors.Is(err, testkit.ErrCleanupIncomplete)
	return result
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
		"OPERATION":   "Fixture pair action or inspection failed.",
		"CANCELED":    "Fixture operation was canceled.",
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
	if req.Method == "capabilities" {
		if !emptyParams(req.Params) {
			res.Error = failure("PROTOCOL")
		} else {
			res.Result = map[string]int{"sonarrFilterWitness": 1}
		}
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
			res.Error.Reason = string(testkit.StartupReasonStorage)
			return res
		}
		timeout := 12 * time.Minute
		if p.TimeoutMS > 0 {
			timeout = time.Duration(p.TimeoutMS) * time.Millisecond
		}
		appCtx, cancel := context.WithCancel(ctx)
		b.app, err = b.start(appCtx, testkit.Options{App: p.App, Timeout: timeout, RootParent: parent, Image: p.Image})
		if err != nil {
			cancel()
			res.Error = startFailure(ctx, err)
			return res
		}
		b.appCancel = cancel
		res.Result = map[string]string{"url": b.app.URL()}
		return res
	}
	if req.Method == "spinupPair" {
		if b.started || b.startPair == nil || p.Consumer == "" || p.Target == "" || p.TimeoutMS < 0 || p.TimeoutMS > 840000 {
			res.Error = failure("PROTOCOL")
			return res
		}
		if p.SonarrFilterWitness && (p.Consumer != "homarr" || p.Target != "sonarr") {
			res.Error = failure("PROTOCOL")
			return res
		}
		b.started = true
		parent, err := registerParent(ctx, b.manifest, b.owner)
		if err != nil {
			res.Error = failure("START")
			res.Error.Reason = string(testkit.StartupReasonStorage)
			return res
		}
		timeout := 12 * time.Minute
		if p.TimeoutMS > 0 {
			timeout = time.Duration(p.TimeoutMS) * time.Millisecond
		}
		lifetime, cancel := context.WithCancel(ctx)
		b.pair, err = b.startPair(lifetime, testkit.PairOptions{Consumer: p.Consumer, Target: p.Target, Order: testkit.PairOrder(p.Order), Images: p.Images, Timeout: timeout, RootParent: parent, SonarrFilterWitness: p.SonarrFilterWitness})
		if err != nil {
			cancel()
			res.Error = startFailure(ctx, err)
			return res
		}
		b.appCancel = cancel
		consumer, consumerErr := b.pair.View(p.Consumer)
		target, targetErr := b.pair.View(p.Target)
		if consumerErr != nil || targetErr != nil {
			res.Error = failure("STATE")
			return res
		}
		result := map[string]any{"consumer": map[string]string{"id": p.Consumer, "url": consumer.URL()}, "target": map[string]string{"id": p.Target, "url": target.URL()}, "initial": b.pair.Initial()}
		b.sonarrFilterWitness = p.SonarrFilterWitness
		if p.SonarrFilterWitness {
			result["sonarrFilterWitness"] = 1
		}
		res.Result = result
		return res
	}
	if req.Method == "close" {
		if b.appCancel != nil {
			defer b.appCancel()
		}
		if b.app != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()
			if b.app.Close(closeCtx) != nil {
				res.Error = failure("CLEANUP")
				return res
			}
			b.app = nil
		}
		if b.pair != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()
			if b.pair.Close(closeCtx) != nil {
				res.Error = failure("CLEANUP")
				return res
			}
			b.pair = nil
		}
		res.Result = map[string]bool{"closed": true}
		return res
	}
	if req.Method == "createSonarrFilterWitness" || req.Method == "readSonarrFilterWitness" {
		if !emptyParams(req.Params) {
			res.Error = failure("PROTOCOL")
			return res
		}
		if b.pair == nil || b.pairFailed || !b.sonarrFilterWitness {
			res.Error = failure("STATE")
			return res
		}
		opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var value testkit.SonarrFilterWitness
		var err error
		if req.Method == "createSonarrFilterWitness" {
			value, err = b.pair.CreateSonarrFilterWitness(opCtx)
		} else {
			value, err = b.pair.ReadSonarrFilterWitness(opCtx)
		}
		if err != nil {
			res.Error = b.pairOperationFailure(err)
		} else {
			res.Result = value
		}
		return res
	}
	if req.Method == "inspect" || req.Method == "setup" || req.Method == "remove" || req.Method == "install" {
		if b.pair == nil || b.pairFailed {
			res.Error = failure("STATE")
			return res
		}
		if req.Method != "inspect" && !(req.Method == "setup" && p.App == "") {
			if _, err := b.pair.View(p.App); err != nil {
				res.Error = failure("STATE")
				return res
			}
		}
		opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if req.Method == "setup" || req.Method == "install" {
			cancel()
			opCtx, cancel = context.WithCancel(ctx)
		}
		defer cancel()
		var value testkit.PairInspection
		var err error
		switch req.Method {
		case "inspect":
			value, err = b.pair.Inspect(opCtx)
		case "setup":
			value, err = b.pair.Setup(opCtx, p.App)
		case "remove":
			value, err = b.pair.Remove(opCtx, p.App)
		case "install":
			value, err = b.pair.Install(opCtx, p.App)
		}
		if err != nil {
			res.Error = b.pairOperationFailure(err)
		} else {
			res.Result = value
		}
		return res
	}
	if req.Method != "getApiKey" && req.Method != "credentials" && req.Method != "info" {
		res.Error = failure("PROTOCOL")
		return res
	}
	if b.app == nil && b.pair == nil || b.pairFailed {
		res.Error = failure("STATE")
		return res
	}
	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var selected fixtureView = b.app
	if b.pair != nil {
		view, err := b.pair.View(p.App)
		if err != nil {
			res.Error = failure("STATE")
			return res
		}
		selected = view
	}
	switch req.Method {
	case "getApiKey":
		key, err := selected.GetAPIKey(opCtx, p.Name)
		if err != nil {
			var operation *testkit.OperationError
			if errors.As(err, &operation) {
				res.Error = b.pairOperationFailure(err)
			} else {
				res.Error = failure("CREDENTIALS")
			}
		} else {
			res.Result = map[string]string{"apiKey": key}
		}
	case "credentials":
		value, err := selected.Credentials(opCtx)
		if err != nil {
			var operation *testkit.OperationError
			if errors.As(err, &operation) {
				res.Error = b.pairOperationFailure(err)
			} else {
				res.Error = failure("CREDENTIALS")
			}
		} else {
			res.Result = map[string]string{"username": value.Username, "password": value.Password, "email": value.Email, "apiKey": value.APIKey}
		}
	case "info":
		value, err := selected.Info(opCtx)
		if err != nil {
			var operation *testkit.OperationError
			if errors.As(err, &operation) {
				res.Error = b.pairOperationFailure(err)
			} else {
				res.Error = failure("INFO")
			}
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

func emptyParams(data json.RawMessage) bool {
	if len(data) == 0 {
		return true
	}
	var fields map[string]json.RawMessage
	return json.Unmarshal(data, &fields) == nil && fields != nil && len(fields) == 0
}

func (b *bridge) pairOperationFailure(err error) *protocolError {
	b.pairFailed = true
	var operation *testkit.OperationError
	cleanupFailed := false
	reason := string(testkit.StartupReasonSetup)
	if errors.As(err, &operation) {
		cleanupFailed = operation.CleanupError() != nil
		reason = operation.Reason()
	} else {
		cleanup, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		cleanupFailed = b.pair.Close(cleanup) != nil
		cancel()
	}
	if b.appCancel != nil {
		b.appCancel()
	}
	if !cleanupFailed {
		b.pair = nil
	}
	result := failure("OPERATION")
	result.Reason = reason
	if errors.Is(err, context.DeadlineExceeded) {
		result = failure("TIMEOUT")
	} else if errors.Is(err, context.Canceled) {
		result = failure("CANCELED")
	}
	result.CleanupFailed = cleanupFailed
	return result
}
