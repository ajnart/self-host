package testkit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
)

const sonarrFilterWitnessKind = "sonarr-custom-filter-v1"
const sonarrFilterPath = "/api/v3/customfilter"

var sonarrWitnessVersion = regexp.MustCompile(`^4\.[0-9]+\.[0-9]+(?:\.[0-9]+)?$`)

type SonarrFilterWitness struct {
	Kind          string `json:"kind"`
	ObjectID      int    `json:"objectId"`
	ContentSHA256 string `json:"contentSha256"`
}

type sonarrFilterWitnessState struct {
	attempted bool
	label     string
	imageID   string
	receipt   SonarrFilterWitness
}

type sonarrFilterContent struct {
	Type    string            `json:"type"`
	Label   string            `json:"label"`
	Filters []json.RawMessage `json:"filters"`
}

type sonarrFilterResource struct {
	ID int `json:"id"`
	sonarrFilterContent
}

func sonarrWitnessImage(image string) bool {
	return project.ValidFixtureImage(image) && strings.Contains(image, "@sha256:")
}

func sonarrWitnessFailure(reason StartupReason) error {
	return startupError(reason, "testkit Sonarr filter witness did not verify")
}

func (p *Pair) sonarrWitnessHTTP(ctx context.Context) (*helpers.H, string, string, error) {
	a := p.owner
	if err := ctx.Err(); err != nil {
		return nil, "", "", err
	}
	if !a.pair.options.SonarrFilterWitness || a.pair.options.Consumer != "homarr" || a.pair.options.Target != "sonarr" || !sonarrWitnessImage(a.pair.options.Images["sonarr"]) {
		return nil, "", "", sonarrWitnessFailure(StartupReasonUnsupported)
	}
	marker, err := readOwnership(a.marker.Root)
	if err != nil || marker != a.marker {
		return nil, "", "", sonarrWitnessFailure(StartupReasonOwnership)
	}
	if err := a.guard.verify(ctx); err != nil {
		return nil, "", "", err
	}
	inspection, err := a.inspectPair(ctx)
	if err != nil {
		return nil, "", "", err
	}
	if inspection.Outcome != PairComplete || len(inspection.Apps) != 2 {
		return nil, "", "", sonarrWitnessFailure(StartupReasonSetup)
	}
	for _, app := range inspection.Apps {
		if !app.Installed || app.Setup != "ok" || app.State != "running" {
			return nil, "", "", sonarrWitnessFailure(StartupReasonSetup)
		}
	}
	port := a.pair.ports["sonarr"]
	view := &AppView{owner: a, id: "sonarr"}
	address := "http://127.0.0.1:" + strconv.Itoa(port)
	if port < 1 || port > 65535 || view.URL() != address {
		return nil, "", "", sonarrWitnessFailure(StartupReasonOwnership)
	}
	var targetImage string
	for _, selected := range a.pair.apps {
		if selected.Info().ID == "sonarr" {
			container := a.guard.containers[selected.Info().Service]
			if container.ID == "" || container.ConfiguredImage != a.pair.options.Images["sonarr"] {
				return nil, "", "", sonarrWitnessFailure(StartupReasonOwnership)
			}
			targetImage = container.Image
		}
	}
	if !imageID.MatchString(targetImage) {
		return nil, "", "", sonarrWitnessFailure(StartupReasonOwnership)
	}
	creds, err := a.service.Credentials(ctx)
	if err != nil {
		return nil, "", "", sonarrWitnessFailure(StartupReasonSetup)
	}
	key, err := apiKey(creds, "sonarr", "")
	if err != nil {
		return nil, "", "", sonarrWitnessFailure(StartupReasonSetup)
	}
	h := helpers.New(helpers.Config{App: "sonarr", BaseURL: address, HTTP: a.service.Deps.HTTP, Log: a.service.Log})
	return h, key, targetImage, nil
}

func sonarrWitnessRequest(ctx context.Context, h *helpers.H, key string, req helpers.Request) (helpers.Response, error) {
	req.NoRedirect = true
	req.Header = map[string]string{"X-Api-Key": key}
	response, err := h.HTTP(ctx, req)
	if ctx.Err() != nil {
		return helpers.Response{}, ctx.Err()
	}
	if err != nil || response.Status != 200 && !(req.Method == "POST" && response.Status == 201) {
		return helpers.Response{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	return response, nil
}

func decodeSonarrFilter(data []byte) (sonarrFilterResource, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return sonarrFilterResource{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		name, valid := token.(string)
		if err != nil || !valid || fields[name] != nil {
			return sonarrFilterResource{}, sonarrWitnessFailure(StartupReasonSetup)
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return sonarrFilterResource{}, sonarrWitnessFailure(StartupReasonSetup)
		}
		fields[name] = value
	}
	closing, err := decoder.Token()
	var trailing json.RawMessage
	if err != nil || closing != json.Delim('}') || decoder.Decode(&trailing) != io.EOF || len(fields) != 4 || fields["id"] == nil || fields["type"] == nil || fields["label"] == nil || fields["filters"] == nil {
		return sonarrFilterResource{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	var resource sonarrFilterResource
	if json.Unmarshal(data, &resource) != nil || resource.ID < 1 || int64(resource.ID) > 2147483647 || resource.Type != "series" || resource.Label == "" || resource.Filters == nil || len(resource.Filters) != 0 {
		return sonarrFilterResource{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	return resource, nil
}

func sonarrFilterReceipt(resource sonarrFilterResource) (SonarrFilterWitness, error) {
	content, err := json.Marshal(resource.sonarrFilterContent)
	if err != nil {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	fingerprint := sha256.Sum256(append([]byte("self-host/testkit/sonarr-filter-witness/v1\x00"), content...))
	return SonarrFilterWitness{Kind: sonarrFilterWitnessKind, ObjectID: resource.ID, ContentSHA256: hex.EncodeToString(fingerprint[:])}, nil
}

func (p *Pair) CreateSonarrFilterWitness(ctx context.Context) (_ SonarrFilterWitness, err error) {
	a := p.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.pair.failed {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonOptions)
	}
	defer a.closeOnPairFailure(&err)
	work, cancel := context.WithDeadline(ctx, minTime(a.workDeadline, time.Now().Add(30*time.Second)))
	defer cancel()
	state := &a.pair.sonarrFilterWitness
	if state.attempted {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonOptions)
	}
	h, key, targetImage, err := p.sonarrWitnessHTTP(work)
	if err != nil {
		return SonarrFilterWitness{}, err
	}
	identity, err := sonarrWitnessRequest(work, h, key, helpers.Request{Path: "/api/v3/system/status"})
	if err != nil {
		return SonarrFilterWitness{}, err
	}
	var app struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	if identity.Decode(&app) != nil || app.AppName != "Sonarr" || len(app.Version) > 80 || !sonarrWitnessVersion.MatchString(app.Version) {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonUnsupported)
	}
	before, err := sonarrWitnessRequest(work, h, key, helpers.Request{Path: sonarrFilterPath})
	if err != nil {
		return SonarrFilterWitness{}, err
	}
	var items []json.RawMessage
	if json.Unmarshal(before.Body, &items) != nil || items == nil || len(items) != 0 {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	var nonce [32]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	state.label = "selfhost-witness-" + hex.EncodeToString(nonce[:])
	state.attempted = true
	created, err := sonarrWitnessRequest(work, h, key, helpers.Request{Method: "POST", Path: sonarrFilterPath, JSON: sonarrFilterContent{Type: "series", Label: state.label, Filters: []json.RawMessage{}}})
	if err != nil {
		return SonarrFilterWitness{}, err
	}
	resource, err := decodeSonarrFilter(created.Body)
	if err != nil || resource.Label != state.label {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	readback, err := sonarrWitnessRequest(work, h, key, helpers.Request{Path: sonarrFilterPath + "/" + strconv.Itoa(resource.ID)})
	if err != nil {
		return SonarrFilterWitness{}, err
	}
	observed, err := decodeSonarrFilter(readback.Body)
	if err != nil || observed.ID != resource.ID || observed.Label != state.label {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	after, err := sonarrWitnessRequest(work, h, key, helpers.Request{Path: sonarrFilterPath})
	if err != nil {
		return SonarrFilterWitness{}, err
	}
	if json.Unmarshal(after.Body, &items) != nil || len(items) != 1 {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	only, err := decodeSonarrFilter(items[0])
	if err != nil || only.ID != resource.ID || only.Label != state.label {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	if err := a.guard.verify(work); err != nil {
		return SonarrFilterWitness{}, err
	}
	if a.guard.containers[a.pair.apps[1].Info().Service].Image != targetImage {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonUnsupported)
	}
	if err := work.Err(); err != nil {
		return SonarrFilterWitness{}, err
	}
	state.receipt, err = sonarrFilterReceipt(observed)
	state.imageID = targetImage
	return state.receipt, err
}

func (p *Pair) ReadSonarrFilterWitness(ctx context.Context) (_ SonarrFilterWitness, err error) {
	a := p.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.pair.failed {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonOptions)
	}
	defer a.closeOnPairFailure(&err)
	work, cancel := context.WithDeadline(ctx, minTime(a.workDeadline, time.Now().Add(30*time.Second)))
	defer cancel()
	state := &a.pair.sonarrFilterWitness
	if state.receipt.ObjectID == 0 {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonOptions)
	}
	h, key, targetImage, err := p.sonarrWitnessHTTP(work)
	if err != nil {
		return SonarrFilterWitness{}, err
	}
	if targetImage != state.imageID {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonUnsupported)
	}
	response, err := sonarrWitnessRequest(work, h, key, helpers.Request{Path: sonarrFilterPath + "/" + strconv.Itoa(state.receipt.ObjectID)})
	if err != nil {
		return SonarrFilterWitness{}, err
	}
	resource, err := decodeSonarrFilter(response.Body)
	if err != nil || resource.ID != state.receipt.ObjectID || resource.Label != state.label {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	receipt, err := sonarrFilterReceipt(resource)
	if err != nil || receipt != state.receipt {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonSetup)
	}
	if err := a.guard.verify(work); err != nil {
		return SonarrFilterWitness{}, err
	}
	if a.guard.containers[a.pair.apps[1].Info().Service].Image != state.imageID {
		return SonarrFilterWitness{}, sonarrWitnessFailure(StartupReasonUnsupported)
	}
	if err := work.Err(); err != nil {
		return SonarrFilterWitness{}, err
	}
	return receipt, nil
}
