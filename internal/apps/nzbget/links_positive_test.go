package nzbget_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/servarr"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

func (f *nzbgetLinkFixture) ownership() string {
	return f.secrets[f.from.Key("link_nzbget_resource")]
}

func (f *nzbgetLinkFixture) config(name, value string) {
	stored, _ := f.targetSettings.Load("config")
	f.targetSettings.Store("config", nzbgetDownloadSet(stored.([]map[string]string), name, value))
}

func (f *nzbgetLinkFixture) rpcRequests() int64 {
	value, _ := f.targetSettings.Load("rpc_requests")
	return value.(*atomic.Int64).Load()
}

func TestNZBGetLinksAllConsumersPreserveIdentityChoicesAndAcceptedPasswordRecovery(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		t.Run(consumer.id+"/"+consumer.version, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, consumer)
			f.pathProbe.supported = true
			f.from.Settings["MEDIA_LAYOUT"], f.to.Settings["MEDIA_LAYOUT"] = "shared", "shared"
			if consumer.id == "radarr" {
				f.nativeStatus = 204
			}
			ctx := context.Background()
			if err := f.link.Apply(ctx, f.from, f.to); err != nil {
				t.Fatal(err)
			}
			items := f.backend.DownloadClients()
			if len(items) != 1 || items[0]["protocol"] != consumer.protocol || f.creates != 1 || f.nativeTests != 2 || f.pathProbe.wrongIdentity != 0 || f.pathProbe.targets != 2 || f.pathProbe.consumers != 4 || arrtest.Field(items[0], "port") != float64(6789) || arrtest.Field(items[0], "username") != "admin" || arrtest.Field(items[0], "password") != password {
				t.Fatal("provider identity, internal Address, accepted Admin credential or guarded native tests were incorrect")
			}
			firstID, firstRecord := items[0]["id"], f.ownership()
			var record map[string]any
			if json.Unmarshal([]byte(firstRecord), &record) != nil || len(record) != 4 || record["target"] != "nzbget" || record["endpoint"] != "nzbget" {
				t.Fatal("shared serialized ownership format changed")
			}
			nzbgetSetClientField(items[0], consumer.categoryField, "Music")
			items[0]["priority"] = float64(7)
			nzbgetSetClientField(items[0], "addPaused", true)
			f.config("Category3.DestDir", "/data/usenet/complete/kept-music")
			if err := f.link.Apply(ctx, f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.creates != 1 || f.updates != 0 || f.nativeTests != 3 || f.ownership() != firstRecord || items[0]["priority"] != float64(7) || arrtest.Field(items[0], "addPaused") != true || arrtest.Field(items[0], consumer.categoryField) != "Music" {
				t.Fatal("rerun changed ownership, category, priority or noncredential settings")
			}
			for _, observed := range []string{"nzbget:/data/usenet/complete/kept-music", consumer.id + ":/data/usenet/complete/kept-music", consumer.id + ":/data/media/library"} {
				if !slices.Contains(f.pathProbe.observed, observed) {
					t.Fatal("stored category destination or selected library was not verified")
				}
			}
			f.mask = true
			if err := f.link.Apply(ctx, f.from, f.to); err != nil || f.updates != 0 || f.nativeTests != 4 {
				t.Fatal("masked same-credential rerun failed", err)
			}
			f.to.Secrets.Set("ADMIN_PASSWORD", "renewed-accepted-control-password")
			f.targetSettings.Store("rpc_password", "renewed-accepted-control-password")
			f.config("ControlPassword", "renewed-accepted-control-password")
			if err := f.link.Apply(ctx, f.from, f.to); err != nil {
				t.Fatal(err)
			}
			current := f.backend.DownloadClients()[0]
			if current["id"] != firstID || current["protocol"] != consumer.protocol || f.updates != 1 || f.creates != 1 || arrtest.Field(current, "password") != "renewed-accepted-control-password" || arrtest.Field(current, consumer.categoryField) != "Music" || current["priority"] != float64(7) || arrtest.Field(current, "addPaused") != true {
				t.Fatal("accepted password recovery replaced the client or noncredential choices")
			}
			for _, requests := range [][]map[string]any{f.nativeItems, f.createdItems, f.updatedItems} {
				for _, item := range requests {
					if item["protocol"] != consumer.protocol {
						t.Fatal("a submitted DTO changed the schema's exact protocol")
					}
				}
			}
		})
	}
}

func TestNZBGetConsumerProtocolsMatchUpstreamWithoutNormalization(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		t.Run(consumer.id+"/"+consumer.version, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, consumer)
			f.pathProbe.supported = true
			if f.template["protocol"] != consumer.protocol {
				t.Fatal("fixture does not model the pinned upstream protocol")
			}
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal("actual consumer protocol rejected", err)
			}
			client := f.backend.DownloadClients()[0]
			if client["protocol"] != consumer.protocol || f.nativeTests != 2 {
				t.Fatal("actual protocol was normalized or native verification was skipped")
			}
			other := "UsenetDownloadProtocol"
			if consumer.protocol == "UsenetDownloadProtocol" {
				other = "usenet"
			}
			owned, native, requests := f.ownership(), f.nativeTests, f.rpcRequests()
			client["protocol"] = other
			if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("wrong stored protocol accepted", err)
			}
			if f.nativeTests != native || f.rpcRequests() != requests || f.updates != 0 || f.ownership() != owned {
				t.Fatal("wrong stored protocol reached peer/native reads or changed ownership")
			}
			g := newNZBGetLinkFixture(t, consumer)
			g.pathProbe.supported = true
			g.template["protocol"] = "TorrentDownloadProtocol"
			if err := g.link.Apply(context.Background(), g.from, g.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("wrong schema protocol accepted", err)
			}
			if g.nativeTests != 0 || g.creates != 0 || g.rpcRequests() != 0 || g.ownership() != "" {
				t.Fatal("wrong schema protocol reached peer/native reads or acquired ownership")
			}
		})
	}
}

func TestNZBGetRejectsUnsupportedActualConsumerContractsBeforePeerReads(t *testing.T) {
	for _, scenario := range []struct {
		consumer nzbgetConsumerCase
		actual   string
	}{
		{nzbgetConsumerCases[0], "readarr"},
		{nzbgetConsumerCases[0], "radarr"},
		{nzbgetConsumerCases[0], "lidarr"},
		{nzbgetConsumerCases[2], "sonarr"},
	} {
		for _, removing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/remove=%t", scenario.consumer.id, scenario.actual, removing), func(t *testing.T) {
				f := newNZBGetLinkFixture(t, scenario.consumer)
				f.pathProbe.supported = true
				from := *f.from
				from.App = scenario.actual
				f.secrets[from.Key("api_key")] = f.backend.Key
				record := ""
				var err error
				if removing {
					record = f.seedOwned(t)
					f.secrets[from.Key("link_nzbget_resource")] = record
					err = f.link.Remove(context.Background(), &from, f.to)
				} else {
					err = f.link.Apply(context.Background(), &from, f.to)
				}
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatal("unsupported consumer/API/category-field combination was accepted", err)
				}
				if f.nativeTests != 0 || f.creates != 0 || f.deletes != 0 || f.rpcRequests() != 0 || f.targetSecrets.reads != 0 || f.targetSecrets.writes != 0 || f.consumerSecrets.writes != 0 || f.secrets[from.Key("link_nzbget_resource")] != record || len(f.pathProbe.observed) != 0 {
					t.Fatal("unsupported consumer reached the target or changed ownership/resources")
				}
			})
		}
	}
}

func TestNZBGetLeafSharedAndExplicitLegacyMappingsRemainReadOnly(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/legacy=%t", consumer.id, consumer.version, legacy), func(t *testing.T) {
				f := newNZBGetLinkFixture(t, consumer)
				f.pathProbe.supported = true
				if legacy {
					f.config("DestDir", "/downloads/completed")
					f.pathProbe.remote, f.pathProbe.local = "/downloads/completed", "/legacy/nzbget"
					f.mappings = []map[string]any{{"id": 1, "host": "NZBGET", "remotePath": "/downloads/completed", "localPath": "/legacy/nzbget"}}
					library := "/media/library"
					if consumer.id == "lidarr" {
						library = "/data/media/music"
					}
					f.roots = []map[string]any{{"id": 1, "path": library}}
				}
				if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
					t.Fatal("compatible controlled views failed", err)
				}
				id, tests, record := f.backend.DownloadClients()[0]["id"], f.nativeTests, f.ownership()
				f.pathProbe.distinct = true
				if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("different backing directory was accepted", err)
				}
				if f.nativeTests != tests || f.backend.DownloadClients()[0]["id"] != id || f.updates != 0 || f.ownership() != record {
					t.Fatal("incompatible path verification reached native tests or mutation")
				}
			})
		}
	}
}

func TestNZBGetLeafManualPathPrerequisitesPrecedeNativeTests(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		for _, prerequisite := range []string{"library", "target-access", "consumer-access", "overlap", "mapping-host", "missing-wrapper"} {
			t.Run(consumer.id+"/"+consumer.version+"/"+prerequisite, func(t *testing.T) {
				f := newNZBGetLinkFixture(t, consumer)
				f.pathProbe.supported = true
				switch prerequisite {
				case "library":
					f.missingRoots = true
				case "target-access":
					f.pathProbe.denied = "nzbget:/data/usenet/complete/" + consumer.category
				case "consumer-access":
					f.pathProbe.denied = consumer.id + ":/data/usenet/complete/" + consumer.category
				case "overlap":
					f.pathProbe.overlap = true
				case "mapping-host":
					f.pathProbe.local = "/legacy/nzbget"
					f.mappings = []map[string]any{{"id": 1, "host": "nzbget.", "remotePath": "/data/usenet/complete", "localPath": "/legacy/nzbget"}}
				case "missing-wrapper":
					f.pathProbe.supported = false
				}
				if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("unsafe path prerequisite was accepted", err)
				}
				if f.creates != 0 || f.nativeTests != 0 || f.ownership() != "" || f.recorder.Status("link-nzbget") != stepstatus.StepManual {
					t.Fatal("unsafe path prerequisite reached native testing or ownership writes")
				}
			})
		}
	}
}

func TestNZBGetCategorySanitizerBranchGuards(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		for _, owned := range []bool{false, true} {
			operation := "create"
			if owned {
				operation = "owned"
			}
			for _, branch := range []string{"append", "no-append", "explicit-destination"} {
				for _, tc := range []struct {
					category string
					manual   bool
				}{
					{"tv:hd", true}, {"tv.", true}, {"CoN.hd", true}, {"lPt9", true}, {"TV-HD", false}, {"Tv+HD (1080p)", false},
				} {
					t.Run(consumer.id+"/"+consumer.version+"/"+operation+"/"+branch+"/"+tc.category, func(t *testing.T) {
						f := newNZBGetLinkFixture(t, consumer)
						f.pathProbe.supported = true
						f.link = servarr.NZBGetLink(consumer.api, consumer.categoryField, tc.category)
						f.config("Category1.Name", tc.category)
						want := "/data/usenet/complete/" + tc.category
						switch branch {
						case "no-append":
							f.config("AppendCategoryDir", "no")
							want = "/data/usenet/complete"
						case "explicit-destination":
							f.config("Category1.DestDir", "/data/usenet/complete/chosen/")
							want = "/data/usenet/complete/chosen"
						}
						if owned {
							f.seedOwned(t)
							item := f.backend.DownloadClients()[0]
							nzbgetSetClientField(item, consumer.categoryField, tc.category)
							nzbgetSetClientField(item, "addPaused", true)
							item["priority"] = float64(17)
						}
						record, writes := f.ownership(), f.consumerSecrets.writes
						before, err := json.Marshal(f.backend.DownloadClients())
						if err != nil {
							t.Fatal(err)
						}
						active, _ := f.targetSettings.Load("config")
						settingsBefore, err := json.Marshal(active)
						if err != nil {
							t.Fatal(err)
						}
						err = f.link.Apply(context.Background(), f.from, f.to)
						if tc.manual && branch == "append" {
							if !errors.Is(err, helpers.ErrManual) {
								t.Fatalf("sanitizer-sensitive category passed decoy verbatim folder: native=%d target-probes=%d consumer-probes=%d error=%v", f.nativeTests, f.pathProbe.targets, f.pathProbe.consumers, err)
							}
							after, marshalErr := json.Marshal(f.backend.DownloadClients())
							if marshalErr != nil || string(after) != string(before) || f.ownership() != record || f.consumerSecrets.writes != writes || f.targetSecrets.writes != 0 || f.creates != 0 || f.updates != 0 || f.deletes != 0 || f.nativeTests != 0 || len(f.pathProbe.observed) != 0 || f.pathProbe.wrongIdentity != 0 || f.recorder.Status("link-nzbget") != stepstatus.StepManual {
								t.Fatal("sanitizer hold reached path/native probes or changed an owned resource/settings", marshalErr)
							}
						} else {
							if err != nil || f.pathProbe.directory != want || f.nativeTests == 0 {
								t.Fatal("source-supported branch failed guarded native verification", err)
							}
							item := f.backend.DownloadClients()[0]
							if item["protocol"] != consumer.protocol || arrtest.Field(item, consumer.categoryField) != tc.category || owned && (f.ownership() != record || f.creates != 0 || f.updates != 0 || item["priority"] != float64(17) || arrtest.Field(item, "addPaused") != true) {
								t.Fatal("accepted category branch changed the literal, ownership or user options")
							}
						}
						active, _ = f.targetSettings.Load("config")
						settingsAfter, err := json.Marshal(active)
						if err != nil || string(settingsAfter) != string(settingsBefore) || f.targetSecrets.writes != 0 {
							t.Fatal("Link changed active category/path settings or target credentials", err)
						}
					})
				}
			}
		}
	}
}

func TestNZBGetLeafRemovalReinstallAndUncertainCreationPreserveOtherClients(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		t.Run(consumer.id+"/"+consumer.version, func(t *testing.T) {
			g := newNZBGetLinkFixture(t, consumer)
			g.pathProbe.supported, g.uncertain = true, true
			if err := g.link.Apply(context.Background(), g.from, g.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("uncertain returned identity was accepted", err)
			}
			pending, requests := g.ownership(), g.rpcRequests()
			schemas, resources, native, targetReads := g.schemaReads, g.resourceReads, g.nativeTests, g.targetSecrets.reads
			intentWrites, targetWrites := g.consumerSecrets.writes, g.targetSecrets.writes
			if err := g.link.Apply(context.Background(), g.from, g.to); !errors.Is(err, helpers.ErrManual) || g.creates != 1 || g.ownership() != pending || g.rpcRequests() != requests || g.schemaReads != schemas || g.resourceReads != resources || g.nativeTests != native || g.targetSecrets.reads != targetReads || g.consumerSecrets.writes != intentWrites || g.targetSecrets.writes != targetWrites {
				t.Fatal("uncertain creation was duplicated or its intent was changed", err)
			}
			f := newNZBGetLinkFixture(t, consumer)
			f.pathProbe.supported = true
			f.backend.SeedDownloadClient(nzbgetSurvivor(consumer))
			survivor := f.backend.DownloadClients()[0]
			nzbgetSetClientField(survivor, "host", "qbittorrent")
			nzbgetSetClientField(survivor, "username", "fixture-admin")
			nzbgetSetClientField(survivor, "password", "fixture-qb-password")
			survivorData, err := json.Marshal(survivor)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			firstID := f.backend.DownloadClients()[1]["id"]
			data, err := json.Marshal(f.backend.DownloadClients()[1])
			if err != nil {
				t.Fatal(err)
			}
			var personal map[string]any
			if err := json.Unmarshal(data, &personal); err != nil {
				t.Fatal(err)
			}
			personal["name"], personal["priority"] = "Personal NZBGet", float64(11)
			nzbgetSetClientField(personal, "host", "personal-nzbget")
			f.backend.SeedDownloadClient(personal)
			personalID := personal["id"]
			personalData, err := json.Marshal(personal)
			if err != nil {
				t.Fatal(err)
			}
			ownedRecord := f.ownership()
			for _, masked := range []bool{false, true} {
				f.mask = masked
				if err := f.link.Apply(context.Background(), f.from, f.to); err != nil || f.ownership() != ownedRecord || f.creates != 1 || f.updates != 0 {
					t.Fatal("recorded owned multi-client rerun was treated as fresh selection", err)
				}
			}
			f.mask = false
			preserved, err := json.Marshal(personal)
			if err != nil || string(preserved) != string(personalData) {
				t.Fatal("recorded owned rerun changed a distinct enabled same-family survivor", err)
			}
			nzbgetSetClientField(personal, "host", "nzbget")
			personalData, err = json.Marshal(personal)
			if err != nil {
				t.Fatal(err)
			}
			f.to.Secrets.Set("ADMIN_PASSWORD", "")
			f.to.Config.Exec = nil
			requests, tests, probes := f.rpcRequests(), f.nativeTests, len(f.pathProbe.observed)
			reads, writes := f.targetSecrets.reads, f.targetSecrets.writes
			if err := f.link.Remove(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			items := f.backend.DownloadClients()
			if len(items) != 2 || items[0]["id"] != survivor["id"] || items[0]["protocol"] != consumer.survivorProtocol || items[1]["id"] != personalID || items[1]["priority"] != float64(11) || f.deletes != 1 || f.ownership() != "" || f.rpcRequests() != requests || f.nativeTests != tests || len(f.pathProbe.observed) != probes || f.targetSecrets.reads != reads || f.targetSecrets.writes != writes {
				t.Fatal("recorded removal touched target/native prerequisites or another client")
			}
			for index, want := range [][]byte{survivorData, personalData} {
				got, err := json.Marshal(items[index])
				if err != nil || string(got) != string(want) || items[index]["enable"] != true {
					t.Fatal("owned reruns/removal changed a complete enabled survivor DTO", err)
				}
			}
			if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.creates != 1 || f.rpcRequests() != requests {
				t.Fatal("reinstall adopted or replaced the personal same-target resource", err)
			}
			nzbgetSetClientField(personal, "host", "personal-nzbget")
			f.to.Secrets.Set("ADMIN_PASSWORD", password)
			f.to.Config.Exec = f.pathProbe
			before, err := json.Marshal(f.backend.DownloadClients())
			if err != nil {
				t.Fatal(err)
			}
			reads, writes = f.targetSecrets.reads, f.targetSecrets.writes
			consumerWrites := f.consumerSecrets.writes
			if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "enabled competing client") {
				t.Fatal("an endpoint edit was misread as permission for same-family creation", err)
			}
			after, err := json.Marshal(f.backend.DownloadClients())
			if err != nil || string(after) != string(before) || f.rpcRequests() != requests || f.nativeTests != tests || len(f.pathProbe.observed) != probes || f.targetSecrets.reads != reads || f.targetSecrets.writes != writes || f.consumerSecrets.writes != consumerWrites || f.ownership() != "" || f.creates != 1 || f.updates != 0 || f.deletes != 1 {
				t.Fatal("same-family reinstall hold reached the target or changed authority/survivors", err)
			}
			t.Run("explicit-person-choice", func(t *testing.T) {
				personal["enable"] = false
				personalChoice, err := json.Marshal(personal)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
					t.Fatal("explicit person choice did not permit fresh guarded creation", err)
				}
				items = f.backend.DownloadClients()
				if len(items) != 3 || items[2]["id"] == firstID || items[2]["protocol"] != consumer.protocol || items[0]["id"] != survivor["id"] || items[1]["id"] != personalID || f.creates != 2 {
					t.Fatal("explicit-choice reinstall reclaimed the removed owned identity")
				}
				for index, want := range [][]byte{survivorData, personalChoice} {
					got, err := json.Marshal(items[index])
					if err != nil || string(got) != string(want) || items[0]["enable"] != true {
						t.Fatal("explicit-choice reinstall changed survivor DTOs beyond the person's choice", err)
					}
				}
			})
		})
	}
}

func TestNZBGetLeafUserEditsAndNativeErrorsCannotReplaceOwnedResources(t *testing.T) {
	for _, scenario := range []string{"password", "port", "ssl", "disabled", "lost-ownership", "corrupt-ownership", "native-auth", "native-body"} {
		t.Run(scenario, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, nzbgetConsumerCases[0])
			f.pathProbe.supported = true
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			item := f.backend.DownloadClients()[0]
			switch scenario {
			case "password":
				nzbgetSetClientField(item, "password", "person-edited-password")
			case "port":
				nzbgetSetClientField(item, "port", float64(16000))
			case "ssl":
				nzbgetSetClientField(item, "useSsl", true)
			case "disabled":
				item["enable"] = false
			case "lost-ownership":
				f.secrets[f.from.Key("link_nzbget_resource")] = ""
			case "corrupt-ownership":
				f.secrets[f.from.Key("link_nzbget_resource")] = `{"id":null}`
			case "native-auth":
				f.rejectNative = true
			case "native-body":
				f.nativeBody = `{"error":"private-native-response"}`
			}
			before := f.ownership()
			if err := f.link.Apply(context.Background(), f.from, f.to); err == nil || strings.Contains(err.Error(), "private-native-response") || strings.Contains(err.Error(), "person-edited-password") {
				t.Fatal("edited/rejected resource was accepted or disclosed", err)
			}
			if f.creates != 1 || f.updates != 0 || f.ownership() != before {
				t.Fatal("failure replaced the resource or changed its ownership")
			}
		})
	}
}
