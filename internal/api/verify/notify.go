package verify

import (
	"net/http"
	"strings"
	"time"
)

// Notify POSTs message as the raw request body to notifyURL, with an
// optional "Title" header. Deliberately minimal: this is just "POST text to
// a URL", not tied to any specific notification service. It happens to be
// exactly ntfy's publish contract (POST the message as the body, optional
// Title header), but works equally well against Gotify, a custom webhook
// receiver, or anything else that accepts a plain POST - callers pick their
// own notifyURL rather than this package assuming a particular service.
func Notify(notifyURL, title, message string) error {
	req, err := http.NewRequest(http.MethodPost, notifyURL, strings.NewReader(message))
	if err != nil {
		return err
	}
	if title != "" {
		req.Header.Set("Title", title)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
