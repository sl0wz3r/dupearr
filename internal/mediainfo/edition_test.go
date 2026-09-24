package mediainfo

import (
	"strings"
	"testing"
)

func TestEdition(t *testing.T) {
	tests := []struct {
		name, path, plex, arr, want string
	}{
		// priority
		{"plex wins", "/m/Movie (2010)/Movie (2010) {edition-Extended}.mkv", "Director's Cut", "Unrated", "Director's Cut"},
		{"arr second", "/m/Movie (2010)/Movie (2010) {edition-Extended}.mkv", "", "Unrated", "Unrated"},
		{"tag third", "/m/Movie (2010)/Movie (2010) {edition-Extended Cut} Remux-2160p.mkv", "", "", "Extended Cut"},
		{"plex cleaned", "", "  Director’s   Cut ", "", "Director's Cut"},
		{"plex whitespace only falls through", "/m/Movie (2010)/Movie (2010) IMAX.mkv", "   ", "", "IMAX"},
		// {edition-…} tags (Plex naming)
		{"tag in folder", "/m/Movie (2010) {edition-Director's Cut}/Movie (2010).mkv", "", "", "Director's Cut"},
		{"tag file beats folder", "/m/Movie (2010) {edition-Theatrical}/Movie (2010) {edition-IMAX}.mkv", "", "", "IMAX"},
		{"tag case-insensitive", "/m/Movie (2010) {Edition-Final Cut}.mkv", "", "", "Final Cut"},
		{"tag scene dots", "/m/Movie.2010.{edition-Directors.Cut}.mkv", "", "", "Directors Cut"},
		{"tag windows path", `C:\Movies\Movie (2010)\Movie (2010) {edition-Special Edition}.mkv`, "", "", "Special Edition"},
		{"tag beats folder tokens", "/m/Movie (2010) Unrated/Movie (2010) {edition-Uncut}.mkv", "", "", "Uncut"},
		// filename tokens (Radarr EditionRegex style)
		{"directors cut", "/m/Movie (2010)/Movie (2010) Director's Cut Bluray-1080p.mkv", "", "", "Director's Cut"},
		{"directors cut dotted", "/dl/Movie.2010.Directors.Cut.1080p.BluRay.x264-GRP.mkv", "", "", "Director's Cut"},
		{"directors cut curly", "/m/Movie (2010) Director’s Cut.mkv", "", "", "Director's Cut"},
		{"extended", "/dl/Movie.2010.EXTENDED.1080p.BluRay.x264-GRP.mkv", "", "", "Extended"},
		{"extended edition", "/m/Movie (2001) Extended Edition.mkv", "", "", "Extended"},
		{"extended cut", "/m/Movie (2001) Extended Cut.mkv", "", "", "Extended"},
		{"theatrical", "/dl/Movie.2010.Theatrical.Cut.1080p.mkv", "", "", "Theatrical"},
		{"unrated", "/dl/Movie.2010.UNRATED.1080p.mkv", "", "", "Unrated"},
		{"uncut", "/dl/Movie.2010.Uncut.1080p.mkv", "", "", "Uncut"},
		{"uncensored", "/dl/Movie.2010.Uncensored.1080p.mkv", "", "", "Uncensored"},
		{"imax", "/dl/Movie.2021.IMAX.2160p.WEB-DL.mkv", "", "", "IMAX"},
		{"imax enhanced", "/dl/Movie.2021.IMAX.Enhanced.2160p.mkv", "", "", "IMAX"},
		{"remastered", "/dl/Movie.1982.REMASTERED.1080p.BluRay.mkv", "", "", "Remastered"},
		{"criterion", "/dl/Movie.1954.Criterion.Collection.1080p.BluRay.mkv", "", "", "Criterion"},
		{"special edition", "/dl/Movie.1977.Special.Edition.1080p.mkv", "", "", "Special Edition"},
		{"ultimate cut", "/dl/Watchmen.2009.Ultimate.Cut.1080p.mkv", "", "", "Ultimate Cut"},
		{"ultimate edition", "/dl/Movie.2016.Ultimate.Edition.1080p.mkv", "", "", "Ultimate Edition"},
		{"final cut", "/m/Blade Runner (1982) Final Cut.mkv", "", "", "Final Cut"},
		{"collectors edition", "/dl/Movie.1990.Collectors.Edition.mkv", "", "", "Collector's Edition"},
		{"anniversary", "/dl/Movie.1984.25th.Anniversary.Edition.1080p.mkv", "", "", "25th Anniversary Edition"},
		{"anniversary 40", "/dl/Movie.1982.40th.Anniversary.1080p.mkv", "", "", "40th Anniversary Edition"},
		{"anniversary 21st", "/dl/Movie.1990.21st.Anniversary.mkv", "", "", "21st Anniversary Edition"},
		{"anniversary no number", "/dl/Movie.1990.Anniversary.Edition.mkv", "", "", "Anniversary Edition"},
		{"redux", "/dl/Apocalypse.Now.1979.Redux.1080p.mkv", "", "", "Redux"},
		{"open matte", "/dl/Movie.2003.Open.Matte.1080p.mkv", "", "", "Open Matte"},
		{"open matte joined", "/dl/Movie.2003.OpenMatte.1080p.mkv", "", "", "Open Matte"},
		{"combined in order", "/dl/Movie.2010.Extended.Directors.Cut.1080p.mkv", "", "", "Extended Director's Cut"},
		{"combined imax remastered", "/dl/Movie.2010.IMAX.Remastered.mkv", "", "", "IMAX Remastered"},
		{"folder tokens", "/m/Movie (2010) Director's Cut/movie.mkv", "", "", "Director's Cut"},
		// not editions
		{"title final cut", "/m/The Final Cut (2004)/The Final Cut (2004) Bluray-1080p.mkv", "", "", ""},
		{"title with year inside", "/m/Blade Runner 2049 (2017)/Blade Runner 2049 (2017).mkv", "", "", ""},
		{"no tokens", "/m/Movie (2010)/Movie (2010) Bluray-1080p.mkv", "", "", ""},
		{"extendedly is not extended", "/dl/Movie.2010.Extendedly.mkv", "", "", ""},
		{"imaxx is not imax", "/dl/Movie.2010.IMAXX.mkv", "", "", ""},
		{"special alone is not special edition", "/dl/Movie.2010.Special.1080p.mkv", "", "", ""},
		{"special extended edition", "/dl/LOTR.2001.Special.Extended.Edition.mkv", "", "", "Extended"},
		{"empty", "", "", "", ""},
		{"root", "/", "", "", ""},
		// Edition tokens BEFORE the year that the folder name lacks are editions, not title words
		// (regression: these used to return "" and let a different cut be merged and deleted).
		{"redux before year", "/m/Apocalypse Now (1979)/Apocalypse Now Redux (1979).mkv", "", "", "Redux"},
		{"directors cut before year", "/m/Blade Runner (1982)/Blade Runner Director's Cut (1982) Bluray-1080p.mkv", "", "", "Director's Cut"},
		{"folder title token missing from file", "/m/Apocalypse Now Redux (1979)/Apocalypse.Now.1979.1080p.mkv", "", "", "Redux"},
		{"file and folder tokens combined", "/m/Apocalypse Now Redux (1979)/Apocalypse.Now.1979.Remastered.1080p.mkv", "", "", "Remastered Redux"}, // file first
		{"file token plus folder token", "/m/Movie (2010) Extended/Movie (2010) IMAX.mkv", "", "", "IMAX Extended"},
		{"same token file and folder once", "/m/Movie (2010) Extended/Movie (2010) Extended Cut.mkv", "", "", "Extended"},
		{"title token in both names", "/m/Uncut Gems (2019)/Uncut Gems (2019) WEBDL-1080p.mkv", "", "", ""},
		{"flat library title token errs to edition", "/m/The Final Cut (2004).mkv", "", "", "Final Cut"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Edition(tt.path, tt.plex, tt.arr); got != tt.want {
				t.Errorf("Edition(%q, %q, %q) = %q, want %q", tt.path, tt.plex, tt.arr, got, tt.want)
			}
		})
	}
}

func TestEditionWithTitles(t *testing.T) {
	tests := []struct {
		name, path, title, plex, want string
	}{
		{"title word is not an edition", "/m/The Final Cut (2004)/The Final Cut (2004) Bluray-1080p.mkv", "The Final Cut", "", ""},
		{"flat library title word", "/m/The Final Cut (2004).mkv", "The Final Cut", "", ""},
		{"no year, title word", "/m/The Final Cut (2004)/The Final Cut.mkv", "The Final Cut", "", ""},
		{"redux of Apocalypse Now", "/m/Apocalypse Now Redux (1979)/Apocalypse Now Redux (1979).mkv", "Apocalypse Now", "", "Redux"},
		{"redux folder, plain file", "/m/Apocalypse Now Redux (1979)/Apocalypse.Now.1979.mkv", "Apocalypse Now", "", "Redux"},
		{"theatrical copy", "/m/Apocalypse Now (1979)/Apocalypse Now (1979).mkv", "Apocalypse Now", "", ""},
		{"directors cut before year", "/m/Blade Runner (1982)/Blade Runner Director's Cut (1982).mkv", "Blade Runner", "", "Director's Cut"},
		{"no year, edition word", "/m/Avatar (2009)/Avatar.Extended.mkv", "Avatar", "", "Extended"},
		{"after year always counts", "/m/Extended Family (2023)/Extended Family (2023) Extended Cut.mkv", "Extended Family", "", "Extended"},
		{"episode title word", "/tv/Show (2020)/Season 01/Show (2020) - S01E05 - The Director's Cut.mkv",
			"The Director’s Cut|Show", "", ""},
		{"episode edition", "/tv/Show (2020)/Season 01/Show (2020) - S01E05 - Pilot Extended.mkv", "Pilot|Show", "", "Extended"},
		{"show title word", "/tv/Extended Family (2023)/Season 01/Extended.Family.S01E01.Pilot.1080p.mkv", "Pilot|Extended Family", "", ""},
		{"scene dots and punctuation", "/dl/Star.Wars.Episode.IV.A.New.Hope.1977.Special.Edition.mkv",
			"Star Wars: Episode IV - A New Hope", "", "Special Edition"},
		{"title not in name", "/m/Uncut Gems (2019)/movie.mkv", "Uncut Gems", "", ""},
		{"blank titles fall back", "/m/Apocalypse Now (1979)/Apocalypse Now Redux (1979).mkv", " | ", "", "Redux"},
		{"plex edition still wins", "/m/Apocalypse Now Redux (1979)/x.mkv", "Apocalypse Now", "Final Cut", "Final Cut"},
		{"tag still wins", "/m/Movie (2010)/Movie Redux (2010) {edition-IMAX}.mkv", "Movie", "", "IMAX"},
		{"empty title equals Edition", "/m/Apocalypse Now (1979)/Apocalypse Now Redux (1979).mkv", "", "", "Redux"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var titles []string
			if tt.title != "" {
				titles = strings.Split(tt.title, "|")
			}
			if got := EditionWithTitles(tt.path, tt.plex, "", titles...); got != tt.want {
				t.Errorf("EditionWithTitles(%q, %q) = %q, want %q", tt.path, titles, got, tt.want)
			}
		})
	}
	// Edition is EditionWithTitles without titles.
	for _, p := range []string{"/m/Apocalypse Now (1979)/Apocalypse Now Redux (1979).mkv", "/dl/Movie.2010.IMAX.mkv", ""} {
		if Edition(p, "", "") != EditionWithTitles(p, "", "") {
			t.Errorf("Edition(%q) differs from EditionWithTitles without titles", p)
		}
	}
}

func TestStripWords(t *testing.T) {
	tests := []struct {
		text    string
		phrases []string
		want    string
	}{
		{"the final cut 2004 mkv", []string{"the final cut"}, "2004 mkv"},
		{"final cut final cut", []string{"final cut"}, ""},
		{"the final cutter", []string{"final cut"}, "the final cutter"}, // whole words only
		{"a b a b", []string{"a b"}, ""},
		{"", []string{"x"}, ""},
	}
	for _, tt := range tests {
		if got := stripWords(tt.text, tt.phrases); got != tt.want {
			t.Errorf("stripWords(%q, %q) = %q, want %q", tt.text, tt.phrases, got, tt.want)
		}
	}
	if got := normWords("  Director’s.Cut: [2004]_x "); got != "directors cut 2004 x" {
		t.Errorf("normWords = %q", got)
	}
}

func TestEditionLengthCap(t *testing.T) {
	long := strings.Repeat("x", 500)
	if got := Edition("", long, ""); len([]rune(got)) != maxEditionLen {
		t.Fatalf("Edition capped length = %d, want %d", len([]rune(got)), maxEditionLen)
	}
}

func TestEditionKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"Theatrical", ""},
		{"theatrical cut", ""},
		{"Theatrical Edition", ""},
		{"Original Theatrical", ""},
		{"Original Theatrical Release", ""},
		{"Director's Cut", "directors"},
		{"Directors Cut", "directors"},
		{"Director’s Cut", "directors"},
		{"Director Cut", "directors"},
		{"director's edition", "directors"},
		{"Extended", "extended"},
		{"Extended Cut", "extended"},
		{"Extended Edition", "extended"},
		{"EXTENDED VERSION", "extended"},
		{"Unrated", "unrated"},
		{"Uncut", "uncut"},
		{"IMAX", "imax"},
		{"IMAX Edition", "imax"},
		{"Remastered", "remastered"},
		{"Criterion", "criterion"},
		{"Criterion Collection", "criterion"},
		{"Special Edition", "special"},
		{"Ultimate Edition", "ultimate"},
		{"Ultimate Cut", "ultimate"},
		{"Final Cut", "final"},
		{"Collector's Edition", "collectors"},
		{"25th Anniversary Edition", "25-anniversary"},
		{"25 Anniversary", "25-anniversary"},
		{"Redux", "redux"},
		{"Open Matte", "open-matte"},
		{"Extended Director's Cut", "extended-directors"},
		{"3D", "3d"},
		{"Édition Spéciale", "édition-spéciale"},
		// Filler-only / symbol-only editions stay distinct from "no edition" (a missed edition
		// could merge, and delete, a different cut).
		{"Cut", "cut"},
		{"The Collection", "the-collection"},
		{"★", "★"},
		{"Standard", ""},
		{"Standard Edition", ""},
	}
	for _, tt := range tests {
		if got := EditionKey(tt.in); got != tt.want {
			t.Errorf("EditionKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Every display edition produced by Edition must round-trip to a stable, non-colliding key.
func TestEditionKeyDistinctForKnownEditions(t *testing.T) {
	displays := []string{
		"Director's Cut", "Collector's Edition", "Extended", "Unrated", "Uncut", "Uncensored", "IMAX",
		"Remastered", "Criterion", "Special Edition", "Ultimate Edition", "Final Cut",
		"25th Anniversary Edition", "Anniversary Edition", "Redux", "Open Matte",
	}
	seen := map[string]string{}
	for _, d := range displays {
		k := EditionKey(d)
		if k == "" {
			t.Errorf("EditionKey(%q) is empty", d)
			continue
		}
		if prev, ok := seen[k]; ok {
			t.Errorf("EditionKey collision: %q and %q → %q", prev, d, k)
		}
		seen[k] = d
	}
	if EditionKey("Ultimate Cut") != EditionKey("Ultimate Edition") {
		t.Errorf("Ultimate Cut and Ultimate Edition should share a key")
	}
}

func TestIs3D(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/m/Avatar (2009)/Avatar (2009) 3D.mkv", true},
		{"/dl/Avatar.2009.1080p.BluRay.Half-SBS.x264-GRP.mkv", true},
		{"/dl/Avatar.2009.1080p.BluRay.Half-OU.x264-GRP.mkv", true},
		{"/dl/Avatar.2009.1080p.Half.SBS.mkv", true},
		{"/dl/Avatar.2009.1080p.HSBS.mkv", true},
		{"/dl/Avatar.2009.1080p.H-SBS.mkv", true},
		{"/dl/Avatar.2009.1080p.HOU.mkv", true},
		{"/dl/Avatar.2009.1080p.SBS.mkv", true},
		{"/dl/Avatar.2009.1080p.BluRay3D.mkv", true},
		{"/dl/Avatar.1080p.BluRay3D.mkv", true}, // anywhere, no year needed
		{"/dl/Avatar.BD3D.mkv", true},
		{"/m/Avatar (2009) 3D/Avatar (2009).mkv", true}, // folder
		{"/m/Movie (2010)/Movie (2010) {edition-3D}.mkv", true},
		// A file name without a year: every token counts (errs towards 3D = no deletion).
		{"/m/Avatar (2009)/Avatar.3D.HSBS.mkv", true},
		{"/m/Avatar (2009)/Avatar.3D.mkv", true},
		{"/m/Avatar (2009)/Avatar HOU.mkv", true},
		{"/m/3D Movie.mkv", true},
		{`C:\Movies\Avatar (2009)\Avatar.2009.Half-SBS.mkv`, true},
		{"/tv/Show/Season 01/Show.S01E01.1080p.3D.mkv", true}, // episode marker counts as release marker
		// "3D" before the year that the folder lacks is a variant marker (regression).
		{"/m/Avatar (2009)/Avatar 3D (2009).mkv", true},
		{"/m/Avatar 3D (2009)/Avatar (2009) Bluray-1080p.mkv", true},
		{"/dl/Avatar.SBS.2009.mkv", true}, // SBS family anywhere
		{"/dl/Avatar.2009.1080p.FSBS.mkv", true},
		{"/dl/Avatar.2009.1080p.F-SBS.mkv", true},
		{"/dl/Avatar.2009.1080p.HTAB.mkv", true},
		{"/dl/Avatar.2009.1080p.Half-TAB.mkv", true},
		{"/dl/Avatar.2009.1080p.Full.OU.mkv", true},
		{"/dl/Avatar.2009.1080p.Full-TAB.mkv", true},
		{"/dl/Avatar.2009.1080p.FOU.mkv", true},
		{"/m/Pierrot le Fou (1965)/Pierrot le Fou (1965) Bluray-1080p.mkv", false}, // "Fou" in the title
		{"/dl/Movie.2010.1080p.OU.mkv", false},                                     // a bare OU/TAB is not enough
		{"/dl/Movie.2010.1080p.TAB.mkv", false},
		// not 3D
		{"/m/Step Up 3D (2010)/Step Up 3D (2010) Bluray-1080p.mkv", false}, // title before year
		{"/m/Avatar (2009)/Avatar (2009) Bluray-1080p.mkv", false},
		{"/dl/Game.2010.3DO.Edition.mkv", false},
		{"/dl/Movie.2013D.mkv", false},
		{"/dl/Movie.2010.x3d.mkv", false},
		{"/dl/Movie.2010.1080p.SBSX.mkv", false},
		{"/dl/Movie.2010.1080p.HOUSE.mkv", false},
		{"/m/3DO Games (1993)/3DO Games (1993).mkv", false},
		{"/m/Movie (2010)/Movie (2010) 1080p x264.mkv", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := Is3D(tt.path); got != tt.want {
			t.Errorf("Is3D(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestIsSamplePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/dl/Movie.2019.1080p.BluRay.x264-GRP/Sample/grp-movie-sample.mkv", true},
		{"/dl/Movie.2019.1080p.BluRay.x264-GRP/Samples/movie.mkv", true},
		{"/m/Movie (2019)/Movie.2019.1080p-sample.mkv", true},
		{"/m/Movie (2019)/sample.mkv", true},
		{"/m/Movie (2019)/SAMPLE.mkv", true},
		{"/m/Movie (2019)/grp-movie-sample.mkv", true},
		{`D:\dl\Movie.2019\Sample\x.mkv`, true},
		// not samples
		{"/m/The Sample (2019)/The.Sample.2019.1080p.mkv", false},
		{"/m/Movie (2019)/Movie (2019) Sampled.mkv", false},
		{"/m/Movie (2019)/Movie (2019).mkv", false},
		{"/samples/library/Movie (2019)/Movie (2019).mkv", false}, // only the immediate folder counts
		{"", false},
	}
	for _, tt := range tests {
		if got := IsSamplePath(tt.path); got != tt.want {
			t.Errorf("IsSamplePath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestOrdinal(t *testing.T) {
	tests := map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th",
		21: "21st", 22: "22nd", 23: "23rd", 25: "25th", 100: "100th", 101: "101st", 111: "111th"}
	for n, want := range tests {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}
