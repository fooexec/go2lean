package semantics

import (
	"reflect"
	"unsafe"
)

// The zero-copy conversions between strings and byte slices that cast
// pointers through unsafe.Pointer (tidwall/gjson and sjson): each is a
// view of the same bytes.

func unsafeBytesString(b []byte) string { return *(*string)(unsafe.Pointer(&b)) }

func testUnsafeBytesToString() bool {
	b := []byte("héllo")
	s := unsafeBytesString(b)
	return s == "héllo" && len(s) == 6 && unsafeBytesString(nil) == ""
}

type semStringHeader struct {
	data unsafe.Pointer
	len  int
}

type semSliceHeader struct {
	data unsafe.Pointer
	len  int
	cap  int
}

func unsafeStringBytes(s string) []byte {
	return *(*[]byte)(unsafe.Pointer(&semSliceHeader{
		data: (*semStringHeader)(unsafe.Pointer(&s)).data,
		len:  len(s),
		cap:  len(s),
	}))
}

func testUnsafeStringHeaderToBytes() bool {
	b := unsafeStringBytes("abc")
	return len(b) == 3 && cap(b) == 3 && b[0] == 'a' && b[2] == 'c' && string(b[1:]) == "bc"
}

func testUnsafeHeaderFields() bool {
	s := "json"
	h := *(*semStringHeader)(unsafe.Pointer(&s))
	bh := semSliceHeader{data: h.data, len: h.len, cap: h.len}
	b := *(*[]byte)(unsafe.Pointer(&bh))
	return h.len == 4 && string(b) == "json"
}

func testUnsafeReflectHeader() bool {
	b := []byte("xyz")
	sh := (*reflect.SliceHeader)(unsafe.Pointer(&b))
	return sh.Len == 3 && sh.Cap == 3 && *(*string)(unsafe.Pointer(&b)) == "xyz"
}
