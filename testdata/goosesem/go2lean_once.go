package semantics

import "sync"

// go2lean's own tests (#210): a table built lazily under a package-level
// sync.Once, as hash/crc32 builds its tables. The model runs the body once,
// before the trials; every Do then sees it done, as Go's later Do calls do.

var (
	onceTable    sync.Once
	onceTableVal *[256]uint32
	onceCalls    int
)

func onceBuild() {
	t := new([256]uint32)
	for i := range t {
		c := uint32(i)
		for j := 0; j < 8; j++ {
			if c&1 == 1 {
				c = (c >> 1) ^ 0xedb88320
			} else {
				c >>= 1
			}
		}
		t[i] = c
	}
	onceTableVal = t
	onceCalls++
}

func onceCRC(b []byte) uint32 {
	onceTable.Do(onceBuild)
	c := ^uint32(0)
	for _, x := range b {
		c = onceTableVal[byte(c)^x] ^ (c >> 8)
	}
	return ^c
}

func testOnceLazyTable() bool {
	return onceCRC([]byte("a")) == 0xe8b7be43 && onceCRC([]byte("abc")) == 0x352441c2 &&
		onceTableVal[1] == 0x77073096 && onceCalls == 1
}
