package disc

import "strings"

// Language codes on discs: Blu-ray streams carry ISO 639-2 codes (either the bibliographic
// "fre" or the terminological "fra" form), DVDs ISO 639-1 codes ("fr"). Dupearr's models use
// ISO 639-2/B, so both are normalized here. The package deliberately does not import
// internal/mediainfo (which may come to depend on this package); the tables below are the
// complete ISO 639-1 set and the complete list of 639-2 codes whose T and B forms differ.

// iso6392TtoB maps every ISO 639-2/T code that differs from its /B form.
var iso6392TtoB = map[string]string{
	"bod": "tib", "ces": "cze", "cym": "wel", "deu": "ger", "ell": "gre", "eus": "baq",
	"fas": "per", "fra": "fre", "hye": "arm", "isl": "ice", "kat": "geo", "mkd": "mac",
	"mri": "mao", "msa": "may", "mya": "bur", "nld": "dut", "ron": "rum", "slk": "slo",
	"sqi": "alb", "zho": "chi",
}

// iso6391 maps ISO 639-1 codes (and the legacy iw/in/ji/jw) to ISO 639-2/B.
var iso6391 = map[string]string{
	"aa": "aar", "ab": "abk", "ae": "ave", "af": "afr", "ak": "aka", "am": "amh", "an": "arg",
	"ar": "ara", "as": "asm", "av": "ava", "ay": "aym", "az": "aze", "ba": "bak", "be": "bel",
	"bg": "bul", "bh": "bih", "bi": "bis", "bm": "bam", "bn": "ben", "bo": "tib", "br": "bre",
	"bs": "bos", "ca": "cat", "ce": "che", "ch": "cha", "co": "cos", "cr": "cre", "cs": "cze",
	"cu": "chu", "cv": "chv", "cy": "wel", "da": "dan", "de": "ger", "dv": "div", "dz": "dzo",
	"ee": "ewe", "el": "gre", "en": "eng", "eo": "epo", "es": "spa", "et": "est", "eu": "baq",
	"fa": "per", "ff": "ful", "fi": "fin", "fj": "fij", "fo": "fao", "fr": "fre", "fy": "fry",
	"ga": "gle", "gd": "gla", "gl": "glg", "gn": "grn", "gu": "guj", "gv": "glv", "ha": "hau",
	"he": "heb", "hi": "hin", "ho": "hmo", "hr": "hrv", "ht": "hat", "hu": "hun", "hy": "arm",
	"hz": "her", "ia": "ina", "id": "ind", "ie": "ile", "ig": "ibo", "ii": "iii", "ik": "ipk",
	"io": "ido", "is": "ice", "it": "ita", "iu": "iku", "ja": "jpn", "jv": "jav", "ka": "geo",
	"kg": "kon", "ki": "kik", "kj": "kua", "kk": "kaz", "kl": "kal", "km": "khm", "kn": "kan",
	"ko": "kor", "kr": "kau", "ks": "kas", "ku": "kur", "kv": "kom", "kw": "cor", "ky": "kir",
	"la": "lat", "lb": "ltz", "lg": "lug", "li": "lim", "ln": "lin", "lo": "lao", "lt": "lit",
	"lu": "lub", "lv": "lav", "mg": "mlg", "mh": "mah", "mi": "mao", "mk": "mac", "ml": "mal",
	"mn": "mon", "mr": "mar", "ms": "may", "mt": "mlt", "my": "bur", "na": "nau", "nb": "nob",
	"nd": "nde", "ne": "nep", "ng": "ndo", "nl": "dut", "nn": "nno", "no": "nor", "nr": "nbl",
	"nv": "nav", "ny": "nya", "oc": "oci", "oj": "oji", "om": "orm", "or": "ori", "os": "oss",
	"pa": "pan", "pi": "pli", "pl": "pol", "ps": "pus", "pt": "por", "qu": "que", "rm": "roh",
	"rn": "run", "ro": "rum", "ru": "rus", "rw": "kin", "sa": "san", "sc": "srd", "sd": "snd",
	"se": "sme", "sg": "sag", "si": "sin", "sk": "slo", "sl": "slv", "sm": "smo", "sn": "sna",
	"so": "som", "sq": "alb", "sr": "srp", "ss": "ssw", "st": "sot", "su": "sun", "sv": "swe",
	"sw": "swa", "ta": "tam", "te": "tel", "tg": "tgk", "th": "tha", "ti": "tir", "tk": "tuk",
	"tl": "tgl", "tn": "tsn", "to": "ton", "tr": "tur", "ts": "tso", "tt": "tat", "tw": "twi",
	"ty": "tah", "ug": "uig", "uk": "ukr", "ur": "urd", "uz": "uzb", "ve": "ven", "vi": "vie",
	"vo": "vol", "wa": "wln", "wo": "wol", "xh": "xho", "yi": "yid", "yo": "yor", "za": "zha",
	"zh": "chi", "zu": "zul",
	"iw": "heb", "in": "ind", "ji": "yid", "jw": "jav",
}

// langCode normalizes a disc language field to ISO 639-2/B: 3-letter codes are lower-cased and
// mapped T → B ("fra" → "fre"), 2-letter codes mapped from ISO 639-1 ("fr" → "fre"). Empty,
// padding, non-letter, unknown 2-letter values and "und"/"zxx" return "".
func langCode(raw string) string {
	s := strings.ToLower(strings.TrimRight(raw, "\x00 "))
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return ""
		}
	}
	switch len(s) {
	case 2:
		return iso6391[s]
	case 3:
		switch s {
		case "und", "zxx":
			return ""
		}
		if b, ok := iso6392TtoB[s]; ok {
			return b
		}
		return s
	}
	return ""
}
