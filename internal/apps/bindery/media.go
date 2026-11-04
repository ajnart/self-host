package bindery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

type missingBook struct {
	ID          int64           `json:"id"`
	Title       string          `json:"title"`
	ReleaseDate *string         `json:"releaseDate"`
	ImageURL    *string         `json:"imageUrl"`
	Author      json.RawMessage `json:"author"`
}

type queueItem struct {
	ID         int64           `json:"id"`
	Title      string          `json:"title"`
	Status     string          `json:"status"`
	Percentage json.RawMessage `json:"percentage"`
	TimeLeft   *string         `json:"timeLeft"`
	Book       json.RawMessage `json:"book"`
}

func ValidQueueStatus(status string) bool {
	switch status {
	case "grabbed", "downloading", "completed", "importPending", "importing", "imported", "failed", "importFailed", "importBlocked", "importExternal", "importHeld":
		return true
	default:
		return false
	}
}

func validMediaID(id int64) bool { return id > 0 && id <= 9007199254740991 }

func verifyMedia(ctx context.Context, h *helpers.H) error {
	missing, err := mediaResponse(ctx, h, "/api/v1/wanted/missing")
	if err != nil {
		return err
	}
	var books []missingBook
	if missing.Decode(&books) != nil || books == nil {
		return errors.New("Bindery did not return an explicit missing-book list; retry self-host setup bindery")
	}
	ids := map[int64]bool{}
	for _, book := range books {
		if !validMediaID(book.ID) || ids[book.ID] || strings.TrimSpace(book.Title) == "" {
			return errors.New("Bindery returned invalid or duplicate missing-book identities; retry self-host setup bindery")
		}
		ids[book.ID] = true
		if book.ReleaseDate != nil && *book.ReleaseDate != "" {
			if _, err := time.Parse(time.RFC3339Nano, *book.ReleaseDate); err != nil {
				if _, err := time.Parse(time.DateOnly, *book.ReleaseDate); err != nil {
					return errors.New("Bindery returned an invalid book release date; retry self-host setup bindery")
				}
			}
		}
		if book.ImageURL != nil && *book.ImageURL != "" && !validCover(*book.ImageURL) {
			return errors.New("Bindery returned an invalid book cover address; retry self-host setup bindery")
		}
		if len(book.Author) > 0 {
			var author struct {
				Name string `json:"authorName"`
			}
			if json.Unmarshal(book.Author, &author) != nil || strings.TrimSpace(author.Name) == "" {
				return errors.New("Bindery returned invalid book author data; retry self-host setup bindery")
			}
		}
	}
	queue, err := mediaResponse(ctx, h, "/api/v1/queue")
	if err != nil {
		return err
	}
	return verifyQueue(queue.Body)
}

func mediaResponse(ctx context.Context, h *helpers.H, endpoint string) (helpers.Response, error) {
	resp, err := h.HTTP(ctx, keyRequest(h, endpoint))
	if err != nil {
		return resp, errors.New("Bindery book or queue data could not be read; retry self-host setup bindery")
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return resp, &helpers.ManualError{Hint: keyHint}
	}
	if resp.Status != http.StatusOK {
		return resp, errors.New("Bindery book or queue data failed; retry self-host setup bindery")
	}
	return resp, nil
}

func verifyQueue(body []byte) error {
	const failure = "Bindery returned incomplete, partial, stale or invalid queue data; repair its download-client connection and retry self-host setup bindery"
	var items []queueItem
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
		if json.Unmarshal(body, &items) != nil {
			return errors.New(failure)
		}
	} else {
		var queue struct {
			Items        []queueItem     `json:"items"`
			Partial      json.RawMessage `json:"partial"`
			Stale        json.RawMessage `json:"stale"`
			StaleClients json.RawMessage `json:"staleClients"`
			Error        json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &queue) != nil || len(queue.Error) > 0 && string(bytes.TrimSpace(queue.Error)) != "null" {
			return errors.New(failure)
		}
		for _, flag := range []json.RawMessage{queue.Partial, queue.Stale} {
			if len(flag) > 0 && string(bytes.TrimSpace(flag)) != "false" {
				return errors.New(failure)
			}
		}
		if len(queue.StaleClients) > 0 {
			var clients []json.RawMessage
			if json.Unmarshal(queue.StaleClients, &clients) != nil || clients == nil || len(clients) != 0 {
				return errors.New(failure)
			}
		}
		items = queue.Items
	}
	if items == nil {
		return errors.New(failure)
	}
	ids := map[int64]bool{}
	for _, item := range items {
		if !validMediaID(item.ID) || ids[item.ID] || strings.TrimSpace(item.Title) == "" || !ValidQueueStatus(item.Status) {
			return errors.New(failure)
		}
		ids[item.ID] = true
		if item.TimeLeft != nil && strings.TrimSpace(*item.TimeLeft) == "" {
			return errors.New(failure)
		}
		if len(item.Percentage) > 0 {
			var percentage string
			if json.Unmarshal(item.Percentage, &percentage) != nil || percentage == "" {
				return errors.New(failure)
			}
			value, err := strconv.ParseFloat(percentage, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
				return errors.New(failure)
			}
		}
		if len(item.Book) > 0 {
			var book struct {
				ID     int64  `json:"id"`
				Title  string `json:"title"`
				Author string `json:"authorName"`
			}
			if json.Unmarshal(item.Book, &book) != nil || !validMediaID(book.ID) || strings.TrimSpace(book.Title) == "" {
				return errors.New(failure)
			}
		}
	}
	return nil
}

func validCover(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil {
		return false
	}
	if parsed.IsAbs() {
		return (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
	}
	return strings.HasPrefix(parsed.Path, "/") && parsed.Host == ""
}
