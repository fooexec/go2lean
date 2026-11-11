package semantics

import "sync"

// go2lean's own tests (#193): sync.Map is a map, empty at first, as
// msgpack's and go-toml's per-type caches use it.

type typeCache struct{ m sync.Map }

var globalCache sync.Map

func testSyncMapEmpty() bool {
	var c typeCache
	v, ok := c.m.Load("k")
	return v == nil && !ok
}

func testSyncMapStoreLoad() bool {
	var c typeCache
	c.m.Store("a", 1)
	c.m.Store("b", 2)
	c.m.Store("a", 3)
	v, ok := c.m.Load("a")
	w, ok2 := c.m.Load("b")
	_, ok3 := c.m.Load("c")
	return ok && v.(int) == 3 && ok2 && w.(int) == 2 && !ok3
}

func testSyncMapLoadOrStore() bool {
	var c typeCache
	v, loaded := c.m.LoadOrStore("k", 1)
	w, loaded2 := c.m.LoadOrStore("k", 2)
	return !loaded && v.(int) == 1 && loaded2 && w.(int) == 1
}

func testSyncMapDeleteSwap() bool {
	var c typeCache
	prev, loaded := c.m.Swap("k", 1)
	if prev != nil || loaded {
		return false
	}
	prev, loaded = c.m.Swap("k", 2)
	if !loaded || prev.(int) != 1 {
		return false
	}
	v, ok := c.m.LoadAndDelete("k")
	if !ok || v.(int) != 2 {
		return false
	}
	c.m.Store("j", 5)
	c.m.Delete("j")
	_, ok = c.m.Load("j")
	return !ok
}

func testSyncMapGlobal() bool {
	globalCache.Store(7, "seven")
	v, ok := globalCache.Load(7)
	globalCache.Delete(7)
	_, ok2 := globalCache.Load(7)
	return ok && v.(string) == "seven" && !ok2
}
