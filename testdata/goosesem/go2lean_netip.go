package semantics

import "net/netip"

// net/netip, translated from its source with unique.Make interned (#256):
// the client-IP middlewares of chi and echo parse, unmap and compare
// addresses and check prefixes.

func testNetipParseV4() bool {
	a, err := netip.ParseAddr("192.168.1.10")
	return err == nil && a.Is4() && !a.Is6() && a.String() == "192.168.1.10" && a.IsPrivate()
}

func testNetipParseV6() bool {
	a, err := netip.ParseAddr("2001:db8::1")
	b, err2 := netip.ParseAddr("2001:0db8:0:0:0:0:0:1")
	return err == nil && err2 == nil && a.Is6() && a == b && a.String() == "2001:db8::1"
}

func testNetipMapped() bool {
	a, err := netip.ParseAddr("::ffff:10.0.0.1")
	v4, _ := netip.ParseAddr("10.0.0.1")
	return err == nil && a.Is6() && a.Is4In6() && a != v4 && a.Unmap() == v4 && a.Unmap().Is4()
}

func testNetipZone() bool {
	a, err := netip.ParseAddr("fe80::1%eth0")
	b, _ := netip.ParseAddr("fe80::1")
	return err == nil && a.Zone() == "eth0" && a != b && a.WithZone("") == b && b.Zone() == ""
}

func testNetipInvalid() bool {
	_, e1 := netip.ParseAddr("1.2.3")
	_, e2 := netip.ParseAddr("01.2.3.4")
	_, e3 := netip.ParseAddr("not-an-ip")
	_, e4 := netip.ParseAddr("")
	return e1 != nil && e2 != nil && e3 != nil && e4 != nil
}

func testNetipPrefix() bool {
	p, err := netip.ParsePrefix("10.0.0.0/8")
	in, _ := netip.ParseAddr("10.9.9.9")
	out, _ := netip.ParseAddr("11.0.0.1")
	mapped, _ := netip.ParseAddr("::ffff:10.0.0.1")
	return err == nil && p.Contains(in) && !p.Contains(out) && !p.Contains(mapped) &&
		p.Contains(mapped.Unmap()) && p.Bits() == 8
}

func testNetipAddrPort() bool {
	ap, err := netip.ParseAddrPort("[2001:db8::9]:443")
	ap4, err4 := netip.ParseAddrPort("1.2.3.4:5678")
	_, bad := netip.ParseAddrPort("1.2.3.4")
	return err == nil && err4 == nil && bad != nil && ap.Port() == 443 && ap.Addr().Is6() &&
		ap4.Addr().String() == "1.2.3.4" && ap4.Port() == 5678
}

func testNetipCompare() bool {
	a, _ := netip.ParseAddr("1.2.3.4")
	b, _ := netip.ParseAddr("1.2.3.5")
	c, _ := netip.ParseAddr("::1")
	return a.Less(b) && !b.Less(a) && a.Compare(a) == 0 && a.Less(c)
}
