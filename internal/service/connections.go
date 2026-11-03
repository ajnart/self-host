package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

const externalConnectionsKey = "HOMARR_EXTERNAL_CONNECTIONS"
const externalConnectionsRecovery = "Review the private external connection record through the Project secret boundary, preserving its opaque IDs and owned bindings, then run self-host setup homarr."

var externalConnectionID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var externalConnectionKind = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]{0,63}$`)

type ConnectionOptions struct {
	File     string
	Approved bool
}

type ConnectionStatus struct {
	ID       string           `json:"id"`
	Kind     string           `json:"kind"`
	Removing bool             `json:"removing,omitempty"`
	Setup    stepstatus.Setup `json:"setup"`
	Steps    []StepStatus     `json:"steps,omitempty"`
	Hint     string           `json:"hint,omitempty"`
}

type externalCredentialRecord struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type externalConnectionInput struct {
	Endpoint    string                     `json:"endpoint"`
	Credentials []externalCredentialRecord `json:"credentials"`
	Selection   map[string]string          `json:"selection"`
}

type externalConnectionRecord struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Removing bool   `json:"removing,omitempty"`
	externalConnectionInput
}

type externalConnectionRecords struct {
	Version     int                        `json:"version"`
	Connections []externalConnectionRecord `json:"connections"`
}

func (c externalConnectionRecord) connection() helpers.ExternalConnection {
	credentials := make([]helpers.ExternalCredential, 0, len(c.Credentials))
	for _, credential := range c.Credentials {
		credentials = append(credentials, helpers.ExternalCredential{Kind: credential.Kind, Value: credential.Value})
	}
	return helpers.ExternalConnection{ID: c.ID, Kind: c.Kind, Endpoint: c.Endpoint, Credentials: credentials, Selection: c.Selection, Removing: c.Removing}
}

func externalConnectionJSONValue(d *json.Decoder, depth int) error {
	if depth > 12 {
		return errors.New("external connection JSON is too deeply nested")
	}
	token, err := d.Token()
	if err != nil {
		return errors.New("invalid external connection JSON")
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return errors.New("invalid external connection JSON")
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate external connection JSON field")
			}
			seen[name] = true
		}
		if err := externalConnectionJSONValue(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func decodeExternalConnectionJSON(raw string, into any) error {
	d := json.NewDecoder(strings.NewReader(raw))
	if err := externalConnectionJSONValue(d, 0); err != nil {
		return errors.New("invalid or duplicate external connection JSON fields")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("external connection input must contain one JSON object")
	}
	d = json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil || strings.TrimSpace(raw) == "null" {
		return errors.New("invalid external connection fields; use only the documented private input fields")
	}
	return nil
}

func (s *Service) registerExternalConnection(c externalConnectionRecord) {
	s.Log.Secret(c.Endpoint)
	if endpoint, err := url.Parse(c.Endpoint); err == nil && endpoint.Host != "" {
		s.Log.Secret(endpoint.Scheme+"://"+endpoint.Host, endpoint.Host, endpoint.Hostname())
	}
	for _, credential := range c.Credentials {
		s.Log.Secret(credential.Value)
	}
	for _, value := range c.Selection {
		s.Log.Secret(value)
	}
}

func (s *Service) registerExternalConnectionFields(raw string) {
	d := json.NewDecoder(strings.NewReader(raw))
	var visit func(int, bool, string) bool
	visit = func(depth int, private bool, field string) bool {
		if depth > 12 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		if value, ok := token.(string); ok {
			if field == "endpoint" {
				s.registerExternalConnection(externalConnectionRecord{externalConnectionInput: externalConnectionInput{Endpoint: value}})
			} else if private || field == "value" {
				s.Log.Secret(value)
			}
			return true
		}
		delim, container := token.(json.Delim)
		if !container {
			return true
		}
		for d.More() {
			name := field
			if delim == '{' {
				key, err := d.Token()
				if err != nil {
					return false
				}
				name, _ = key.(string)
			}
			if !visit(depth+1, private || name == "selection", name) {
				return false
			}
		}
		_, err = d.Token()
		return err == nil
	}
	visit(0, false, "")
}

func (ss *session) externalConnectionRecords() (externalConnectionRecords, error) {
	raw, present := ss.secrets.values[externalConnectionsKey]
	if !present {
		return externalConnectionRecords{Version: 1, Connections: []externalConnectionRecord{}}, nil
	}
	var records externalConnectionRecords
	invalid := func() (externalConnectionRecords, error) {
		return externalConnectionRecords{}, errors.Join(ErrManual, &helpers.ManualError{Hint: externalConnectionsRecovery})
	}
	if len(raw) > 1<<20 || decodeExternalConnectionJSON(raw, &records) != nil || records.Version != 1 || records.Connections == nil || len(records.Connections) > 64 {
		return invalid()
	}
	seen := map[string]bool{}
	for _, c := range records.Connections {
		ss.s.registerExternalConnection(c)
		if !externalConnectionID.MatchString(c.ID) || !externalConnectionKind.MatchString(c.Kind) || seen[c.ID] {
			return invalid()
		}
		seen[c.ID] = true
	}
	return records, nil
}

func (ss *session) externalConnections() ([]helpers.ExternalConnection, error) {
	records, err := ss.externalConnectionRecords()
	if err != nil {
		return nil, err
	}
	connections := make([]helpers.ExternalConnection, 0, len(records.Connections))
	for _, c := range records.Connections {
		connections = append(connections, c.connection())
	}
	return connections, nil
}

func (ss *session) saveExternalConnections(records externalConnectionRecords) error {
	raw, err := json.Marshal(records)
	if err != nil || len(raw) > 1<<20 {
		return errors.New("external connection records exceed the private storage limit")
	}
	return ss.secrets.Set(externalConnectionsKey, string(raw))
}

func (s *Service) readExternalConnection(opts ConnectionOptions) (externalConnectionInput, error) {
	var input externalConnectionInput
	if !opts.Approved {
		return input, errors.New("review the exact private connection input and pass --yes before changing connections")
	}
	if opts.File == "" {
		return input, errors.New("pass --file PATH to an owner-only private JSON input, or --file - for bounded stdin")
	}
	owner, err := s.Owner()
	if err != nil {
		return input, err
	}
	var raw string
	if opts.File == "-" {
		if s.Deps.In == nil {
			return input, errors.New("private connection stdin is unavailable")
		}
		data, err := io.ReadAll(io.LimitReader(s.Deps.In, maxPrivateInputBytes+1))
		if err != nil || len(data) > maxPrivateInputBytes {
			return input, errors.New("cannot read private connection stdin (maximum 64 KiB)")
		}
		raw = strings.TrimRight(string(data), "\r\n")
	} else {
		raw, err = readPrivateInput(opts.File, owner.UID)
		if err != nil {
			return input, err
		}
	}
	s.Log.Secret(raw)
	s.registerExternalConnectionFields(raw)
	if len(raw) > maxPrivateInputBytes || strings.ContainsRune(raw, 0) {
		return input, errors.New("invalid private connection input; maximum 64 KiB and no NUL bytes")
	}
	if err := decodeExternalConnectionJSON(raw, &input); err != nil {
		return input, err
	}
	s.registerExternalConnection(externalConnectionRecord{externalConnectionInput: input})
	u, err := url.Parse(input.Endpoint)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(input.Endpoint, "\r\n\t\\ #") {
		return input, errors.New("the private endpoint must be an absolute HTTP(S) address without embedded credentials, a query or a fragment")
	}
	seen := map[string]bool{}
	for _, credential := range input.Credentials {
		if !externalConnectionKind.MatchString(credential.Kind) || seen[credential.Kind] {
			return input, errors.New("invalid or duplicate external credential kinds")
		}
		seen[credential.Kind] = true
	}
	encoded, err := json.Marshal(input)
	if err != nil || validPrivateValue(string(encoded)) != nil {
		return input, errors.New("private connection fields cannot contain single quotes or exceed 64 KiB")
	}
	return input, nil
}

func (s *Service) externalConnectionConsumer() (app.ExternalConnectionConsumer, error) {
	a, found := s.Catalog.App(dashboardAppID)
	if !found {
		return nil, errors.New("Homarr's external connection support is unavailable in this Catalog")
	}
	consumer, ok := a.(app.ExternalConnectionConsumer)
	if !ok {
		return nil, errors.New("Homarr's external connection support is unavailable in this Catalog")
	}
	return consumer, nil
}

func (s *Service) AddConnection(ctx context.Context, kind string, opts ConnectionOptions) (string, error) {
	if !externalConnectionKind.MatchString(kind) {
		return "", errors.New("use an exact supported external integration kind, never an endpoint or credential argument")
	}
	input, err := s.readExternalConnection(opts)
	if err != nil {
		return "", err
	}
	consumer, err := s.externalConnectionConsumer()
	if err != nil {
		return "", err
	}
	c := externalConnectionRecord{ID: GenerateHex(32), Kind: kind, externalConnectionInput: input}
	if err := consumer.ValidateExternalConnection(c.connection()); err != nil {
		return "", s.Log.RedactError(err)
	}
	saved := false
	err = s.run(ctx, change{title: "configure external connection", edit: func(ctx context.Context, ss *session) error {
		records, err := ss.externalConnectionRecords()
		if err != nil {
			return err
		}
		if len(records.Connections) >= 64 {
			return errors.New("the Project already has the maximum 64 external connections")
		}
		records.Connections = append(records.Connections, c)
		if err := ss.saveExternalConnections(records); err != nil {
			return err
		}
		saved = true
		s.Log.Emit(events.Event{Event: events.Info, App: dashboardAppID, Msg: "saved external connection " + c.ID + " (" + c.Kind + ")"})
		return ss.setupExternalConnections(ctx)
	}})
	if !saved {
		return "", err
	}
	return c.ID, err
}

func (s *Service) UpdateConnection(ctx context.Context, id string, opts ConnectionOptions) error {
	if !externalConnectionID.MatchString(id) {
		return errors.New("use the opaque connection ID from self-host connections status")
	}
	input, err := s.readExternalConnection(opts)
	if err != nil {
		return err
	}
	consumer, err := s.externalConnectionConsumer()
	if err != nil {
		return err
	}
	return s.run(ctx, change{title: "update external connection", edit: func(ctx context.Context, ss *session) error {
		records, err := ss.externalConnectionRecords()
		if err != nil {
			return err
		}
		for i, current := range records.Connections {
			if current.ID != id {
				continue
			}
			if current.Removing {
				return errors.New("this connection is awaiting removal; retry self-host setup homarr before reconnecting")
			}
			if current.Endpoint != input.Endpoint {
				return errors.New("a connection's endpoint and kind are immutable; remove it and add a new connection instead")
			}
			current.externalConnectionInput = input
			if err := consumer.ValidateExternalConnection(current.connection()); err != nil {
				return s.Log.RedactError(err)
			}
			records.Connections[i] = current
			if err := ss.saveExternalConnections(records); err != nil {
				return err
			}
			ss.forgetStep(dashboardAppID, "link-external-"+id)
			if err := ss.p.SaveState(ss.state); err != nil {
				return err
			}
			return ss.setupExternalConnections(ctx)
		}
		return errors.New("external connection not found; run self-host connections status")
	}})
}

func (s *Service) RemoveConnection(ctx context.Context, id string, approved bool) error {
	if !approved {
		return errors.New("approve removing only this connection's recorded Homarr bindings with --yes; Proxmox and other external targets are never changed")
	}
	if !externalConnectionID.MatchString(id) {
		return errors.New("use the opaque connection ID from self-host connections status")
	}
	return s.run(ctx, change{title: "remove external connection", edit: func(ctx context.Context, ss *session) error {
		records, err := ss.externalConnectionRecords()
		if err != nil {
			return err
		}
		for i := range records.Connections {
			if records.Connections[i].ID != id {
				continue
			}
			records.Connections[i].Removing = true
			if err := ss.saveExternalConnections(records); err != nil {
				return err
			}
			if ss.secrets.Get(SecretKey(dashboardAppID, "linked_external-"+id)) == "" {
				return ss.finishExternalConnectionRemovals([]string{id})
			}
			return ss.removeExternalConnection(ctx, records.Connections[i])
		}
		return errors.New("external connection not found; run self-host connections status")
	}})
}

func (ss *session) removeExternalConnection(ctx context.Context, c externalConnectionRecord) error {
	a, found := ss.s.Catalog.App(dashboardAppID)
	if !found || ss.state.Apps[dashboardAppID] == nil {
		return fmt.Errorf("%w: removal is saved; restore Homarr's API and retry self-host setup homarr; no external target credentials or reachability are required", ErrManual)
	}
	consumer, ok := a.(app.ExternalConnectionConsumer)
	if !ok {
		return fmt.Errorf("%w: preserve pending ownership and restore Homarr's external cleanup support", ErrManual)
	}
	h := ss.helpers(a)
	h.ExternalConnections = func() ([]helpers.ExternalConnection, error) { return []helpers.ExternalConnection{c.connection()}, nil }
	removed, err := consumer.SetupExternalConnections(ctx, h)
	ss.reportUnexpected(h, "unlink-external-"+c.ID, err)
	cleanupErr := ss.finishExternalConnectionRemovals(removed)
	if stateErr := ss.p.SaveState(ss.state); stateErr != nil {
		return stateErr
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	if errors.Is(err, helpers.ErrManual) {
		return fmt.Errorf("%w: external removal is pending; follow its recorded Homarr recovery hint", ErrManual)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil {
		return fmt.Errorf("%w: external cleanup did not finish; retry only the pending Homarr cleanup", ErrIncomplete)
	}
	if len(removed) != 1 || removed[0] != c.ID {
		return fmt.Errorf("%w: external cleanup did not confirm the requested pending connection", ErrIncomplete)
	}
	return nil
}

func (ss *session) setupExternalConnections(ctx context.Context) error {
	a, found := ss.s.Catalog.App(dashboardAppID)
	if !found || !ss.ready(dashboardAppID) {
		return fmt.Errorf("%w: external connection saved; finish Homarr's normal Setup, then run self-host setup homarr; no external target was installed or changed", ErrManual)
	}
	incomplete, err := ss.runSetup(ctx, a)
	if err != nil {
		return err
	}
	if len(incomplete) > 0 {
		return fmt.Errorf("%w: Homarr connection Setup needs recovery; follow its recorded hints", ErrIncomplete)
	}
	if ss.state.Apps[dashboardAppID].Setup == stepstatus.SetupManual {
		return fmt.Errorf("%w: follow Homarr's recorded external connection recovery hints", ErrManual)
	}
	return nil
}

func (ss *session) finishExternalConnectionRemovals(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	records, err := ss.externalConnectionRecords()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !slices.ContainsFunc(records.Connections, func(c externalConnectionRecord) bool { return c.ID == id && c.Removing }) {
			return errors.New("external cleanup did not identify a pending owned connection")
		}
		if ss.secrets.Get(SecretKey(dashboardAppID, "linked_external-"+id)) != "" {
			return errors.New("external cleanup still has recorded Homarr integration ownership")
		}
	}
	records.Connections = slices.DeleteFunc(records.Connections, func(c externalConnectionRecord) bool { return slices.Contains(ids, c.ID) })
	if err := ss.saveExternalConnections(records); err != nil {
		return err
	}
	for _, id := range ids {
		ss.forgetStep(dashboardAppID, "link-external-"+id)
		ss.forgetStep(dashboardAppID, "unlink-external-"+id)
	}
	if st := ss.state.Apps[dashboardAppID]; st != nil && st.Setup != stepstatus.SetupPending && st.Setup != "" {
		st.Setup = setupResult(st)
	}
	return nil
}

func (ss *session) connectionStatuses(id string) ([]ConnectionStatus, error) {
	records, err := ss.externalConnectionRecords()
	if err != nil {
		return []ConnectionStatus{{Setup: stepstatus.SetupManual, Hint: externalConnectionsRecovery,
			Steps: []StepStatus{{ID: "link-external-connections", Status: stepstatus.StepManual, Msg: "review external connection ownership", Hint: externalConnectionsRecovery}},
		}}, nil
	}
	statuses := []ConnectionStatus{}
	for _, c := range records.Connections {
		if id != "" && c.ID != id {
			continue
		}
		item := ConnectionStatus{ID: c.ID, Kind: c.Kind, Removing: c.Removing, Setup: stepstatus.SetupPending,
			Hint: "Run self-host setup homarr after its normal Setup is ready. External target lifecycle is never managed."}
		stepID := "link-external-" + c.ID
		if c.Removing {
			stepID = "un" + stepID
			item.Hint = "Removal is pending; restore Homarr's API and retry self-host setup homarr. No external target credentials or target reachability are required."
		}
		if st := ss.state.Apps[dashboardAppID]; st != nil {
			if r, found := st.Steps[stepID]; found {
				item.Steps = []StepStatus{{ID: stepID, Status: r.Status, Msg: ss.s.Log.Redact(r.Msg), Error: ss.s.Log.Redact(r.Error), Hint: ss.s.Log.Redact(r.Hint)}}
				item.Hint = ss.s.Log.Redact(r.Hint)
				switch r.Status {
				case stepstatus.StepOK, stepstatus.StepSkip:
					item.Setup = stepstatus.SetupOK
				case stepstatus.StepManual:
					item.Setup = stepstatus.SetupManual
				case stepstatus.StepFail:
					item.Setup = stepstatus.SetupIncomplete
				}
			}
		}
		statuses = append(statuses, item)
	}
	if id != "" && len(statuses) == 0 {
		return nil, errors.New("external connection not found; run self-host connections status")
	}
	return statuses, nil
}

func (s *Service) Connections(ctx context.Context, id string) ([]ConnectionStatus, error) {
	if id != "" && !externalConnectionID.MatchString(id) {
		return nil, errors.New("use the opaque connection ID from self-host connections status")
	}
	ss, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	return ss.connectionStatuses(id)
}
