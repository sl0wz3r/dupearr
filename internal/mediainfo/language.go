package mediainfo

import (
	"strings"
)

// language is one row of the language table: the ISO 639-2/B code, the ISO 639-2/T code when it
// differs, the ISO 639-1 code (if any) and lower-case names (English first, then native names
// commonly seen in media-server/*arr output).
type language struct {
	b, t, one string
	names     []string
}

var languageTable = []language{
	{"eng", "", "en", []string{"english"}},
	{"fre", "fra", "fr", []string{"french", "français", "francais"}},
	{"ger", "deu", "de", []string{"german", "deutsch"}},
	{"spa", "", "es", []string{"spanish", "español", "espanol", "castilian", "latin american spanish"}},
	{"ita", "", "it", []string{"italian", "italiano"}},
	{"por", "", "pt", []string{"portuguese", "português", "portugues", "brazilian portuguese", "brazilian"}},
	{"rus", "", "ru", []string{"russian", "русский"}},
	{"jpn", "", "ja", []string{"japanese", "日本語"}},
	{"chi", "zho", "zh", []string{"chinese", "mandarin", "中文"}},
	{"kor", "", "ko", []string{"korean", "한국어"}},
	{"ara", "", "ar", []string{"arabic"}},
	{"hin", "", "hi", []string{"hindi"}},
	{"dut", "nld", "nl", []string{"dutch", "flemish", "nederlands"}},
	{"swe", "", "sv", []string{"swedish", "svenska"}},
	{"nor", "", "no", []string{"norwegian", "norsk"}},
	{"nob", "", "nb", []string{"norwegian bokmål", "norwegian bokmal", "bokmål", "bokmal"}},
	{"nno", "", "nn", []string{"norwegian nynorsk", "nynorsk"}},
	{"dan", "", "da", []string{"danish", "dansk"}},
	{"fin", "", "fi", []string{"finnish", "suomi"}},
	{"pol", "", "pl", []string{"polish", "polski"}},
	{"cze", "ces", "cs", []string{"czech", "čeština", "cestina"}},
	{"slo", "slk", "sk", []string{"slovak", "slovenčina", "slovencina"}},
	{"hun", "", "hu", []string{"hungarian", "magyar"}},
	{"rum", "ron", "ro", []string{"romanian", "moldavian", "moldovan", "română", "romana"}},
	{"bul", "", "bg", []string{"bulgarian"}},
	{"gre", "ell", "el", []string{"greek", "modern greek"}},
	{"tur", "", "tr", []string{"turkish", "türkçe", "turkce"}},
	{"heb", "", "he", []string{"hebrew"}},
	{"tha", "", "th", []string{"thai"}},
	{"vie", "", "vi", []string{"vietnamese", "tiếng việt"}},
	{"ind", "", "id", []string{"indonesian", "bahasa indonesia"}},
	{"may", "msa", "ms", []string{"malay", "bahasa melayu"}},
	{"tgl", "", "tl", []string{"tagalog"}},
	{"fil", "", "", []string{"filipino"}},
	{"ukr", "", "uk", []string{"ukrainian"}},
	{"hrv", "", "hr", []string{"croatian", "hrvatski"}},
	{"srp", "", "sr", []string{"serbian", "srpski"}},
	{"slv", "", "sl", []string{"slovenian", "slovene", "slovenščina"}},
	{"bos", "", "bs", []string{"bosnian"}},
	{"mac", "mkd", "mk", []string{"macedonian"}},
	{"alb", "sqi", "sq", []string{"albanian", "shqip"}},
	{"est", "", "et", []string{"estonian", "eesti"}},
	{"lav", "", "lv", []string{"latvian", "latviešu"}},
	{"lit", "", "lt", []string{"lithuanian", "lietuvių"}},
	{"ice", "isl", "is", []string{"icelandic", "íslenska", "islenska"}},
	{"per", "fas", "fa", []string{"persian", "farsi"}},
	{"urd", "", "ur", []string{"urdu"}},
	{"ben", "", "bn", []string{"bengali", "bangla"}},
	{"tam", "", "ta", []string{"tamil"}},
	{"tel", "", "te", []string{"telugu"}},
	{"mar", "", "mr", []string{"marathi"}},
	{"kan", "", "kn", []string{"kannada"}},
	{"mal", "", "ml", []string{"malayalam"}},
	{"guj", "", "gu", []string{"gujarati"}},
	{"pan", "", "pa", []string{"punjabi", "panjabi"}},
	{"cat", "", "ca", []string{"catalan", "català", "catala", "valencian"}},
	{"baq", "eus", "eu", []string{"basque", "euskara"}},
	{"glg", "", "gl", []string{"galician", "galego"}},
	{"wel", "cym", "cy", []string{"welsh", "cymraeg"}},
	{"gle", "", "ga", []string{"irish", "gaeilge"}},
	{"arm", "hye", "hy", []string{"armenian"}},
	{"geo", "kat", "ka", []string{"georgian"}},
	{"aze", "", "az", []string{"azerbaijani"}},
	{"kaz", "", "kk", []string{"kazakh"}},
	{"uzb", "", "uz", []string{"uzbek"}},
	{"mon", "", "mn", []string{"mongolian"}},
	{"nep", "", "ne", []string{"nepali"}},
	{"sin", "", "si", []string{"sinhala", "sinhalese"}},
	{"khm", "", "km", []string{"khmer", "central khmer"}},
	{"lao", "", "lo", []string{"lao"}},
	{"bur", "mya", "my", []string{"burmese"}},
	{"amh", "", "am", []string{"amharic"}},
	{"swa", "", "sw", []string{"swahili"}},
	{"afr", "", "af", []string{"afrikaans"}},
	{"zul", "", "zu", []string{"zulu"}},
	{"xho", "", "xh", []string{"xhosa"}},
	{"yor", "", "yo", []string{"yoruba"}},
	{"ibo", "", "ig", []string{"igbo"}},
	{"hau", "", "ha", []string{"hausa"}},
	{"som", "", "so", []string{"somali"}},
	{"lat", "", "la", []string{"latin"}},
	{"epo", "", "eo", []string{"esperanto"}},
	{"tib", "bod", "bo", []string{"tibetan"}},
	{"mao", "mri", "mi", []string{"maori", "māori"}},
	{"haw", "", "", []string{"hawaiian"}},
	{"yid", "", "yi", []string{"yiddish"}},
	{"bel", "", "be", []string{"belarusian"}},
	{"ltz", "", "lb", []string{"luxembourgish", "letzeburgesch"}},
	{"fao", "", "fo", []string{"faroese"}},
	{"kur", "", "ku", []string{"kurdish"}},
	{"pus", "", "ps", []string{"pashto", "pushto"}},
	{"tat", "", "tt", []string{"tatar"}},
	{"mul", "", "", []string{"multiple languages", "multi", "multiple"}},
}

// languageIndex maps every known code/name (lower case) to its ISO 639-2/B code. Built once at
// package initialization and never written afterwards (read-only lookup table).
var languageIndex = buildLanguageIndex()

// Legacy ISO 639-1 codes still emitted by some tools.
var legacyLanguageCodes = map[string]string{"iw": "heb", "in": "ind", "ji": "yid", "jw": "jav"}

func buildLanguageIndex() map[string]string {
	idx := make(map[string]string, len(languageTable)*5)
	for _, l := range languageTable {
		idx[l.b] = l.b
		if l.t != "" {
			idx[l.t] = l.b
		}
		if l.one != "" {
			idx[l.one] = l.b
		}
		for _, n := range l.names {
			idx[n] = l.b
		}
	}
	for k, v := range legacyLanguageCodes {
		idx[k] = v
	}
	return idx
}

// LanguageCode normalizes a language code or name to its ISO 639-2/B code: "eng", "en",
// "en-US", "English", "English (United States)" → "eng"; "deu"/"de"/"German"/"Deutsch" → "ger";
// "fra" → "fre", "zho" → "chi", "pt-BR"/"Brazilian Portuguese" → "por". Undetermined values
// ("und", "unknown", "zxx", "") return "". An unrecognized three-letter code is returned lower
// case as-is (it is most likely already an ISO 639-2 code); any other unrecognized value
// returns "".
func LanguageCode(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return ""
	}
	switch v {
	case "und", "unknown", "undetermined", "zxx", "none", "n/a":
		return ""
	}
	if c, ok := languageIndex[v]; ok {
		return c
	}
	// "English (United States)" → "english"; "Portuguese (Brazil)" → "portuguese".
	if i := strings.IndexAny(v, "(["); i > 0 {
		if c := LanguageCode(v[:i]); c != "" {
			return c
		}
	}
	// BCP 47 / locale tags: "en-US", "pt_BR", "zh-Hant" → primary subtag.
	if i := strings.IndexAny(v, "-_"); i > 0 {
		if c, ok := languageIndex[v[:i]]; ok {
			return c
		}
	}
	if len(v) == 3 && isASCIILetters(v) {
		return v
	}
	return ""
}

func isASCIILetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return s != ""
}
