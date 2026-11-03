package nzbget_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/servarr"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestNZBGetSourceFamiliesKeepAuthenticatedStatusAndOldRecords(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		t.Run(consumer.id+"/"+consumer.version, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, consumer)
			f.pathProbe.supported = true
			ctx := context.Background()
			response, err := f.from.HTTP(ctx, servarr.Request(f.from, consumer.api, helpers.Request{Path: "/system/status", NoRedirect: true}))
			var status struct{ Version string }
			if err != nil || response.Status != 200 || response.Decode(&status) != nil || status.Version != consumer.version {
				t.Fatal("fixture status does not match its independently declared source family", err)
			}
			record := f.seedOwned(t)
			item := f.backend.DownloadClients()[0]
			id := item["id"]
			item["priority"] = float64(11)
			nzbgetSetClientField(item, "addPaused", true)
			for attempt := 0; attempt < 2; attempt++ {
				if err := f.link.Apply(ctx, f.from, f.to); err != nil {
					t.Fatal("source-consistent old-format ownership was rejected", err)
				}
			}
			if f.ownership() != record || f.consumerSecrets.writes != 0 || f.creates != 0 || f.updates != 0 || item["id"] != id || item["protocol"] != consumer.protocol || item["priority"] != float64(11) || arrtest.Field(item, "addPaused") != true || f.nativeTests != 2 {
				t.Fatal("rerun migrated the old record, DTO literal, ID or options")
			}
		})
	}
}

func TestNZBGetProtocolPreflightFaultsCannotReachTargetOrWrites(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		for _, operation := range []string{"create", "apply-owned", "remove-owned"} {
			for _, fault := range []string{
				"missing-key", "rejected-key", "schema-401", "schema-403", "schema-false", "schema-object", "schema-malformed", "schema-null", "schema-empty", "schema-duplicate", "schema-contract", "schema-fields", "schema-port", "schema-password", "schema-category",
				"protocol-null", "protocol-number", "protocol-bool", "protocol-empty", "protocol-unknown", "protocol-case", "protocol-space", "protocol-torrent", "protocol-new-torrent", "schema-crossed", "row-crossed",
				"resources-401", "resources-false", "resources-malformed",
			} {
				if operation == "create" && (fault == "schema-crossed" || fault == "row-crossed") {
					continue
				}
				t.Run(consumer.id+"/"+consumer.version+"/"+operation+"/"+fault, func(t *testing.T) {
					f := newNZBGetLinkFixture(t, consumer)
					f.pathProbe.supported = true
					if operation != "create" {
						f.seedOwned(t)
					}
					other := "UsenetDownloadProtocol"
					if consumer.protocol == "UsenetDownloadProtocol" {
						other = "usenet"
					}
					body := ""
					switch fault {
					case "missing-key":
						f.secrets[f.from.Key("api_key")] = ""
					case "rejected-key":
						f.secrets[f.from.Key("api_key")] = "private-rejected-key"
					case "schema-401":
						f.schemaStatus = 401
					case "schema-403":
						f.schemaStatus = 403
					case "schema-false":
						body, f.schemaBody = "false", &body
					case "schema-object":
						body, f.schemaBody = `{"error":"private-response"}`, &body
					case "schema-malformed":
						body, f.schemaBody = "{private-response", &body
					case "schema-null":
						body, f.schemaBody = "null", &body
					case "schema-empty":
						f.schemas = []map[string]any{}
					case "schema-duplicate":
						f.schemas = []map[string]any{f.template, f.template}
					case "schema-contract":
						f.template["configContract"] = "NZBGetSettings"
					case "schema-fields":
						f.template["fields"] = f.template["fields"].([]any)[1:]
					case "schema-port":
						nzbgetSetClientField(f.template, "port", "6789")
					case "schema-password":
						nzbgetSetClientField(f.template, "password", false)
					case "schema-category":
						nzbgetSetClientField(f.template, consumer.categoryField, float64(1))
					case "protocol-null":
						f.template["protocol"] = nil
					case "protocol-number":
						f.template["protocol"] = float64(1)
					case "protocol-bool":
						f.template["protocol"] = true
					case "protocol-empty":
						f.template["protocol"] = ""
					case "protocol-unknown":
						f.template["protocol"] = "unknown"
					case "protocol-case":
						f.template["protocol"] = strings.ToUpper(consumer.protocol)
					case "protocol-space":
						f.template["protocol"] = consumer.protocol + " "
					case "protocol-torrent":
						f.template["protocol"] = "torrent"
					case "protocol-new-torrent":
						f.template["protocol"] = "TorrentDownloadProtocol"
					case "schema-crossed":
						f.template["protocol"] = other
					case "row-crossed":
						f.backend.DownloadClients()[0]["protocol"] = other
					case "resources-401":
						f.resourceStatus = 401
					case "resources-false":
						body, f.resourceBody = "false", &body
					case "resources-malformed":
						body, f.resourceBody = "{private-response", &body
					}
					record, writes := f.ownership(), f.consumerSecrets.writes
					before, err := json.Marshal(f.backend.DownloadClients())
					if err != nil {
						t.Fatal(err)
					}
					if operation == "remove-owned" {
						err = f.link.Remove(context.Background(), f.from, f.to)
					} else {
						err = f.link.Apply(context.Background(), f.from, f.to)
					}
					if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private-") {
						t.Fatal("invalid consumer preflight was accepted or disclosed", err)
					}
					after, err := json.Marshal(f.backend.DownloadClients())
					if err != nil {
						t.Fatal(err)
					}
					if f.rpcRequests() != 0 || len(f.pathProbe.observed) != 0 || f.nativeTests != 0 || f.creates != 0 || f.updates != 0 || f.deletes != 0 || f.consumerSecrets.writes != writes || f.targetSecrets.reads != 0 || f.targetSecrets.writes != 0 || f.ownership() != record || string(before) != string(after) {
						t.Fatal("invalid preflight reached the target, native test, resource or ownership mutation")
					}
				})
			}
		}
	}
}

func TestNZBGetProtocolResponseMismatchPreservesUncertaintyAndFingerprint(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		if consumer.id != "lidarr" {
			continue
		}
		for _, update := range []bool{false, true} {
			operation := "create"
			if update {
				operation = "update"
			}
			t.Run(consumer.version+"/"+operation, func(t *testing.T) {
				f := newNZBGetLinkFixture(t, consumer)
				f.pathProbe.supported = true
				ctx := context.Background()
				var id any
				if update {
					if err := f.link.Apply(ctx, f.from, f.to); err != nil {
						t.Fatal(err)
					}
					item := f.backend.DownloadClients()[0]
					id, item["priority"] = item["id"], float64(11)
					nzbgetSetClientField(item, "addPaused", true)
					f.to.Secrets.Set("ADMIN_PASSWORD", "renewed-accepted-control-password")
					f.targetSettings.Store("rpc_password", "renewed-accepted-control-password")
					f.config("ControlPassword", "renewed-accepted-control-password")
				}
				record := f.ownership()
				f.responseProtocol = "UsenetDownloadProtocol"
				if consumer.protocol == "UsenetDownloadProtocol" {
					f.responseProtocol = "usenet"
				}
				if err := f.link.Apply(ctx, f.from, f.to); err == nil {
					t.Fatal("crossed response protocol advanced ownership")
				}
				items := f.backend.DownloadClients()
				if len(items) != 1 || items[0]["protocol"] != consumer.protocol || f.creates != 1 || f.deletes != 0 {
					t.Fatal("response mismatch rewrote or undid the submitted server resource")
				}
				if update {
					if f.ownership() != record || f.updates != 1 || items[0]["id"] != id || items[0]["priority"] != float64(11) || arrtest.Field(items[0], "addPaused") != true || arrtest.Field(items[0], "password") != "renewed-accepted-control-password" || len(f.updatedItems) != 1 || f.updatedItems[0]["protocol"] != consumer.protocol {
						t.Fatal("update response mismatch advanced the fingerprint or changed noncredential choices")
					}
					return
				}
				var pending map[string]any
				if json.Unmarshal([]byte(f.ownership()), &pending) != nil || pending["id"] != float64(0) || len(pending) != 4 {
					t.Fatal("uncertain creation acquired an ID or lost its existing intent format")
				}
				intent, requests, native := f.ownership(), f.rpcRequests(), f.nativeTests
				if err := f.link.Apply(ctx, f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.creates != 1 || f.ownership() != intent || f.rpcRequests() != requests || f.nativeTests != native {
					t.Fatal("uncertain creation was retried or adopted", err)
				}
			})
		}
	}
}

func TestNZBGetProtocolRemovalWithoutOwnershipDiscoversNothing(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		t.Run(consumer.id+"/"+consumer.version, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, consumer)
			f.secrets[f.from.Key("api_key")] = ""
			body := "false"
			f.schemaBody, f.resourceBody = &body, &body
			if err := f.link.Remove(context.Background(), f.from, f.to); err != nil {
				t.Fatal("no-ownership removal depended on consumer or target discovery", err)
			}
			if f.schemaReads != 0 || f.resourceReads != 0 || f.rpcRequests() != 0 || f.targetSecrets.reads != 0 || f.targetSecrets.writes != 0 || f.consumerSecrets.writes != 0 || f.nativeTests != 0 || f.deletes != 0 {
				t.Fatal("no-ownership removal discovered or mutated a resource")
			}
		})
	}
}

func TestNZBGetAbsentOwnedRowRequiresVerifiedSchemaBeforeClearingRecord(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		t.Run(consumer.id+"/"+consumer.version, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, consumer)
			record := f.seedOwned(t)
			body := "[]"
			f.resourceBody, f.schemaStatus = &body, 503
			if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("missing owned row with unavailable schema reached apply", err)
			}
			if err := f.link.Remove(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("missing owned row bypassed consumer schema verification", err)
			}
			if f.ownership() != record || f.consumerSecrets.writes != 0 || f.targetSecrets.reads != 0 || f.targetSecrets.writes != 0 || f.rpcRequests() != 0 || f.nativeTests != 0 || f.creates != 0 || f.updates != 0 || f.deletes != 0 || len(f.pathProbe.observed) != 0 {
				t.Fatal("unverified absence changed ownership or reached target operations")
			}
		})
	}
}

func TestNZBGetFreshSelectionHoldsBeforeTargetAccess(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		for _, scenario := range []string{"same-family", "same-family-crossed", "missing-enable", "missing-schema", "duplicate-schema", "crossed-protocol", "field-type"} {
			t.Run(consumer.id+"/"+consumer.version+"/"+scenario, func(t *testing.T) {
				f := newNZBGetLinkFixture(t, consumer)
				f.pathProbe.supported = true
				incumbent := nzbgetSurvivor(consumer)
				if scenario == "same-family" || scenario == "same-family-crossed" {
					data, err := json.Marshal(f.template)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(data, &incumbent); err != nil {
						t.Fatal(err)
					}
					incumbent["name"], incumbent["priority"], incumbent["tags"] = "Personal Usenet", float64(7), []any{float64(3)}
					nzbgetSetClientField(incumbent, "host", "personal-nzbget")
				}
				switch scenario {
				case "same-family-crossed":
					incumbent["protocol"] = "UsenetDownloadProtocol"
					if consumer.protocol == "UsenetDownloadProtocol" {
						incumbent["protocol"] = "usenet"
					}
				case "missing-enable":
					delete(incumbent, "enable")
				case "missing-schema":
					f.schemas = []map[string]any{f.template}
				case "duplicate-schema":
					f.schemas = append(f.schemas, nzbgetSurvivor(consumer))
				case "crossed-protocol":
					incumbent["protocol"] = "TorrentDownloadProtocol"
					if consumer.survivorProtocol == "TorrentDownloadProtocol" {
						incumbent["protocol"] = "torrent"
					}
				case "field-type":
					nzbgetSetClientField(incumbent, "port", "8080")
				}
				f.backend.SeedDownloadClient(incumbent)
				before, err := json.Marshal(f.backend.DownloadClients())
				if err != nil {
					t.Fatal(err)
				}
				if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "enabled competing client") {
					t.Fatal("fresh selection ambiguity reached the leaf instead of its Manual hold", err)
				}
				after, err := json.Marshal(f.backend.DownloadClients())
				if err != nil || string(after) != string(before) || f.targetSecrets.reads != 0 || f.targetSecrets.writes != 0 || f.consumerSecrets.writes != 0 || f.rpcRequests() != 0 || f.nativeTests != 0 || len(f.pathProbe.observed) != 0 || f.creates != 0 || f.updates != 0 || f.deletes != 0 || f.ownership() != "" {
					t.Fatal("fresh selection hold changed inventory/authority or reached target credentials/RPC/probes/native tests", err)
				}
			})
		}
	}
}
