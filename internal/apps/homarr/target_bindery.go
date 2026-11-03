package homarr

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/apps/bindery"
	"github.com/ajnart/self-host/internal/helpers"
)

func binderyTarget() target {
	return target{
		id: "bindery", name: "Bindery", kind: "bindery",
		internal: (*helpers.H).InternalURL,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: bindery.APIKey(to)}}
		},
		widgets: []widget{plain("mediaMissing")}, preflight: verifyBinderyReady, verify: verifyBindery,
	}
}

func verifyBinderyReady(ctx context.Context, from, to *helpers.H) error {
	if err := (bindery.App{}).Check(ctx, to); err != nil {
		if errors.Is(err, helpers.ErrManual) {
			from.Info(err.Error())
			return err
		}
		return errors.New("Bindery's administrator, saved API key or book/queue data is not ready; run self-host setup bindery, then self-host setup homarr")
	}
	return nil
}

func verifyBindery(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := verifyBinderyReady(ctx, from, to); err != nil {
		return err
	}
	sample, err := readMediaOrganizer(ctx, from, integrationID)
	if err != nil {
		return err
	}
	if float64(len(sample.missing)) != math.Min(sample.missingCount, 50) || float64(len(sample.queued)) != math.Min(sample.queuedCount, 50) {
		return errors.New("Homarr returned incomplete Bindery missing-book or active-queue pages; retry self-host setup homarr")
	}
	browser, err := browserAddress(ctx, from, to)
	if err != nil {
		return errors.New("Bindery's browser Address could not be resolved; retry self-host setup homarr")
	}
	base, err := url.Parse(browser)
	if err != nil || base.User != nil || base.Host == "" || base.Scheme != "http" && base.Scheme != "https" || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("Bindery's browser Address is invalid; review Access and retry self-host setup homarr")
	}
	if err := verifyBinderyItems(sample.missing, base, false); err != nil {
		return err
	}
	return verifyBinderyItems(sample.queued, base, true)
}

func verifyBinderyItems(items []any, browser *url.URL, queued bool) error {
	const failure = "Homarr returned invalid Bindery book identities, destinations or queue progress; retry self-host setup homarr"
	ids := map[float64]bool{}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		id, validID := countValue(item["id"])
		if !ok || !validID || id == 0 || id > 9007199254740991 || ids[id] || stringField(item, "type") != "book" || strings.TrimSpace(stringField(item, "title")) == "" {
			return errors.New(failure)
		}
		ids[id] = true
		if year, present := item["year"]; present && year != nil {
			value, valid := countValue(year)
			if !valid || value > 9999 {
				return errors.New(failure)
			}
		}
		if author, present := item["seriesTitle"]; present && author != nil {
			if _, valid := author.(string); !valid {
				return errors.New(failure)
			}
		}
		image, present := item["imageUrl"]
		if !present || !binderyNativeCover(image) || queued && image != nil {
			return errors.New(failure)
		}
		if !binderyNativeLink(browser, stringField(item, "link"), id, queued) {
			return errors.New(failure)
		}
		if !queued {
			continue
		}
		state := stringField(item, "status")
		percentage, valid := countValue(item["percentComplete"])
		timeLeft, present := item["timeLeft"]
		if !bindery.ValidQueueStatus(state) || state == "imported" || !valid || percentage > 100 || !present || percentage >= 100 && timeLeft != nil {
			return errors.New(failure)
		}
		if timeLeft != nil {
			value, valid := timeLeft.(string)
			if !valid || strings.TrimSpace(value) == "" {
				return errors.New(failure)
			}
		}
	}
	return nil
}

func binderyNativeLink(base *url.URL, link string, id float64, queued bool) bool {
	destination, err := url.Parse(link)
	if err != nil || destination.User != nil || destination.Scheme != base.Scheme || destination.Host != base.Host || destination.RawQuery != "" || destination.Fragment != "" {
		return false
	}
	prefix := strings.TrimRight(base.Path, "/")
	if !queued {
		return destination.Path == prefix+"/book/"+strconv.FormatFloat(id, 'f', 0, 64)
	}
	if destination.Path == prefix+"/queue" {
		return true
	}
	book := strings.TrimPrefix(destination.Path, prefix+"/book/")
	bookID, err := strconv.ParseInt(book, 10, 64)
	return err == nil && bookID > 0 && bookID <= 9007199254740991 && destination.Path == prefix+"/book/"+strconv.FormatInt(bookID, 10)
}

func binderyNativeCover(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	image, err := url.Parse(text)
	return err == nil && image.User == nil && image.Host != "" && (image.Scheme == "http" || image.Scheme == "https")
}
