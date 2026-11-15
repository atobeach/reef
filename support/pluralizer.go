package support

import (
	"regexp"
	"strings"
)

// Plural is the English plural of a word, by the standard English inflection
// rules: the uninflected patterns, then the irregular words, then the first
// matching regular rule, with the result matched to the input's case (lower,
// UPPER, Ucfirst, Ucwords).
func Plural(value string) string {
	lowered := strings.ToLower(value)
	if lowered == "recommended" || lowered == "related" || !endsAlphanumeric.MatchString(value) {
		return value
	}

	return matchCase(pluralize(value), value)
}

var endsAlphanumeric = regexp.MustCompile(`^(.*)[A-Za-z0-9\x{0080}-\x{FFFF}]$`)

func pluralize(word string) string {
	if word == "" {
		return ""
	}

	if uninflectedPlural.MatchString(word) {
		return word
	}

	lower := strings.ToLower(word)
	if to, ok := irregularPlurals[lower]; ok {
		if lower[0] != word[0] {
			return strings.ToUpper(to[:1]) + to[1:]
		}

		return to
	}

	for _, rule := range pluralRules {
		if rule.pattern.MatchString(word) {
			return rule.pattern.ReplaceAllString(word, rule.replacement)
		}
	}

	return word
}

func matchCase(value string, comparison string) string {
	switch comparison {
	case strings.ToLower(comparison):
		return strings.ToLower(value)
	case strings.ToUpper(comparison):
		return strings.ToUpper(value)
	case ucfirst(comparison):
		return ucfirst(value)
	case ucwords(comparison):
		return ucwords(value)
	}

	return value
}

func ucfirst(s string) string {
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

// ucwords upper-cases the ASCII letter opening each whitespace-separated
// word. Bytes past ASCII are left alone: they are UTF-8 sequence bytes, not
// Latin-1 letters, and case-mapping them would corrupt the string.
func ucwords(s string) string {
	out := []byte(s)
	for i, b := range out {
		if 'a' <= b && b <= 'z' && (i == 0 || strings.IndexByte(" \t\r\n\f\v", out[i-1]) >= 0) {
			out[i] = b - 'a' + 'A'
		}
	}

	return string(out)
}

type pluralRule struct {
	pattern     *regexp.Regexp
	replacement string
}

// rule compiles a case-insensitive inflection pattern with its `\N`
// back-references rewritten to Go's `${N}`.
func rule(pattern string, replacement string) pluralRule {
	return pluralRule{
		pattern:     regexp.MustCompile("(?i)" + pattern),
		replacement: backReference.ReplaceAllString(replacement, "$${$1}"),
	}
}

var backReference = regexp.MustCompile(`\\(\d)`)

// pluralRules are the regular plural rules, first match wins.
var pluralRules = []pluralRule{
	rule(`(s)tatus$`, `\1\2tatuses`),
	rule(`(quiz)$`, `\1zes`),
	rule(`^(ox)$`, `\1\2en`),
	rule(`([m|l])ouse$`, `\1ice`),
	rule(`(matr|vert|ind)(ix|ex)$`, `\1ices`),
	rule(`(x|ch|ss|sh)$`, `\1es`),
	rule(`([^aeiouy]|qu)y$`, `\1ies`),
	rule(`(hive|gulf)$`, `\1s`),
	rule(`(?:([^f])fe|([lr])f)$`, `\1\2ves`),
	rule(`sis$`, `ses`),
	rule(`([ti])um$`, `\1a`),
	rule(`(tax)on$`, `\1a`),
	rule(`(c)riterion$`, `\1riteria`),
	rule(`(p)erson$`, `\1eople`),
	rule(`(m)an$`, `\1en`),
	rule(`(c)hild$`, `\1hildren`),
	rule(`(f)oot$`, `\1eet`),
	rule(`(buffal|her|potat|tomat|volcan)o$`, `\1\2oes`),
	rule(`(alumn|bacill|cact|foc|fung|nucle|radi|stimul|syllab|termin|vir)us$`, `\1i`),
	rule(`us$`, `uses`),
	rule(`(alias)$`, `\1es`),
	rule(`(analys|ax|cris|test|thes)is$`, `\1es`),
	rule(`s$`, `s`),
	rule(`^$`, ``),
	rule(`$`, `s`),
}

// irregularPlurals are the words no regular rule inflects.
var irregularPlurals = map[string]string{
	"abuse": "abuses", "alga": "algae", "atlas": "atlases", "avalanche": "avalanches",
	"axis": "axes", "axe": "axes", "beef": "beefs", "blouse": "blouses", "brother": "brothers",
	"brownie": "brownies", "cache": "caches", "cafe": "cafes", "canvas": "canvases",
	"cave": "caves", "chateau": "chateaux", "child": "children", "cookie": "cookies",
	"corpus": "corpuses", "cow": "cows", "criterion": "criteria", "curriculum": "curricula",
	"curve": "curves", "demo": "demos", "die": "dice", "domino": "dominoes", "echo": "echoes",
	"emphasis": "emphases", "epoch": "epochs", "foe": "foes", "foot": "feet", "fungus": "fungi",
	"ganglion": "ganglions", "gas": "gases", "genie": "genies", "genus": "genera",
	"goose": "geese", "graffito": "graffiti", "grave": "graves", "hippopotamus": "hippopotami",
	"hoax": "hoaxes", "hoof": "hoofs", "human": "humans", "iris": "irises", "larva": "larvae",
	"leaf": "leaves", "lens": "lenses", "loaf": "loaves", "man": "men", "medium": "media",
	"memorandum": "memoranda", "money": "monies", "mongoose": "mongooses", "motto": "mottoes",
	"move": "moves", "mythos": "mythoi", "neurosis": "neuroses", "niche": "niches",
	"niveau": "niveaux", "nucleus": "nuclei", "numen": "numina", "nursery": "nurseries",
	"oasis": "oases", "occiput": "occiputs", "octopus": "octopuses", "opus": "opuses",
	"ox": "oxen", "passerby": "passersby", "penis": "penises", "person": "people",
	"plateau": "plateaux", "runner-up": "runners-up", "safe": "safes", "save": "saves",
	"sex": "sexes", "sieve": "sieves", "soliloquy": "soliloquies", "son-in-law": "sons-in-law",
	"stadium": "stadiums", "syllabus": "syllabi", "testis": "testes", "thief": "thieves",
	"tooth": "teeth", "tornado": "tornadoes", "trilby": "trilbys", "turf": "turfs",
	"valve": "valves", "volcano": "volcanoes", "wave": "waves", "zombie": "zombies",
}

// uninflectedPlural matches the words whose plural is the word itself, as
// one case-insensitive whole-word alternation.
var uninflectedPlural = regexp.MustCompile(`(?i)^(?:` + strings.Join([]string{
	`\w+media`, `advice`, `aircraft`, `amoyese`, `art`, `audio`, `baggage`, `bison`, `borghese`,
	`bream`, `breeches`, `britches`, `buffalo`, `butter`, `cantus`, `carp`, `cattle`, `chassis`,
	`clippers`, `clothing`, `coal`, `cod`, `coitus`, `compensation`, `congoese`, `contretemps`,
	`coreopsis`, `corps`, `cotton`, `data`, `debris`, `deer`, `diabetes`, `djinn`, `education`,
	`eland`, `elk`, `emoji`, `equipment`, `evidence`, `faroese`, `feedback`, `fish`, `flounder`,
	`flour`, `foochowese`, `food`, `furniture`, `gallows`, `genevese`, `genoese`, `gilbertese`,
	`gold`, `headquarters`, `herpes`, `hijinks`, `homework`, `hottentotese`, `impatience`,
	`information`, `innings`, `jackanapes`, `jeans`, `jedi`, `kin`, `kiplingese`, `knowledge`,
	`kongoese`, `leather`, `love`, `lucchese`, `luggage`, `mackerel`, `Maltese`, `management`,
	`metadata`, `mews`, `money`, `moose`, `mumps`, `music`, `nankingese`, `news`, `nexus`,
	`niasese`, `nutrition`, `offspring`, `oil`, `patience`, `pekingese`, `piedmontese`,
	`pincers`, `pistoiese`, `plankton`, `pliers`, `pokemon`, `police`, `polish`, `portuguese`,
	`proceedings`, `progress`, `rabies`, `rain`, `research`, `rhinoceros`, `rice`, `salmon`,
	`sand`, `sarawakese`, `scissors`, `sea[- ]bass`, `series`, `shavese`, `shears`, `sheep`,
	`siemens`, `silk`, `sms`, `soap`, `social media`, `spam`, `species`, `staff`, `sugar`,
	`swine`, `talent`, `toothpaste`, `traffic`, `travel`, `trousers`, `trout`, `tuna`, `us`,
	`vermontese`, `vinegar`, `weather`, `wenchowese`, `wheat`, `whiting`, `wildebeest`, `wood`,
	`wool`, `yengeese`,
	`people`, `trivia`, `\w+ware$`, `media`,
}, "|") + `)$`)
