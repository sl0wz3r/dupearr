package mediainfo

import "testing"

func TestLanguageCode(t *testing.T) {
	tests := []struct{ in, want string }{
		// ISO 639-2/B passthrough (Plex languageCode, Radarr audioLanguages)
		{"eng", "eng"},
		{"ENG", "eng"},
		{" eng ", "eng"},
		{"ger", "ger"},
		{"fre", "fre"},
		{"jpn", "jpn"},
		// ISO 639-2/T → /B
		{"deu", "ger"},
		{"fra", "fre"},
		{"zho", "chi"},
		{"ces", "cze"},
		{"nld", "dut"},
		{"ell", "gre"},
		{"fas", "per"},
		{"ron", "rum"},
		{"slk", "slo"},
		{"sqi", "alb"},
		{"hye", "arm"},
		{"kat", "geo"},
		{"isl", "ice"},
		{"mkd", "mac"},
		{"msa", "may"},
		{"mya", "bur"},
		{"eus", "baq"},
		{"cym", "wel"},
		{"bod", "tib"},
		{"mri", "mao"},
		// ISO 639-1 and locale tags (Plex languageTag)
		{"en", "eng"},
		{"de", "ger"},
		{"pt", "por"},
		{"en-US", "eng"},
		{"en-GB", "eng"},
		{"pt-BR", "por"},
		{"pt_BR", "por"},
		{"zh-Hant", "chi"},
		{"es-419", "spa"},
		{"nb", "nob"},
		{"iw", "heb"},
		{"in", "ind"},
		// names (Plex language, *arr languages[].name)
		{"English", "eng"},
		{"english", "eng"},
		{"English (United States)", "eng"},
		{"German", "ger"},
		{"Deutsch", "ger"},
		{"French", "fre"},
		{"Français", "fre"},
		{"Spanish", "spa"},
		{"Español", "spa"},
		{"Latin American Spanish", "spa"},
		{"Portuguese (Brazil)", "por"},
		{"Brazilian Portuguese", "por"},
		{"Japanese", "jpn"},
		{"Chinese", "chi"},
		{"Mandarin", "chi"},
		{"Korean", "kor"},
		{"Dutch", "dut"},
		{"Flemish", "dut"},
		{"Swedish", "swe"},
		{"Norwegian", "nor"},
		{"Norwegian Bokmål", "nob"},
		{"Danish", "dan"},
		{"Finnish", "fin"},
		{"Polish", "pol"},
		{"Czech", "cze"},
		{"Hungarian", "hun"},
		{"Greek", "gre"},
		{"Turkish", "tur"},
		{"Hebrew", "heb"},
		{"Hindi", "hin"},
		{"Arabic", "ara"},
		{"Russian", "rus"},
		{"Persian", "per"},
		{"Farsi", "per"},
		{"Filipino", "fil"},
		{"Tagalog", "tgl"},
		// undetermined / unknown
		{"", ""},
		{"   ", ""},
		{"und", ""},
		{"Unknown", ""},
		{"zxx", ""},
		{"xx", ""},
		{"Klingonish", ""},
		{"tlh", "tlh"}, // unknown 3-letter code: passed through
		{"e1n", ""},
		{"(eng)", ""},
	}
	for _, tt := range tests {
		if got := LanguageCode(tt.in); got != tt.want {
			t.Errorf("LanguageCode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLanguageTableConsistency(t *testing.T) {
	seenB := map[string]bool{}
	for _, l := range languageTable {
		if len(l.b) != 3 || !isASCIILetters(l.b) {
			t.Errorf("bad ISO 639-2/B code %q", l.b)
		}
		if seenB[l.b] {
			t.Errorf("duplicate ISO 639-2/B code %q", l.b)
		}
		seenB[l.b] = true
		if l.t != "" && (len(l.t) != 3 || l.t == l.b) {
			t.Errorf("%s: bad ISO 639-2/T code %q", l.b, l.t)
		}
		if l.one != "" && len(l.one) != 2 {
			t.Errorf("%s: bad ISO 639-1 code %q", l.b, l.one)
		}
		if got := LanguageCode(l.b); got != l.b {
			t.Errorf("LanguageCode(%q) = %q, want itself", l.b, got)
		}
		for _, n := range l.names {
			if got := LanguageCode(n); got != l.b {
				t.Errorf("LanguageCode(%q) = %q, want %q", n, got, l.b)
			}
		}
	}
}
