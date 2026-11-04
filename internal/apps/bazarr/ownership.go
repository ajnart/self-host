package bazarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

type connectionOwnership struct {
	Version  int              `json:"version"`
	Resource string           `json:"resource"`
	Target   string           `json:"target"`
	Endpoint string           `json:"endpoint"`
	Before   connectionFields `json:"before"`
	Applied  connectionFields `json:"applied"`
	Phase    string           `json:"phase"`
}

func (s libraryConnection) ownershipKey(from *helpers.H) string {
	return from.Key("link_" + s.id + "_resource")
}

func (s libraryConnection) ownership(from, to *helpers.H) (connectionOwnership, error) {
	var record connectionOwnership
	raw := from.Secret(s.ownershipKey(from))
	if raw == "" {
		return record, nil
	}
	object, err := settingsObject([]byte(raw))
	before, beforeOK := object["before"].(map[string]any)
	applied, appliedOK := object["applied"].(map[string]any)
	beforeFields, beforeFieldsOK := readConnectionFields(before)
	appliedFields, appliedFieldsOK := readConnectionFields(applied)
	if err != nil || len(object) != 7 || !beforeOK || !appliedOK || len(before) != 5 || len(applied) != 5 || !beforeFieldsOK || !appliedFieldsOK || json.Unmarshal([]byte(raw), &record) != nil || from.App != "bazarr" || record.Version != 1 || record.Resource != "settings."+s.id || record.Target != s.id || record.Endpoint != "http://"+helpers.InternalAddress(appliedFields.IP, appliedFields.Port) || appliedFields.IP != s.id || appliedFields.SSL || appliedFields.BaseURL != "/" || appliedFields.APIKey == "" || !s.vacant(beforeFields) || (record.Phase != "applying" && record.Phase != "configured" && record.Phase != "detach" && record.Phase != "removing") {
		return record, s.manual(from, "The saved singleton ownership is missing required identity or transaction state. Restore only its known matching private record; existing settings were not adopted.")
	}
	if to != nil && (to.App != s.id || record.Endpoint != to.InternalURL()) {
		return record, s.manual(from, "The recorded singleton does not identify this selected target. No connection was changed.")
	}
	from.Log.Secret(record.Applied.APIKey)
	return record, nil
}

func (s libraryConnection) saveOwnership(from *helpers.H, record connectionOwnership) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return from.SetSecret(s.ownershipKey(from), string(raw))
}

func (s libraryConnection) write(ctx context.Context, from *helpers.H, fields connectionFields) error {
	form := url.Values{
		"settings-" + s.id + "-ip":       {fields.IP},
		"settings-" + s.id + "-port":     {strconv.Itoa(fields.Port)},
		"settings-" + s.id + "-base_url": {fields.BaseURL},
		"settings-" + s.id + "-ssl":      {strconv.FormatBool(fields.SSL)},
		"settings-" + s.id + "-apikey":   {fields.APIKey},
	}
	response, err := s.request(ctx, from, form)
	if err != nil || response.Status != http.StatusNoContent {
		return s.manual(from, "The connection settings write did not return a confirmed result. Its pending ownership was retained; inspect the exact attempted connection before retrying, without replacing credentials or adopting current settings.")
	}
	return nil
}

func (s libraryConnection) apply(ctx context.Context, from, to *helpers.H) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	record, err := s.ownership(from, to)
	if err != nil {
		return err
	}
	snapshot, err := s.snapshot(ctx, from)
	if err != nil {
		return err
	}
	desired, err := s.desired(from, to)
	if err != nil {
		return err
	}
	if record.Phase != "" {
		if record.Phase != "configured" {
			return s.manual(from, "A previous connection mutation or removal has a pending outcome. Inspect it and restore matching ownership through an approved Project Backup before retrying; no mutation was repeated.")
		}
		if !snapshot.fields.same(record.Applied) || !desired.same(record.Applied) {
			return s.manual(from, "The recorded connection was edited, or the target's accepted key or Address changed. Reconcile that exact owned connection explicitly; Setup does not overwrite user edits or rotate credentials.")
		}
		return s.verifyConfigured(ctx, from, to, snapshot)
	}
	if !s.vacant(snapshot.fields) {
		return s.manual(from, "An unowned connection already occupies this singleton settings block, even if its Address or key matches the target. It was not adopted or overwritten.")
	}
	if snapshot.enabled {
		return s.manual(from, "This backend is enabled. Saving connection fields can restart SignalR and trigger synchronization or subtitle downloads; explicitly disable only this backend in Bazarr before retrying the connection write.")
	}
	if err := s.emptyLibrary(ctx, from); err != nil {
		return err
	}
	if _, err := s.verifyTarget(ctx, from, to, snapshot); err != nil {
		return err
	}
	record = connectionOwnership{Version: 1, Resource: "settings." + s.id, Target: s.id, Endpoint: to.InternalURL(), Before: snapshot.fields, Applied: desired, Phase: "applying"}
	if err := s.saveOwnership(from, record); err != nil {
		return s.manual(from, "The pending ownership record could not be saved. No settings write was attempted; repair the Project's private credential storage before retrying.")
	}
	current, err := s.snapshot(ctx, from)
	if err != nil || current.fields != snapshot.fields || current.other != snapshot.other {
		return s.manual(from, "Settings changed or could not be reread before mutation. The pending hold was retained; inspect it before retrying. No settings write was attempted.")
	}
	if err := s.write(ctx, from, desired); err != nil {
		return err
	}
	current, err = s.snapshot(ctx, from)
	if err != nil || !current.fields.same(desired) || current.other != snapshot.other {
		return s.manual(from, "The saved connection or preserved settings could not be verified after the write. Keep the pending record for explicit recovery; no replacement or rollback was attempted.")
	}
	record.Phase = "configured"
	if err := s.saveOwnership(from, record); err != nil {
		return s.manual(from, "The connection was written but committed ownership could not be saved. Restore its known matching pending record through the private Project boundary before recovery; do not repeat the write.")
	}
	return s.verifyConfigured(ctx, from, to, current)
}

func (s libraryConnection) remove(ctx context.Context, from, to *helpers.H) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	record, err := s.ownership(from, to)
	if err != nil || record.Phase == "" {
		return err
	}
	current, err := s.snapshot(ctx, from)
	if err != nil {
		return err
	}
	if current.enabled {
		if record.Phase == "configured" && current.fields.same(record.Applied) {
			record.Phase = "detach"
			if err := s.saveOwnership(from, record); err != nil {
				return s.manual(from, "The active connection was left unchanged and its removal intent could not be saved. Repair private storage before retrying scoped cleanup.")
			}
		}
		return s.manual(from, "This backend is enabled. Owned cleanup would restart SignalR with restored settings and could run background work. Explicitly disable only this backend in Bazarr, then retry its owned cleanup with self-host setup bazarr; no enabled choice, library or settings were changed.")
	}
	if record.Phase == "removing" && current.fields.same(record.Before) {
		if err := from.SetSecret(s.ownershipKey(from), ""); err != nil {
			return s.manual(from, "The owned fields are restored but the private removal record could not be cleared. Repair private storage and retry scoped cleanup.")
		}
		return nil
	}
	if (record.Phase != "configured" && record.Phase != "detach") || !current.fields.same(record.Applied) {
		return s.manual(from, "The owned connection has a pending outcome or its fields were edited. It was not disconnected; review only the recorded singleton before retrying scoped cleanup.")
	}
	record.Phase = "removing"
	if err := s.saveOwnership(from, record); err != nil {
		return s.manual(from, "Removal intent could not be saved privately. The connection was left unchanged.")
	}
	before, err := s.snapshot(ctx, from)
	if err != nil || before.fields != current.fields || before.other != current.other {
		return s.manual(from, "Settings changed or could not be reread before cleanup. The removal hold was retained and no settings write was attempted.")
	}
	if err := s.write(ctx, from, record.Before); err != nil {
		return err
	}
	after, err := s.snapshot(ctx, from)
	if err != nil || !after.fields.same(record.Before) || after.other != current.other {
		return s.manual(from, "Restoration of the owned fields and preservation of other settings could not be verified. Keep the removal hold for explicit recovery; no repeated write was attempted.")
	}
	if err := from.SetSecret(s.ownershipKey(from), ""); err != nil {
		return s.manual(from, "The owned connection fields were restored but private ownership could not be cleared. Retry only after repairing private storage.")
	}
	return nil
}

func retryRemovedConnections(ctx context.Context, from *helpers.H) error {
	for _, connection := range libraryConnections() {
		if from.Peer(connection.id) != nil || from.Secret(connection.ownershipKey(from)) == "" {
			continue
		}
		err := from.Step(ctx, "unlink-"+connection.id, "recover the recorded "+connection.name+" connection removal", helpers.StepSpec{
			Do: func(ctx context.Context) error {
				record, err := connection.ownership(from, nil)
				if err != nil {
					return err
				}
				if record.Phase != "detach" && record.Phase != "removing" {
					return connection.manual(from, "The recorded target is absent without a confirmed removal intent. Preserve its connection until its identity and intended removal are explicitly reviewed; no missing target was installed or adopted.")
				}
				return connection.remove(ctx, from, nil)
			},
			Credential: "admin",
		})
		if err != nil && !errors.Is(err, helpers.ErrManual) && !errors.Is(err, helpers.ErrStepFailed) {
			return err
		}
	}
	return nil
}
