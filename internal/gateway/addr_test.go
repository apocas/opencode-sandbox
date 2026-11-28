package gateway

import (
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34": true, "8.8.8.8": true, "1.1.1.1": true, "140.82.121.4": true,
		"2606:4700:4700::1111": true, "2a00:1450:4001:81b::200e": true,
		// This machine, private and shared networks.
		"127.0.0.1": false, "127.8.9.1": false, "0.0.0.0": false, "10.0.0.1": false, "172.16.5.4": false,
		"172.31.255.255": false, "192.168.1.1": false, "100.64.0.1": false, "::1": false, "::": false,
		"fc00::1": false, "fd12:3456::1": false,
		// Where clouds keep metadata, and other link-local addresses.
		"169.254.169.254": false, "169.254.0.1": false, "fe80::1": false, "fe80::1%eth0": false,
		// Around the private ranges, the internet goes on.
		"172.15.255.255": true, "172.32.0.0": true, "192.167.255.255": true, "100.63.255.255": true, "11.0.0.1": true,
		// An IPv4 address in IPv6 clothes is that address.
		"::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false, "::ffff:8.8.8.8": true,
		"64:ff9b::7f00:1": false, "64:ff9b::a00:1": false, "64:ff9b::808:808": true, "64:ff9b:1::1": false,
		"2002:7f00:1::1": false, "2001::1": false,
		// Multicast, and ranges kept for documentation and tests.
		"224.0.0.1": false, "239.255.255.250": false, "255.255.255.255": false, "240.0.0.1": false, "ff02::1": false,
		"192.0.2.1": false, "198.51.100.1": false, "203.0.113.1": false, "198.18.0.1": false, "2001:db8::1": false,
		// IPv6 outside the range hosts on the internet have.
		"4000::1": false, "e000::1": false, "100::1": false,
	} {
		if got := Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: public %v, want %v", addr, got, want)
		}
	}
	if Public(netip.Addr{}) {
		t.Error("no address at all is public")
	}
}

func TestGuard(t *testing.T) {
	only := func(a netip.Addr, port int) bool { return a.String() == "10.0.0.5" && port == 5432 }
	g := guard(only)
	for address, ok := range map[string]bool{
		"93.184.216.34:443": true, "[2606:4700:4700::1111]:443": true,
		"127.0.0.1:8080": false, "[::1]:8080": false, "169.254.169.254:80": false, "192.168.1.1:22": false,
		"[::ffff:127.0.0.1]:80": false, "[fe80::1%lo0]:80": false,
		"10.0.0.5:5432": true, "10.0.0.5:22": false, "10.0.0.6:5432": false,
		"not an address": false,
	} {
		err := g("tcp", address, nil)
		if (err == nil) != ok {
			t.Errorf("%s: %v, want allowed %v", address, err, ok)
		}
		var inner ErrInner
		if err != nil && address != "not an address" && !errors.As(err, &inner) {
			t.Errorf("%s: %v isn't an ErrInner", address, err)
		}
	}
	if err := guard(nil)("tcp", "127.0.0.1:80", nil); err == nil {
		t.Error("without rules, this machine was reached")
	}
	// An address of this machine's is this machine, whatever its number says.
	addrs, _ := net.InterfaceAddrs()
	for _, ia := range addrs {
		ipn, isNet := ia.(*net.IPNet)
		if !isNet {
			continue
		}
		a, _ := netip.AddrFromSlice(ipn.IP)
		if !own(a) {
			t.Errorf("%s is this machine's, and isn't taken for it", a)
		}
		if err := guard(nil)("tcp", netip.AddrPortFrom(a.Unmap().WithZone(""), 443).String(), nil); err == nil {
			t.Errorf("%s, an address of this machine's, was reached", a)
		}
	}
	if own(netip.MustParseAddr("93.184.216.34")) {
		t.Error("an address elsewhere was taken for this machine's")
	}
}
