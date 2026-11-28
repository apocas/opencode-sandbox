package selfupdate

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// A release's builds are attested: GitHub signs, with a key that exists
// only for that run of the release workflow, a statement that the workflow
// built these files (by their SHA-256) from this repository at that tag.
// The signature and the certificate that says whose key it was are recorded
// in a public log (Sigstore's). SHA256SUMS sits next to the builds and says
// only that a download is whole; the attestation says who made it, so a
// build swapped by someone who can change a release's files, and not run
// its workflow, is told apart.

// Attestation is the release's file that holds it.
const Attestation = "attestation.jsonl"

const (
	// issuer is who vouches for a GitHub workflow's identity.
	issuer = "https://token.actions.githubusercontent.com"
	// workflow is the one that builds releases; a tag is added to it.
	workflow = "https://github.com/apocas/opencode-sandbox/.github/workflows/release.yml"
)

// trustedRoot is Sigstore's: the keys of its certificate authority and its
// logs. Fetched with tools/trustedroot, which checks it; verifying needs
// nothing else, and no network.
//
//go:embed trusted_root.json
var trustedRoot []byte

// Attested checks a release's attestation (its Attestation file): that the
// file with that SHA-256 was built by this project's release workflow, run
// for the tag of that version. nil if it was.
func Attested(attestation, sha256 []byte, version string) error {
	return attestedBy(attestation, sha256, workflow+"@refs/tags/v"+version)
}

func attestedBy(attestation, sha256 []byte, signer string) error {
	tr, err := root.NewTrustedRootFromJSON(trustedRoot)
	if err != nil {
		return fmt.Errorf("can't read the trusted root this program was built with: %w", err)
	}
	// The certificate was logged when it was issued, the signature when it
	// was made, and the log's time says the certificate was valid then.
	v, err := verify.NewVerifier(tr, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return err
	}
	id, err := verify.NewShortCertificateIdentity(issuer, "", signer, "")
	if err != nil {
		return err
	}
	policy := verify.NewPolicy(verify.WithArtifactDigest("sha256", sha256), verify.WithCertificateIdentity(id))
	// One attestation after another (a line each, as GitHub writes them);
	// any one that holds will do.
	var last error
	dec := json.NewDecoder(bytes.NewReader(attestation))
	for n := 0; ; n++ {
		var one json.RawMessage
		if err := dec.Decode(&one); err != nil {
			switch {
			case err != io.EOF:
				return fmt.Errorf("it can't be read: %w", err)
			case n == 0:
				return errors.New("it's empty")
			}
			return last
		}
		var b bundle.Bundle
		if err := b.UnmarshalJSON(one); err != nil {
			last = fmt.Errorf("it can't be read: %w", err)
			continue
		}
		if _, err := v.Verify(&b, policy); err != nil {
			last = err
			continue
		}
		return nil
	}
}
