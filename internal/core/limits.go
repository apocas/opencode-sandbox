package core

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/apocas/opencode-sandbox/internal/backend"
	"github.com/apocas/opencode-sandbox/internal/backend/apple"
)

// Limits are the most memory and CPUs a sandbox may use, so that a runaway
// build or a fork of memory inside it can't starve this machine. 0 is no
// limit.
type Limits struct {
	MemoryMB int // MiB
	CPUs     int
}

// MemoryEnv ("8g", "512m") and CPUsEnv ("6") set the limits of new
// sandboxes; `new --memory` and `--cpus` set one sandbox's.
const (
	MemoryEnv = "OPENCODE_SANDBOX_MEMORY"
	CPUsEnv   = "OPENCODE_SANDBOX_CPUS"
)

// What a sandbox gets when nothing says otherwise, and the least memory
// opencode starts in.
const (
	defaultMemoryMB = 4096
	defaultCPUs     = 4
	minMemoryMB     = 512
)

var memoryRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([mg])(?:i?b)?$`)

// ParseMemory reads an amount of memory, "8g" or "512m", as MiB.
func ParseMemory(s string) (int, error) {
	m := memoryRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return 0, fmt.Errorf("%q isn't an amount of memory; say it like 8g or 512m", s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil || n > 1<<20 {
		return 0, fmt.Errorf("%q isn't an amount of memory; say it like 8g or 512m", s)
	}
	if m[2] == "g" {
		n *= 1024
	}
	return int(n), nil
}

// MemoryArg writes an amount of memory the way ParseMemory reads it: "4g",
// "512m".
func MemoryArg(mb int) string {
	if mb%1024 == 0 {
		return strconv.Itoa(mb/1024) + "g"
	}
	return strconv.Itoa(mb) + "m"
}

// Memory says an amount of memory: "4 GB", "1.5 GB", "512 MB".
func Memory(mb int) string {
	if mb < 1024 {
		return strconv.Itoa(mb) + " MB"
	}
	return strings.TrimSuffix(strconv.FormatFloat(float64(mb)/1024, 'f', 1, 64), ".0") + " GB"
}

// String says the limits: "4 GB of memory, 4 CPUs"; "no memory or CPU
// limit" where there are none.
func (l Limits) String() string {
	cpus := strconv.Itoa(l.CPUs) + " CPUs"
	if l.CPUs == 1 {
		cpus = "1 CPU"
	}
	switch {
	case l.MemoryMB == 0 && l.CPUs == 0:
		return "no memory or CPU limit"
	case l.MemoryMB == 0:
		return cpus + ", no memory limit"
	case l.CPUs == 0:
		return Memory(l.MemoryMB) + " of memory, no CPU limit"
	}
	return Memory(l.MemoryMB) + " of memory, " + cpus
}

// engineName names an engine in a sentence.
func engineName(e string) string {
	if e == "apple" {
		return "Apple container"
	}
	return "Docker"
}

// limitsFor decides the limits of a new sandbox in an engine: the memory
// and CPUs asked for ("" leaves each to its variable, then to 4 GB and 4
// CPUs), within what the engine has and can hold a container to. The notes
// say where a sandbox gets less than was asked for.
func limitsFor(engine string, info backend.Info, memory, cpus string) (l Limits, notes []string, err error) {
	name := engineName(engine)

	by, asked := "--memory", true
	if memory == "" {
		memory, by = os.Getenv(MemoryEnv), MemoryEnv
	}
	l.MemoryMB = defaultMemoryMB
	if memory == "" {
		asked = false
	} else if l.MemoryMB, err = ParseMemory(memory); err != nil {
		return Limits{}, nil, fmt.Errorf("%s: %w", by, err)
	} else if l.MemoryMB < minMemoryMB {
		return Limits{}, nil, fmt.Errorf("%s: opencode needs at least %s, and %s is less", by, MemoryArg(minMemoryMB), memory)
	}
	switch {
	case info.NoMemoryLimit:
		if asked {
			notes = append(notes, fmt.Sprintf("%s here can't limit a container's memory, so this sandbox has no memory limit (%s is left aside)", name, by))
		}
		l.MemoryMB = 0
	case info.MemoryMB > 0 && l.MemoryMB > info.MemoryMB:
		if asked {
			notes = append(notes, fmt.Sprintf("%s here has %s of memory, so that is this sandbox's limit, not %s", name, Memory(info.MemoryMB), Memory(l.MemoryMB)))
		}
		l.MemoryMB = info.MemoryMB
	}

	by, asked = "--cpus", true
	if cpus == "" {
		cpus, by = os.Getenv(CPUsEnv), CPUsEnv
	}
	l.CPUs = defaultCPUs
	if cpus == "" {
		asked = false
	} else if l.CPUs, err = strconv.Atoi(strings.TrimSpace(cpus)); err != nil || l.CPUs < 1 || l.CPUs > 1<<16 {
		return Limits{}, nil, fmt.Errorf("%s must be a whole number of CPUs, 1 or more, not %q", by, cpus)
	}
	switch {
	case info.NoCPULimit:
		if asked {
			notes = append(notes, fmt.Sprintf("%s here can't limit a container's CPUs, so this sandbox has no CPU limit (%s is left aside)", name, by))
		}
		l.CPUs = 0
	case info.CPUs > 0 && l.CPUs > info.CPUs:
		if asked {
			notes = append(notes, fmt.Sprintf("%s here has %d CPUs, so that is this sandbox's limit, not %d", name, info.CPUs, l.CPUs))
		}
		l.CPUs = info.CPUs
	}
	return l, notes, nil
}

// info describes engine e: what Docker said when it was found, or what
// Apple container says now.
func (a *Core) info(e backend.Engine) backend.Info {
	if e.Name() == "docker" {
		return a.dockerInfo
	}
	info, _ := e.Info()
	return info
}

// CheckLimits says why a new sandbox can't have that memory ("8g") and
// those CPUs ("6"); "" for each leaves it to the environment, then to the
// default. nil if it can.
func CheckLimits(memory, cpus string) error {
	_, _, err := limitsFor("docker", backend.Info{}, memory, cpus)
	return err
}

// LimitsFor is what a new sandbox may use in that engine and on that
// runtime (as CreateOptions takes them) when nothing more is asked.
func (a *Core) LimitsFor(engine, rt string) (Limits, error) {
	e, _, err := a.place(engine, rt)
	if err != nil {
		return Limits{}, err
	}
	l, _, err := limitsFor(e.Name(), a.info(e), "", "")
	return l, err
}

// limitsOf reads a sandbox's limits from its labels.
func limitsOf(r backend.Row) Limits {
	if r.Engine == "apple" && r.Memory == "" && r.CPUs == "" {
		// From before 0.12.0: every VM had the same.
		return Limits{MemoryMB: apple.MemoryMB, CPUs: apple.CPUs}
	}
	var l Limits
	if r.Memory != "" {
		l.MemoryMB, _ = ParseMemory(r.Memory)
	}
	l.CPUs, _ = strconv.Atoi(r.CPUs)
	return l
}
