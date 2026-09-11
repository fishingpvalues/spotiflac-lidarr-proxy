package spotiflac

import (
	"bytes"
	"strings"
	"testing"
)

// Users do not all run the same spotiflac-cli. The image ships one, but a
// binary install supplies its own, and the CLI's JSON event stream has gained
// fields over time. Every shape below has been emitted by some build, so the
// parser must keep accepting all of them: a field that arrives missing must
// degrade, never break the download.
//
// These run the real parseProgress over a canned stdout stream, which is
// exactly what the subprocess reader does in production.

// drain runs parseProgress over lines and collects what came out.
func drain(t *testing.T, lines string) ([]ProgressEvent, []error) {
	t.Helper()
	events := make(chan ProgressEvent, 64)
	errs := make(chan error, 64)
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		parseProgress(strings.NewReader(lines), events, errs, &out, nil)
		close(events)
		close(errs)
		close(done)
	}()
	<-done

	var gotEvents []ProgressEvent
	for e := range events {
		gotEvents = append(gotEvents, e)
	}
	var gotErrs []error
	for e := range errs {
		gotErrs = append(gotErrs, e)
	}
	return gotEvents, gotErrs
}

func findEvent(events []ProgressEvent, typ string) (ProgressEvent, bool) {
	for _, e := range events {
		if e.Type == typ {
			return e, true
		}
	}
	return ProgressEvent{}, false
}

func TestCLIFormatCompleteWithoutTrackCountOrBytes(t *testing.T) {
	// The oldest shape: a terminal event with only a path. track_count and
	// bytes came later; their absence must not lose the completion.
	events, errs := drain(t, `{"type":"complete","path":"/downloads/x"}`+"\n")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	e, ok := findEvent(events, "complete")
	if !ok {
		t.Fatal("a complete event without track_count must still be reported")
	}
	if e.OutputPath != "/downloads/x" {
		t.Fatalf("path = %q", e.OutputPath)
	}
	if e.TrackCount != 0 || e.Bytes != 0 {
		t.Fatalf("absent fields must stay zero, got track_count=%d bytes=%d", e.TrackCount, e.Bytes)
	}
}

func TestCLIFormatCompleteWithEverything(t *testing.T) {
	events, _ := drain(t, `{"type":"complete","path":"/d","size":1234,"track_count":9,"bytes":4096}`+"\n")
	e, ok := findEvent(events, "complete")
	if !ok {
		t.Fatal("complete event missing")
	}
	if e.Size != 1234 || e.TrackCount != 9 || e.Bytes != 4096 {
		t.Fatalf("size=%d track_count=%d bytes=%d", e.Size, e.TrackCount, e.Bytes)
	}
}

func TestCLIFormatTrackDoneIsMappedToMetadata(t *testing.T) {
	// Builds that report per-track completion use track_done; the download
	// processor only knows "metadata", so the mapping is load-bearing.
	events, _ := drain(t, `{"type":"track_done","title":"T","artist":"A","album":"B"}`+"\n")
	if _, ok := findEvent(events, "track_done"); ok {
		t.Fatal("track_done must not reach the consumer unmapped")
	}
	e, ok := findEvent(events, "metadata")
	if !ok {
		t.Fatal("track_done must arrive as a metadata event")
	}
	if e.Title != "T" || e.Artist != "A" || e.Album != "B" {
		t.Fatalf("fields lost in mapping: %+v", e)
	}
}

func TestCLIFormatErrorWithoutDetail(t *testing.T) {
	// Older builds report only `message`. The error must still carry it.
	_, errs := drain(t, `{"type":"error","message":"boom"}`+"\n")
	if len(errs) != 1 {
		t.Fatalf("expected one error, got %d", len(errs))
	}
	if !strings.Contains(errs[0].Error(), "boom") {
		t.Fatalf("error text lost: %v", errs[0])
	}
}

func TestCLIFormatErrorWithDetailKeepsBothStreams(t *testing.T) {
	// Newer builds add `detail` (the backend's captured stderr). Both it and
	// the provider reasons printed to stdout have to survive - reporting one
	// and dropping the other loses the reason about half the time.
	line := `{"type":"error","message":"failed","detail":"ext:tidal-web: NETWORK_ERROR: Timeout (120s) calling download"}`
	_, errs := drain(t, line+"\n")
	if len(errs) != 1 {
		t.Fatalf("expected one error, got %d", len(errs))
	}
	var de *DownloadError
	if !asDownloadError(errs[0], &de) {
		t.Fatalf("expected a DownloadError, got %T", errs[0])
	}
	if !strings.Contains(de.RawOutput, "NETWORK_ERROR") {
		t.Fatalf("detail dropped from raw output: %q", de.RawOutput)
	}
}

func TestCLIFormatUnknownEventTypeIsForwarded(t *testing.T) {
	// Forward compatibility: a CLI newer than this proxy must not be able to
	// stall a download by inventing an event type. The default branch
	// forwards it and the consumer ignores what it does not know.
	events, errs := drain(t, `{"type":"some_future_thing","title":"x"}`+"\n")
	if len(errs) != 0 {
		t.Fatalf("an unknown event type must not be an error: %v", errs)
	}
	if _, ok := findEvent(events, "some_future_thing"); !ok {
		t.Fatal("unknown event types must be forwarded, not swallowed")
	}
}

func TestCLIFormatNonJSONNoiseIsSkipped(t *testing.T) {
	// Every CLI generation prints some plain-text chatter: banners, ffmpeg
	// output, warnings. A non-JSON line must be skipped silently, not end
	// the stream or surface as a parse error.
	stream := strings.Join([]string{
		"SpotiFLAC CLI v1.2.3",
		"Decrypting file...",
		`{"type":"progress","percent":50,"bytes":2048}`,
		"[warn] something chatty",
		`{"type":"complete","path":"/d","track_count":1}`,
	}, "\n") + "\n"

	events, errs := drain(t, stream)
	if len(errs) != 0 {
		t.Fatalf("plain-text lines must not produce errors: %v", errs)
	}
	if _, ok := findEvent(events, "progress"); !ok {
		t.Fatal("progress event lost among noise")
	}
	if _, ok := findEvent(events, "complete"); !ok {
		t.Fatal("complete event lost among noise")
	}
}

func TestCLIFormatVerificationRequiredReachesTheHook(t *testing.T) {
	// The headless build emits this when community verification is needed.
	// It must both fire the callback and reach the consumer.
	var hookFired ProgressEvent
	events := make(chan ProgressEvent, 8)
	errs := make(chan error, 8)
	var out bytes.Buffer
	line := `{"type":"verification_required","url":"https://verify.example/x","cb":"https://cb"}` + "\n"
	parseProgress(strings.NewReader(line), events, errs, &out, func(e ProgressEvent) { hookFired = e })
	close(events)

	if hookFired.URL != "https://verify.example/x" {
		t.Fatalf("verification hook not called with the URL, got %+v", hookFired)
	}
	var seen bool
	for e := range events {
		if e.Type == "verification_required" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("verification_required must also reach the consumer")
	}
}

// EntityKind decides whether a grab downloads a whole album or one file,
// which is the difference between a release Lidarr imports and one it always
// rejects. The CLI reports `entity`; builds that predate it do not, so the
// URL path is the fallback.
func TestEntityKindAcrossCLIGenerations(t *testing.T) {
	cases := []struct {
		name string
		in   MetadataResult
		want string
	}{
		{"explicit album field wins", MetadataResult{Entity: "album", SpotifyURL: "https://open.spotify.com/track/x"}, "album"},
		{"explicit track field wins", MetadataResult{Entity: "track", SpotifyURL: "https://open.spotify.com/album/x"}, "track"},
		{"old build, album URL", MetadataResult{SpotifyURL: "https://open.spotify.com/album/abc"}, "album"},
		{"old build, track URL", MetadataResult{SpotifyURL: "https://open.spotify.com/track/abc"}, "track"},
		{"unknown entity value falls back to URL", MetadataResult{Entity: "playlist", SpotifyURL: "https://open.spotify.com/album/abc"}, "album"},
		{"nothing to go on", MetadataResult{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.EntityKind(); got != tc.want {
				t.Fatalf("EntityKind() = %q, want %q", got, tc.want)
			}
		})
	}
}

func asDownloadError(err error, target **DownloadError) bool {
	de, ok := err.(*DownloadError)
	if ok {
		*target = de
	}
	return ok
}
