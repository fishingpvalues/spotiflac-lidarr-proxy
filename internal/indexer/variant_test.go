package indexer

import (
	"testing"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func defaultFilter(t *testing.T, skip bool) *ExcludeFilter {
	t.Helper()
	f, err := NewExcludeFilter(DefaultExcludePattern, skip)
	if err != nil {
		t.Fatalf("default pattern does not compile: %v", err)
	}
	return f
}

// Titles taken from one production Lidarr's wanted list and failed-download
// history (2026-10-16). Every one of them failed on every provider.
func TestDefaultPatternMatchesProductionVariants(t *testing.T) {
	f := defaultFilter(t, true)
	for _, title := range []string{
		"Nuvole Bianche (Live From The Steve Jobs Theatre / 2019)", //nolint:misspell // the real title
		"Hardwell On Air - Best Of May",
		"Hardwell on Air: Best of January",
		"Lose Yourself to Dance (Official Version)",
		"BERSERK -Forces- - TV version",
		"Live in Prague",
		"Live",
		"Auf der Suche nach der Schnapsinsel: Live im SO36",
		"USB002 (Live at Alexandra Palace)",
		"Live @ We Are Family",
		"Ya no sos igual (live in Argentinien)",
		"Teeth (Karaoke Version)",
		"Gabby's Poppenhuis (Officiële Afspeellijst)",
		"Where The Lights Are (Mix Cut)",
		"Song (Sped Up)",
		"Interview With Bruce Springsteen",
	} {
		if skip, _ := f.SkipQuery("", title); !skip {
			t.Errorf("%q: not recognized as a variant", title)
		}
	}
}

// A bare word is never enough: these are studio albums.
func TestDefaultPatternKeepsStudioTitles(t *testing.T) {
	f := defaultFilter(t, true)
	for _, title := range []string{
		"Live Forever",
		"Live and Let Die",
		"Alive",
		"Born to Run",
		"Lose Yourself to Dance",
		"Liveline",
		"Videotape",
		"Air",
		"The Mix",
	} {
		if skip, m := f.SkipQuery("", title); skip {
			t.Errorf("%q: wrongly excluded (marker %q)", title, m)
		}
	}
}

func TestApplyDropsOnlyUnrequestedVariants(t *testing.T) {
	f := defaultFilter(t, false)
	results := []spotiflac.MetadataResult{
		{Artist: "Bruce Springsteen", Album: "Born to Run"},
		{Artist: "Bruce Springsteen", Album: "Born to Run (Live)"},
		{Artist: "Bruce Springsteen", Album: "Born to Run (Official Video)"},
	}

	kept, dropped := f.Apply(append([]spotiflac.MetadataResult(nil), results...), "", "Born to Run")
	if dropped != 2 || len(kept) != 1 || kept[0].Album != "Born to Run" {
		t.Fatalf("studio request: kept %v, dropped %d", kept, dropped)
	}

	// A request that itself asks for a live album keeps the live result,
	// but still loses the video.
	kept, dropped = f.Apply(append([]spotiflac.MetadataResult(nil), results...), "", "Born to Run (Live)")
	if dropped != 1 || len(kept) != 2 {
		t.Fatalf("live request: kept %v, dropped %d", kept, dropped)
	}
}

func TestSkipQueryUsesQueryWhenNoAlbum(t *testing.T) {
	f := defaultFilter(t, true)
	if skip, m := f.SkipQuery("Hardwell Hardwell On Air Best Of May", ""); !skip || m != "on" {
		t.Fatalf("q-only radio show search: skip=%v marker=%q", skip, m)
	}
	if skip, _ := f.SkipQuery("Hardwell Hardwell On Air", "Rebels Never Die"); skip {
		t.Fatal("album decides when present; the query text must not")
	}
}

func TestSkipQueriesOff(t *testing.T) {
	f := defaultFilter(t, false)
	if skip, _ := f.SkipQuery("", "Live in Prague"); skip {
		t.Fatal("SkipQueries=false must never skip a search")
	}
}

func TestDisabledFilterIsNilSafe(t *testing.T) {
	for _, p := range []string{"", "-", "  -  "} {
		f, err := NewExcludeFilter(p, true)
		if err != nil || f != nil {
			t.Fatalf("pattern %q: filter %v err %v, want nil nil", p, f, err)
		}
		if skip, _ := f.SkipQuery("", "Live in Prague"); skip {
			t.Fatal("nil filter skipped a query")
		}
		in := []spotiflac.MetadataResult{{Album: "X (Live)"}}
		if out, n := f.Apply(in, "", "X"); n != 0 || len(out) != 1 {
			t.Fatal("nil filter dropped a result")
		}
	}
}

func TestBadPatternIsAnError(t *testing.T) {
	if _, err := NewExcludeFilter("(", true); err == nil {
		t.Fatal("an uncompilable pattern must be reported")
	}
}
