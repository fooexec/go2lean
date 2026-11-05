package semantics

import (
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
)

// go2lean's own tests of encoding/base32, base64 and hex, translated from
// their source: round trips, and the encodings Go's own tests check.

var b32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

func testBase32RoundTrip() bool {
	src := []byte("go2lean")
	enc := make([]byte, base32.StdEncoding.EncodedLen(len(src)))
	base32.StdEncoding.Encode(enc, src)
	dec := make([]byte, base32.StdEncoding.DecodedLen(len(enc)))
	n, err := base32.StdEncoding.Decode(dec, enc)
	return string(enc) == "M5XTE3DFMFXA====" && err == nil && string(dec[:n]) == "go2lean"
}

// A package variable built with WithPadding, as nkeys builds its encoder.
func testBase32NoPadding() bool {
	enc := b32NoPad.EncodeToString([]byte{0xff, 0x00})
	dec, err := b32NoPad.DecodeString(enc)
	return enc == "74AA" && err == nil && len(dec) == 2 && dec[0] == 0xff && dec[1] == 0
}

func testBase64RoundTrip() bool {
	enc := base64.StdEncoding.EncodeToString([]byte("hi!?"))
	dec, err := base64.StdEncoding.DecodeString(enc)
	_, bad := base64.StdEncoding.DecodeString("@@@@")
	return enc == "aGkhPw==" && err == nil && string(dec) == "hi!?" && bad != nil
}

func testHexRoundTrip() bool {
	enc := hex.EncodeToString([]byte{0xde, 0xad, 0x01})
	dec, err := hex.DecodeString(enc)
	_, bad := hex.DecodeString("zz")
	return enc == "dead01" && err == nil && len(dec) == 3 && dec[0] == 0xde && bad != nil
}
