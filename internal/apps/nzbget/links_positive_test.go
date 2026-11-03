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
		t.Run(consumer.id, func(t *testing.T) {
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
		})
	}
}

func TestNZBGetConsumerProtocolsMatchUpstreamWithoutNormalization(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		t.Run(consumer.id, func(t *testing.T) {
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
			if consumer.id == "lidarr" {
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
			g.template["protocol"] = other
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
		t.Run(scenario.consumer.id+"/"+scenario.actual, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, scenario.consumer)
			f.pathProbe.supported = true
			from := *f.from
			from.App = scenario.actual
			f.secrets[from.Key("api_key")] = f.backend.Key
			if err := f.link.Apply(context.Background(), &from, f.to); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("unsupported consumer/API/category-field combination was accepted", err)
			}
			if f.nativeTests != 0 || f.creates != 0 || f.rpcRequests() != 0 || f.secrets[from.Key("link_nzbget_resource")] != "" || len(f.pathProbe.observed) != 0 {
				t.Fatal("unsupported consumer reached peer/native reads or acquired ownership")
			}
		})
	}
}

func TestNZBGetLeafSharedAndExplicitLegacyMappingsRemainReadOnly(t *testing.T) {
	for _, consumer := range nzbgetConsumerCases {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", consumer.id, legacy), func(t *testing.T) {
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
			t.Run(consumer.id+"/"+prerequisite, func(t *testing.T) {
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

func TestNZBGetLeafRemovalReinstallAndUncertainCreationPreserveOtherClients(t *testing.T) {
	f := newNZBGetLinkFixture(t, nzbgetConsumerCases[0])
	f.pathProbe.supported, f.uncertain = true, true
	if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("uncertain returned identity was accepted", err)
	}
	if err := f.link.Apply(context.Background(), f.from, f.to); !errors.Is(err, helpers.ErrManual) || f.creates != 1 {
		t.Fatal("uncertain creation was duplicated", err)
	}
	for _, consumer := range nzbgetConsumerCases {
		t.Run(consumer.id, func(t *testing.T) {
			f := newNZBGetLinkFixture(t, consumer)
			f.pathProbe.supported = true
			f.backend.SeedDownloadClient(map[string]any{"name": "Independent qBittorrent", "implementation": "QBittorrent", "enable": true, "fields": []any{map[string]any{"name": "host", "value": "qbittorrent"}}})
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			firstID := f.backend.DownloadClients()[1]["id"]
			f.to.Secrets.Set("ADMIN_PASSWORD", "")
			f.to.Config.Exec = nil
			requests, tests, probes := f.rpcRequests(), f.nativeTests, len(f.pathProbe.observed)
			if err := f.link.Remove(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			items := f.backend.DownloadClients()
			if len(items) != 1 || items[0]["name"] != "Independent qBittorrent" || f.deletes != 1 || f.ownership() != "" || f.rpcRequests() != requests || f.nativeTests != tests || len(f.pathProbe.observed) != probes {
				t.Fatal("recorded removal touched target/native prerequisites or another client")
			}
			f.to.Secrets.Set("ADMIN_PASSWORD", password)
			f.to.Config.Exec = f.pathProbe
			if err := f.link.Apply(context.Background(), f.from, f.to); err != nil {
				t.Fatal(err)
			}
			if f.backend.DownloadClients()[1]["id"] == firstID || f.creates != 2 {
				t.Fatal("reinstall reclaimed the removed owned identity")
			}
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
