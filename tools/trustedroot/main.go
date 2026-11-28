// Command trustedroot fetches Sigstore's trusted root (the keys of its
// certificate authority and its logs), checked through Sigstore's own
// update framework (TUF), and prints it: what internal/selfupdate is built
// with, to check a release's build attestation.
//
//	go run ./tools/trustedroot CACHEDIR > internal/selfupdate/trusted_root.json
package main

import (
	"fmt"
	"os"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: trustedroot CACHEDIR")
		os.Exit(2)
	}
	opts := tuf.DefaultOptions()
	opts.CachePath = os.Args[1]
	tr, err := root.FetchTrustedRootWithOptions(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, err := tr.MarshalJSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Stdout.Write(data)
}
