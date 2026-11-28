package selfupdate

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The attestation of a real release, 0.11.1, and the SHA-256 of its builds
// (its SHA256SUMS).
var real = map[string]string{
	"darwin_amd64": "2471ef763d06d47bf5a816dbf8da4e898f8371484dec42923a5d024b44d6fc09",
	"darwin_arm64": "efbfdb0d33ba60898b6b5f163f997238e8eba76b0dfd9c660538baae31202f46",
	"linux_amd64":  "7c216e5b81a8c5df0ffd307dedf0001e09998c54040f2e77ce4e90f36e1b9cbf",
	"linux_arm64":  "2f6855bceb1cce05e1d708d15a0b3d5e97313ca03dba6886494fd67a944ab9f4",
}

func digest(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAttested(t *testing.T) {
	att, err := os.ReadFile("testdata/v0.11.1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Every build of the release is the release workflow's, at its tag.
	for build, sum := range real {
		if err := Attested(att, digest(t, sum), "0.11.1"); err != nil {
			t.Errorf("%s: %v", build, err)
		}
	}
	sum := digest(t, real["linux_amd64"])
	// A build that was changed isn't.
	tampered := append([]byte{}, sum...)
	tampered[0] ^= 1
	if err := Attested(att, tampered, "0.11.1"); err == nil {
		t.Error("a build that was changed passed")
	}
	// Nor is the build of another release, passed off as this one: the
	// workflow ran for another tag.
	for _, v := range []string{"0.11.0", "0.11.2", "0.11.10", "9.9.9"} {
		if err := Attested(att, sum, v); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Errorf("as release %s: %v", v, err)
		}
	}
	// Nor one built by someone else's workflow, or another one of ours.
	for _, signer := range []string{
		"https://github.com/someone-else/opencode-sandbox/.github/workflows/release.yml@refs/tags/v0.11.1",
		"https://github.com/apocas/opencode-sandbox/.github/workflows/ci.yml@refs/tags/v0.11.1",
		"https://github.com/apocas/opencode-sandbox/.github/workflows/release.yml@refs/heads/master",
	} {
		if err := attestedBy(att, sum, signer); err == nil {
			t.Errorf("signed by %s: passed", signer)
		}
	}
	// What isn't an attestation at all.
	changed := []byte(strings.Replace(string(att), `"payload":"ey`, `"payload":"ez`, 1))
	if string(changed) == string(att) {
		t.Fatal("the test's attestation has no payload to change")
	}
	for what, data := range map[string][]byte{
		"nothing": nil, "an empty line": []byte("\n\n"), "not JSON": []byte("hello\n"), "JSON of another kind": []byte(`{"a":1}` + "\n"),
		"a changed statement": changed, "cut short": att[:len(att)/2],
	} {
		if err := Attested(data, sum, "0.11.1"); err == nil {
			t.Errorf("%s passed", what)
		}
	}
	// Other lines don't get in the way of the one that holds, nor does how
	// it's laid out.
	if err := Attested(append([]byte("{}\n\n"), att...), sum, "0.11.1"); err != nil {
		t.Errorf("with another line before it: %v", err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, bytes.TrimSpace(att), "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := Attested(pretty.Bytes(), sum, "0.11.1"); err != nil {
		t.Errorf("laid out over many lines: %v", err)
	}
}

// An update is refused unless the release's attestation says this project's
// release workflow built the download: with one that says otherwise, one
// that isn't an attestation, or none.
func TestInstallChecksWhoBuiltIt(t *testing.T) {
	refused := func(t *testing.T, u *Updater, want string) {
		t.Helper()
		exe := installed(t)
		r, err := u.Latest(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		err = u.Install(context.Background(), r, exe)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.HasSuffix(err.Error(), "; nothing was changed") {
			t.Errorf("got %v, want %q", err, want)
		}
		untouched(t, exe)
	}
	u := release(t, "0.2.0", script("0.2.0"), script("0.2.0"))
	var asked []string
	u.Attested = func(att, sha []byte, version string) error {
		asked = append(asked, hex.EncodeToString(sha)+" "+version)
		return errors.New("no certificate of the release workflow")
	}
	refused(t, u, "the download isn't a build that this project's release workflow made for 0.2.0, by the release's attestation (no certificate of the release workflow)")
	// It's asked about the download itself, and the release it's for.
	if len(asked) != 1 || asked[0] != sum(script("0.2.0"))+" 0.2.0" {
		t.Errorf("asked about %q", asked)
	}
	// The check that's there when nothing replaces it is the real one.
	u = release(t, "0.2.0", script("0.2.0"), script("0.2.0"))
	u.Attested = nil
	refused(t, u, "by the release's attestation (")
	// A release without an attestation.
	file := "opencode-sandbox_0.2.0_linux_arm64"
	mux := http.NewServeMux()
	mux.HandleFunc("/latest/download/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sum(script("0.2.0"))+"  "+file+"\n")
	})
	mux.HandleFunc("/download/v0.2.0/"+file, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, script("0.2.0")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	refused(t, &Updater{Base: srv.URL, Client: srv.Client(), GOOS: "linux", GOARCH: "arm64", Agent: "opencode-sandbox/0.1.0"},
		"opencode-sandbox 0.2.0 can't be checked: its release has no record of who built it (attestation.jsonl")
}
