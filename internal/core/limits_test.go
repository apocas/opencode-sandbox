package core

import (
	"strings"
	"testing"

	"github.com/apocas/opencode-sandbox/internal/backend"
)

func TestParseMemory(t *testing.T) {
	for in, want := range map[string]int{
		"4g": 4096, "8G": 8192, "512m": 512, "512MB": 512, "1.5g": 1536, " 2gb ": 2048, "2GiB": 2048, "768 m": 768,
	} {
		if got, err := ParseMemory(in); err != nil || got != want {
			t.Errorf("%q: %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "4", "four", "4t", "-4g", "4g4", "g", "9999999g"} {
		if got, err := ParseMemory(in); err == nil {
			t.Errorf("%q was read as %d", in, got)
		}
	}
	// What's written is read back the same.
	for _, mb := range []int{512, 1536, 4096, 65536} {
		if got, err := ParseMemory(MemoryArg(mb)); err != nil || got != mb {
			t.Errorf("%d MiB written as %q, read as %d, %v", mb, MemoryArg(mb), got, err)
		}
	}
	for mb, want := range map[int]string{512: "512 MB", 1536: "1.5 GB", 4096: "4 GB", 7933: "7.7 GB"} {
		if got := Memory(mb); got != want {
			t.Errorf("%d MiB said as %q, want %q", mb, got, want)
		}
	}
}

func TestLimitsString(t *testing.T) {
	for l, want := range map[Limits]string{
		{4096, 4}: "4 GB of memory, 4 CPUs", {512, 1}: "512 MB of memory, 1 CPU", {}: "no memory or CPU limit",
		{0, 2}: "2 CPUs, no memory limit", {2048, 0}: "2 GB of memory, no CPU limit",
	} {
		if got := l.String(); got != want {
			t.Errorf("%+v: %q, want %q", l, got, want)
		}
	}
}

func TestLimitsFor(t *testing.T) {
	big := backend.Info{CPUs: 12, MemoryMB: 32768}
	small := backend.Info{CPUs: 2, MemoryMB: 1987}
	for _, c := range []struct {
		name           string
		info           backend.Info
		memory, cpus   string // asked for
		envMem, envCPU string
		want           Limits
		notes          []string // each a part of one note, in order
		err            string
	}{
		{name: "the defaults", info: big, want: Limits{4096, 4}},
		{name: "the defaults, where nothing is known of the engine", want: Limits{4096, 4}},
		{name: "the defaults on a small engine: all it has, and nothing to say", info: small, want: Limits{1987, 2}},
		{name: "asked for", info: big, memory: "8g", cpus: "6", want: Limits{8192, 6}},
		{name: "the environment", info: big, envMem: "2g", envCPU: "1", want: Limits{2048, 1}},
		{name: "asked for wins over the environment", info: big, memory: "512m", cpus: "3", envMem: "2g", envCPU: "1", want: Limits{512, 3}},
		{name: "more than the engine has", info: small, memory: "16g", cpus: "8", want: Limits{1987, 2},
			notes: []string{"Docker here has 1.9 GB of memory, so that is this sandbox's limit, not 16 GB", "Docker here has 2 CPUs, so that is this sandbox's limit, not 8"}},
		{name: "more than the engine has, from the environment", info: small, envMem: "16g", envCPU: "8", want: Limits{1987, 2},
			notes: []string{"1.9 GB of memory", "has 2 CPUs"}},
		{name: "an engine that can't limit", info: backend.Info{CPUs: 4, MemoryMB: 8000, NoMemoryLimit: true, NoCPULimit: true}, want: Limits{}},
		{name: "an engine that can't limit, asked to", info: backend.Info{NoMemoryLimit: true, NoCPULimit: true}, memory: "2g", cpus: "2", want: Limits{},
			notes: []string{"can't limit a container's memory", "can't limit a container's CPUs"}},
		{name: "an engine that can limit only memory", info: backend.Info{NoCPULimit: true}, want: Limits{4096, 0}},
		{name: "no unit", memory: "8", err: `--memory: "8" isn't an amount of memory; say it like 8g or 512m`},
		{name: "too little", memory: "100m", err: "--memory: opencode needs at least 512m, and 100m is less"},
		{name: "too little, from the environment", envMem: "0.2g", err: "OPENCODE_SANDBOX_MEMORY: opencode needs at least 512m"},
		{name: "half a CPU", cpus: "0.5", err: `--cpus must be a whole number of CPUs, 1 or more, not "0.5"`},
		{name: "no CPUs", cpus: "0", err: "--cpus must be a whole number"},
		{name: "CPUs that aren't a number, from the environment", envCPU: "all", err: `OPENCODE_SANDBOX_CPUS must be a whole number of CPUs, 1 or more, not "all"`},
	} {
		t.Setenv(MemoryEnv, c.envMem)
		t.Setenv(CPUsEnv, c.envCPU)
		got, notes, err := limitsFor("docker", c.info, c.memory, c.cpus)
		switch {
		case c.err != "":
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %v, want %q", c.name, err, c.err)
			}
			continue
		case err != nil:
			t.Errorf("%s: %v", c.name, err)
			continue
		case got != c.want:
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
		if len(notes) != len(c.notes) {
			t.Errorf("%s: notes %q, want %d", c.name, notes, len(c.notes))
			continue
		}
		for i, n := range notes {
			if !strings.Contains(n, c.notes[i]) {
				t.Errorf("%s: note %q, want %q", c.name, n, c.notes[i])
			}
		}
	}
	t.Setenv(MemoryEnv, "")
	t.Setenv(CPUsEnv, "")
	if _, notes, _ := limitsFor("apple", small, "16g", ""); len(notes) != 1 || !strings.HasPrefix(notes[0], "Apple container here has") {
		t.Errorf("in Apple container: %q", notes)
	}
	if err := CheckLimits("8g", "6"); err != nil {
		t.Errorf("CheckLimits: %v", err)
	}
	if err := CheckLimits("lots", ""); err == nil {
		t.Error("CheckLimits took lots")
	}
}

// A sandbox's limits are read from its labels; a VM from before 0.12.0 had
// the same as every other.
func TestLimitsOf(t *testing.T) {
	for _, c := range []struct {
		r    backend.Row
		want Limits
	}{
		{backend.Row{Engine: "docker", Memory: "8g", CPUs: "6"}, Limits{8192, 6}},
		{backend.Row{Engine: "docker", Memory: "1987m", CPUs: "2"}, Limits{1987, 2}},
		{backend.Row{Engine: "docker"}, Limits{}},
		{backend.Row{Engine: "docker", CPUs: "2"}, Limits{0, 2}},
		{backend.Row{Engine: "apple"}, Limits{4096, 4}},
		{backend.Row{Engine: "apple", Memory: "2g", CPUs: "1"}, Limits{2048, 1}},
	} {
		if got := limitsOf(c.r); got != c.want {
			t.Errorf("%+v: %+v, want %+v", c.r, got, c.want)
		}
	}
}
