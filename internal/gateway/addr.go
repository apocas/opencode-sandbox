package gateway

import (
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// inner are the addresses that aren't hosts on the internet: this machine,
// private and shared networks, link-local ones (where clouds keep their
// metadata), multicast, and ranges kept for documentation, tests and
// protocols. A sandbox with a filtered network reaches one only by a rule
// that names its address.
var inner = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16",
		"3fff::/20", "5f00::/16", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var (
	global = netip.MustParsePrefix("2000::/3")     // where IPv6 hosts on the internet are
	nat64  = netip.MustParsePrefix("64:ff9b::/96") // an IPv4 address, reached from IPv6
)

// Public says whether an address is a host on the internet, as far as its
// number tells.
func Public(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	if !a.IsValid() {
		return false
	}
	if a.Is6() && nat64.Contains(a) {
		// The IPv4 address it stands for is what counts.
		b := a.As16()
		a = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	if a.Is6() && !global.Contains(a) {
		return false
	}
	for _, p := range inner {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// own says whether an address is one of this machine's.
func own(a netip.Addr) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	a = a.Unmap().WithZone("")
	for _, ia := range addrs {
		if ipn, ok := ia.(*net.IPNet); ok {
			if b, ok := netip.AddrFromSlice(ipn.IP); ok && b.Unmap() == a {
				return true
			}
		}
	}
	return false
}

// ErrInner is why a connection to an address that isn't on the internet was
// refused.
type ErrInner struct{ Addr netip.Addr }

func (e ErrInner) Error() string {
	return fmt.Sprintf("%s isn't a host on the internet (it's this machine, or on your network)", e.Addr)
}

// guard is a net.Dialer's Control: it lets a connection be made to an
// address on the internet, and to another only if allowed says so. It sees
// the address a name led to, so a name can't lead somewhere it shouldn't.
func guard(allowed func(netip.Addr, int) bool) func(network, address string, _ syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("can't tell where %s is", address)
		}
		a := ap.Addr().Unmap().WithZone("")
		if Public(a) && !own(a) {
			return nil
		}
		if allowed != nil && allowed(a, int(ap.Port())) {
			return nil
		}
		return ErrInner{Addr: a}
	}
}
