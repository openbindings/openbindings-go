// Package jstring recognizes the strings the SDK's JSON codec produces: a
// JSON string escape of an isolated UTF-16 surrogate is carried exactly, in
// its three-byte WTF-8 encoding, where other decoders substitute U+FFFD.
package jstring

import "unicode/utf8"

// CodeUnit encodes one UTF-16 code unit, including an isolated surrogate.
func CodeUnit(unit uint16) string {
	if unit < 0xd800 || unit > 0xdfff {
		return string(rune(unit))
	}
	return string([]byte{0xe0 | byte(unit>>12), 0x80 | byte(unit>>6)&0x3f, 0x80 | byte(unit)&0x3f})
}

func surrogate(s string) uint16 {
	if len(s) != 3 || s[0] != 0xed || s[1] < 0xa0 || s[1] > 0xbf || s[2]&0xc0 != 0x80 {
		return 0
	}
	return uint16(s[0]&0xf)<<12 | uint16(s[1]&0x3f)<<6 | uint16(s[2]&0x3f)
}

// Valid reports whether s uses canonical UTF-8/WTF-8, rather than arbitrary
// malformed bytes. A surrogate pair must use its scalar UTF-8 encoding.
func Valid(s string) bool {
	var previous uint16
	for len(s) > 0 {
		if len(s) >= 3 {
			if unit := surrogate(s[:3]); unit != 0 {
				if previous >= 0xd800 && previous <= 0xdbff && unit >= 0xdc00 {
					return false
				}
				previous = unit
				s = s[3:]
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s)
		if size == 1 && s[0] >= 0x80 {
			return false
		}
		previous = 0
		s = s[size:]
	}
	return true
}
