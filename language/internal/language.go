// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:generate go run gen.go gen_common.go -output tables.go

package language

// TODO: Remove above NOTE after:
// - verifying that tables are dropped correctly (most notably matcher tables).

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// maxCoreSize is the maximum size of a BCP 47 tag without variants and
	// extensions. Equals max lang (3) + script (4) + max reg (3) + 2 dashes.
	maxCoreSize = 12

	// max99thPercentileSize is a somewhat arbitrary buffer size that presumably
	// is large enough to hold at least 99% of the BCP 47 tags.
	max99thPercentileSize = 32

	// maxSimpleUExtensionSize is the maximum size of a -u extension with one
	// key-type pair. Equals len("-u-") + key (2) + dash + max value (8).
	maxSimpleUExtensionSize = 14
)

// Tag represents a BCP 47 language tag. It is used to specify an instance of a
// specific language or locale. All language tag values are guaranteed to be
// well-formed.
type Tag struct {
	lang   langID
	region regionID
	// TODO: we will soon run out of positions for script. Idea: instead of
	// storing lang, region, and script codes, store only the compact index and
	// have a lookup table from this code to its expansion. This greatly speeds
	// up table lookup, speed up common variant cases.
	// This will also immediately free up 3 extra bytes. Also, the pVariant
	// field can now be moved to the lookup table, as the compact index uniquely
	// determines the offset of a possible variant.
	script   scriptID
	pVariant byte   // offset in str, includes preceding '-'
	pExt     uint16 // offset of first extension, includes preceding '-'

	// str is the string representation of the Tag. It will only be used if the
	// tag has variants or extensions.
	str string
}

// Make is a convenience wrapper for Parse that omits the error.
// In case of an error, a sensible default is returned.
func Make(s string) Tag {
	t, _ := Parse(s)
	return t
}

// Raw returns the raw base language, script and region, without making an
// attempt to infer their values.
// TODO: consider removing
func (t Tag) Raw() (b langID, s scriptID, r regionID) {
	return t.lang, t.script, t.region
}

// equalTags compares language, script and region subtags only.
func (t Tag) equalTags(a Tag) bool {
	return t.lang == a.lang && t.script == a.script && t.region == a.region
}

// IsRoot returns true if t is equal to language "und".
func (t Tag) IsRoot() bool {
	if int(t.pVariant) < len(t.str) {
		return false
	}
	return t.equalTags(und)
}

// private reports whether the Tag consists solely of a private use tag.
func (t Tag) private() bool {
	return t.str != "" && t.pVariant == 0
}

// remakeString is used to update t.str in case lang, script or region changed.
// It is assumed that pExt and pVariant still point to the start of the
// respective parts.
func (t *Tag) remakeString() {
	if t.str == "" {
		return
	}
	extra := t.str[t.pVariant:]
	if t.pVariant > 0 {
		extra = extra[1:]
	}
	if t.equalTags(und) && strings.HasPrefix(extra, "x-") {
		t.str = extra
		t.pVariant = 0
		t.pExt = 0
		return
	}
	var buf [max99thPercentileSize]byte // avoid extra memory allocation in most cases.
	b := buf[:t.genCoreBytes(buf[:])]
	if extra != "" {
		diff := len(b) - int(t.pVariant)
		b = append(b, '-')
		b = append(b, extra...)
		t.pVariant = uint8(int(t.pVariant) + diff)
		t.pExt = uint16(int(t.pExt) + diff)
	} else {
		t.pVariant = uint8(len(b))
		t.pExt = uint16(len(b))
	}
	t.str = string(b)
}

// genCoreBytes writes a string for the base languages, script and region tags
// to the given buffer and returns the number of bytes written. It will never
// write more than maxCoreSize bytes.
func (t *Tag) genCoreBytes(buf []byte) int {
	n := t.lang.stringToBuf(buf[:])
	if t.script != 0 {
		n += copy(buf[n:], "-")
		n += copy(buf[n:], t.script.String())
	}
	if t.region != 0 {
		n += copy(buf[n:], "-")
		n += copy(buf[n:], t.region.String())
	}
	return n
}

// String returns the canonical string representation of the language tag.
func (t Tag) String() string {
	if t.str != "" {
		return t.str
	}
	if t.script == 0 && t.region == 0 {
		return t.lang.String()
	}
	buf := [maxCoreSize]byte{}
	return string(buf[:t.genCoreBytes(buf[:])])
}

// MarshalText implements encoding.TextMarshaler.
func (t Tag) MarshalText() (text []byte, err error) {
	if t.str != "" {
		text = append(text, t.str...)
	} else if t.script == 0 && t.region == 0 {
		text = append(text, t.lang.String()...)
	} else {
		buf := [maxCoreSize]byte{}
		text = buf[:t.genCoreBytes(buf[:])]
	}
	return text, nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *Tag) UnmarshalText(text []byte) error {
	tag, err := Parse(string(text))
	*t = tag
	return err
}

// Variant returns the variants specified explicitly for this language tag.
// or nil if no variant was specified.
func (t Tag) Variants() []Variant {
	v := []Variant{}
	if int(t.pVariant) < int(t.pExt) {
		for x, str := "", t.str[t.pVariant:t.pExt]; str != ""; {
			x, str = nextToken(str)
			v = append(v, Variant{x})
		}
	}
	return v
}

// Parent returns the CLDR parent of t. In CLDR, missing fields in data for a
// specific language are substituted with fields from the parent language.
// The parent for a language may change for newer versions of CLDR.
func (t Tag) Parent() Tag {
	if t.str != "" {
		// Strip the variants and extensions.
		b, s, r := t.Raw()
		t = Tag{lang: b, script: s, region: r}
		if t.region == 0 && t.script != 0 && t.lang != 0 {
			base, _ := addTags(Tag{lang: t.lang})
			if base.script == t.script {
				return Tag{lang: t.lang}
			}
		}
		return t
	}
	if t.lang != 0 {
		if t.region != 0 {
			maxScript := t.script
			if maxScript == 0 {
				max, _ := addTags(t)
				maxScript = max.script
			}

			for i := range parents {
				if langID(parents[i].lang) == t.lang && scriptID(parents[i].maxScript) == maxScript {
					for _, r := range parents[i].fromRegion {
						if regionID(r) == t.region {
							return Tag{
								lang:   t.lang,
								script: scriptID(parents[i].script),
								region: regionID(parents[i].toRegion),
							}
						}
					}
				}
			}

			// Strip the script if it is the default one.
			base, _ := addTags(Tag{lang: t.lang})
			if base.script != maxScript {
				return Tag{lang: t.lang, script: maxScript}
			}
			return Tag{lang: t.lang}
		} else if t.script != 0 {
			// The parent for an base-script pair with a non-default script is
			// "und" instead of the base language.
			base, _ := addTags(Tag{lang: t.lang})
			if base.script != t.script {
				return und
			}
			return Tag{lang: t.lang}
		}
	}
	return und
}

// returns token t and the rest of the string.
func nextToken(s string) (t, tail string) {
	p := strings.Index(s[1:], "-")
	if p == -1 {
		return s[1:], ""
	}
	p++
	return s[1:p], s[p:]
}

// Extension is a single BCP 47 extension.
type Extension struct {
	s string
}

// String returns the string representation of the extension, including the
// type tag.
func (e Extension) String() string {
	return e.s
}

// ParseExtension parses s as an extension and returns it on success.
func ParseExtension(s string) (e Extension, err error) {
	scan := makeScannerString(s)
	var end int
	if n := len(scan.token); n != 1 {
		return Extension{}, errSyntax
	}
	scan.toLower(0, len(scan.b))
	end = parseExtension(&scan)
	if end != len(s) {
		return Extension{}, errSyntax
	}
	return Extension{string(scan.b)}, nil
}

// Type returns the one-byte extension type of e. It returns 0 for the zero
// exception.
func (e Extension) Type() byte {
	if e.s == "" {
		return 0
	}
	return e.s[0]
}

// Tokens returns the list of tokens of e.
func (e Extension) Tokens() []string {
	return strings.Split(e.s, "-")
}

// Extension returns the extension of type x for tag t. It will return
// false for ok if t does not have the requested extension. The returned
// extension will be invalid in this case.
func (t Tag) Extension(x byte) (ext Extension, ok bool) {
	for i := int(t.pExt); i < len(t.str)-1; {
		var ext string
		i, ext = getExtension(t.str, i)
		if ext[0] == x {
			return Extension{ext}, true
		}
	}
	return Extension{}, false
}

// Extensions returns all extensions of t.
func (t Tag) Extensions() []Extension {
	e := []Extension{}
	for i := int(t.pExt); i < len(t.str)-1; {
		var ext string
		i, ext = getExtension(t.str, i)
		e = append(e, Extension{ext})
	}
	return e
}

// TypeForKey returns the type associated with the given key, where key and type
// are of the allowed values defined for the Unicode locale extension ('u') in
// http://www.unicode.org/reports/tr35/#Unicode_Language_and_Locale_Identifiers.
// TypeForKey will traverse the inheritance chain to get the correct value.
func (t Tag) TypeForKey(key string) string {
	if start, end, _ := t.findTypeForKey(key); end != start {
		return t.str[start:end]
	}
	return ""
}

var (
	errPrivateUse       = errors.New("cannot set a key on a private use tag")
	errInvalidArguments = errors.New("invalid key or type")
)

// SetTypeForKey returns a new Tag with the key set to type, where key and type
// are of the allowed values defined for the Unicode locale extension ('u') in
// http://www.unicode.org/reports/tr35/#Unicode_Language_and_Locale_Identifiers.
// An empty value removes an existing pair with the same key.
func (t Tag) SetTypeForKey(key, value string) (Tag, error) {
	if t.private() {
		return t, errPrivateUse
	}
	if len(key) != 2 {
		return t, errInvalidArguments
	}

	// Remove the setting if value is "".
	if value == "" {
		start, end, _ := t.findTypeForKey(key)
		if start != end {
			// Remove key tag and leading '-'.
			start -= 4

			// Remove a possible empty extension.
			if (end == len(t.str) || t.str[end+2] == '-') && t.str[start-2] == '-' {
				start -= 2
			}
			if start == int(t.pVariant) && end == len(t.str) {
				t.str = ""
				t.pVariant, t.pExt = 0, 0
			} else {
				t.str = fmt.Sprintf("%s%s", t.str[:start], t.str[end:])
			}
		}
		return t, nil
	}

	if len(value) < 3 || len(value) > 8 {
		return t, errInvalidArguments
	}

	var (
		buf    [maxCoreSize + maxSimpleUExtensionSize]byte
		uStart int // start of the -u extension.
	)

	// Generate the tag string if needed.
	if t.str == "" {
		uStart = t.genCoreBytes(buf[:])
		buf[uStart] = '-'
		uStart++
	}

	// Create new key-type pair and parse it to verify.
	b := buf[uStart:]
	copy(b, "u-")
	copy(b[2:], key)
	b[4] = '-'
	b = b[:5+copy(b[5:], value)]
	scan := makeScanner(b)
	if parseExtensions(&scan); scan.err != nil {
		return t, scan.err
	}

	// Assemble the replacement string.
	if t.str == "" {
		t.pVariant, t.pExt = byte(uStart-1), uint16(uStart-1)
		t.str = string(buf[:uStart+len(b)])
	} else {
		s := t.str
		start, end, hasExt := t.findTypeForKey(key)
		if start == end {
			if hasExt {
				b = b[2:]
			}
			t.str = fmt.Sprintf("%s-%s%s", s[:start], b, s[end:])
		} else {
			t.str = fmt.Sprintf("%s%s%s", s[:start], value, s[end:])
		}
	}
	return t, nil
}

// findKeyAndType returns the start and end position for the type corresponding
// to key or the point at which to insert the key-value pair if the type
// wasn't found. The hasExt return value reports whether an -u extension was present.
// Note: the extensions are typically very small and are likely to contain
// only one key-type pair.
func (t Tag) findTypeForKey(key string) (start, end int, hasExt bool) {
	p := int(t.pExt)
	if len(key) != 2 || p == len(t.str) || p == 0 {
		return p, p, false
	}
	s := t.str

	// Find the correct extension.
	for p++; s[p] != 'u'; p++ {
		if s[p] > 'u' {
			p--
			return p, p, false
		}
		if p = nextExtension(s, p); p == len(s) {
			return len(s), len(s), false
		}
	}
	// Proceed to the hyphen following the extension name.
	p++

	// curKey is the key currently being processed.
	curKey := ""

	// Iterate over keys until we get the end of a section.
	for {
		// p points to the hyphen preceding the current token.
		if p3 := p + 3; s[p3] == '-' {
			// Found a key.
			// Check whether we just processed the key that was requested.
			if curKey == key {
				return start, p, true
			}
			// Set to the next key and continue scanning type tokens.
			curKey = s[p+1 : p3]
			if curKey > key {
				return p, p, true
			}
			// Start of the type token sequence.
			start = p + 4
			// A type is at least 3 characters long.
			p += 7 // 4 + 3
		} else {
			// Attribute or type, which is at least 3 characters long.
			p += 4
		}
		// p points past the third character of a type or attribute.
		max := p + 5 // maximum length of token plus hyphen.
		if len(s) < max {
			max = len(s)
		}
		for ; p < max && s[p] != '-'; p++ {
		}
		// Bail if we have exhausted all tokens or if the next token starts
		// a new extension.
		if p == len(s) || s[p+2] == '-' {
			if curKey == key {
				return start, p, true
			}
			return p, p, true
		}
	}
}

// ParseBase parses a 2- or 3-letter ISO 639 code.
// It returns a ValueError if s is a well-formed but unknown language identifier
// or another error if another error occurred.
func ParseBase(s string) (langID, error) {
	if n := len(s); n < 2 || 3 < n {
		return 0, errSyntax
	}
	var buf [3]byte
	return getLangID(buf[:copy(buf[:], s)])
}

// ParseScript parses a 4-letter ISO 15924 code.
// It returns a ValueError if s is a well-formed but unknown script identifier
// or another error if another error occurred.
func ParseScript(s string) (scriptID, error) {
	if len(s) != 4 {
		return 0, errSyntax
	}
	var buf [4]byte
	return getScriptID(script, buf[:copy(buf[:], s)])
}

// EncodeM49 returns the Region for the given UN M.49 code.
// It returns an error if r is not a valid code.
func EncodeM49(r int) (regionID, error) {
	return getRegionM49(r)
}

// ParseRegion parses a 2- or 3-letter ISO 3166-1 or a UN M.49 code.
// It returns a ValueError if s is a well-formed but unknown region identifier
// or another error if another error occurred.
func ParseRegion(s string) (regionID, error) {
	if n := len(s); n < 2 || 3 < n {
		return 0, errSyntax
	}
	var buf [3]byte
	return getRegionID(buf[:copy(buf[:], s)])
}

// IsCountry returns whether this region is a country or autonomous area. This
// includes non-standard definitions from CLDR.
func (r regionID) IsCountry() bool {
	if r == 0 || r.IsGroup() || r.IsPrivateUse() && r != _XK {
		return false
	}
	return true
}

// IsGroup returns whether this region defines a collection of regions. This
// includes non-standard definitions from CLDR.
func (r regionID) IsGroup() bool {
	if r == 0 {
		return false
	}
	return int(regionInclusion[r]) < len(regionContainment)
}

// Contains returns whether Region c is contained by Region r. It returns true
// if c == r.
func (r regionID) Contains(c regionID) bool {
	if r == c {
		return true
	}
	g := regionInclusion[r]
	if g >= nRegionGroups {
		return false
	}
	m := regionContainment[g]

	d := regionInclusion[c]
	b := regionInclusionBits[d]

	// A contained country may belong to multiple disjoint groups. Matching any
	// of these indicates containment. If the contained region is a group, it
	// must strictly be a subset.
	if d >= nRegionGroups {
		return b&m != 0
	}
	return b&^m == 0
}

var errNoTLD = errors.New("language: region is not a valid ccTLD")

// TLD returns the country code top-level domain (ccTLD). UK is returned for GB.
// In all other cases it returns either the region itself or an error.
//
// This method may return an error for a region for which there exists a
// canonical form with a ccTLD. To get that ccTLD canonicalize r first. The
// region will already be canonicalized it was obtained from a Tag that was
// obtained using any of the default methods.
func (r regionID) TLD() (regionID, error) {
	// See http://en.wikipedia.org/wiki/Country_code_top-level_domain for the
	// difference between ISO 3166-1 and IANA ccTLD.
	if r == _GB {
		r = _UK
	}
	if (r.typ() & ccTLD) == 0 {
		return 0, errNoTLD
	}
	return r, nil
}

// Canonicalize returns the region or a possible replacement if the region is
// deprecated. It will not return a replacement for deprecated regions that
// are split into multiple regions.
func (r regionID) Canonicalize() regionID {
	if cr := normRegion(r); cr != 0 {
		return cr
	}
	return r
}

// Variant represents a registered variant of a language as defined by BCP 47.
type Variant struct {
	variant string
}

// ParseVariant parses and returns a Variant. An error is returned if s is not
// a valid variant.
func ParseVariant(s string) (Variant, error) {
	s = strings.ToLower(s)
	if _, ok := variantIndex[s]; ok {
		return Variant{s}, nil
	}
	return Variant{}, mkErrInvalid([]byte(s))
}

// String returns the string representation of the variant.
func (v Variant) String() string {
	return v.variant
}
