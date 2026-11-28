// Package backend describes what the launcher needs from a container engine.
// The docker CLI and Apple's container CLI implement it.
package backend

import "io"

// Row is one sandbox as an engine lists it.
type Row struct {
	Name, KeyKind, Key, Status, Created, Source, Providers, KeyMode string

	Runtime string // the OCI runtime it was created with ("" before 0.5.0)
	Gateway string // set if its API keys stay on this machine ("" before 0.9.0)
	Network string // "none" if it has no network ("" before 0.10.0: open)
	// Memory ("4g", "512m") and CPUs ("4") it may use; "" where it has no
	// such limit, and before 0.12.0.
	Memory, CPUs string
	Engine       string // the Name of the engine it lives in
	Version      string // the opencode-sandbox that created it ("" before 0.1)
}

// Bind mounts a host path into a container.
type Bind struct {
	Src, Dst string
	ReadOnly bool
}

// Spec describes a new sandbox container. Labels and Env keep their order.
type Spec struct {
	Container string // full container name
	TTY       bool   // allocate a terminal (stdin is always kept open)
	Labels    [][2]string
	Env       [][2]string
	Volumes   []string // destinations of anonymous volumes
	Binds     []Bind
	Tmpfs     []string // e.g. "/tmp:exec,size=1g,mode=1777"
	Network   string
	// NoNetwork: it gets no network at all, only its own loopback; Network
	// is then left aside.
	NoNetwork bool
	Runtime   string // OCI runtime, e.g. "runsc" for gVisor; "" for the default

	Init            bool // a tiny init as PID 1 (signals, reaping)
	ReadOnlyRoot    bool
	DropAllCaps     bool
	NoNewPrivileges bool
	PidsLimit       int
	// MemoryMB (MiB) and CPUs are the most it may use; 0 for the engine's
	// own default. NoSwap: memory over the limit can't go to swap instead.
	MemoryMB, CPUs int
	NoSwap         bool

	Image string
	Cmd   []string
}

// Caps are what an engine can do; the launcher works around what it can't.
type Caps struct {
	// FileBinds: single files can be bind-mounted (else only folders).
	FileBinds bool
	// Attach: a second terminal can attach to an open container, and a
	// terminal can detach with DetachKeys.
	Attach bool
	// NoNewPrivileges and PidsLimit are enforced.
	NoNewPrivileges, PidsLimit bool
	// CopyNeedsBinds: copying into or out of a stopped container needs its
	// bind-mount sources to exist (docker mounts them for the copy).
	CopyNeedsBinds bool
	// VMPerContainer: each container runs in a VM of its own.
	VMPerContainer bool
}

// Engine is a container engine as the launcher uses it. Methods that fail
// after the engine explained why on stderr return *run.ExitError.
type Engine interface {
	// Name is "docker" or "apple".
	Name() string
	Caps() Caps

	// Exists reports whether the container exists.
	Exists(container string) bool
	// State is "running", "exited", "created"…, or "" if unknown.
	State(container string) string
	// Label reads one label of a container ("" if it isn't set).
	Label(container, label string) (string, error)
	// Names lists the value of label on every container that has it.
	Names(label string) ([]string, error)
	// Rows lists sandboxes for --list.
	Rows(label string) ([]Row, error)
	// BindSources lists the host paths a container bind-mounts.
	BindSources(container string) ([]string, error)

	ImageExists(ref string) bool
	// Build builds an image, writing the engine's output to out.
	Build(ref, contextDir string, buildArgs [][2]string, extra []string, out io.Writer) error
	// EnsureNetwork creates a network on which containers can't reach each
	// other, unless it exists.
	EnsureNetwork(name string) error

	Create(s Spec) error
	Remove(container string) error // with its anonymous volumes
	Stop(container string, timeoutSec int) error
	// CopyIn copies src (a file or folder; "dir/." for its contents) into
	// dstDir in the container, which may be stopped.
	CopyIn(container, src, dstDir string, keepOwner bool) error
	// CopyOut copies src from the container into hostDir.
	CopyOut(container, src, hostDir string) error

	// StartArgv starts a stopped container attached to the terminal;
	// AttachArgv attaches to a running one (nil without Caps.Attach). Both
	// detach on DetachKeys where the engine can.
	StartArgv(container, detachKeys string) []string
	AttachArgv(container, detachKeys string) []string
	// ExecArgv runs a command in a running container.
	ExecArgv(container string, tty bool, detachKeys string, cmd ...string) []string

	// Info describes the engine; it fails when the engine can't be reached.
	Info() (Info, error)
}

// Info describes a container engine.
type Info struct {
	OS      string // where it runs containers: "Docker Desktop", "Ubuntu 24.04", "Apple container"…
	Version string
	// SecurityOptions as Docker lists them: "name=seccomp,profile=builtin",
	// "name=rootless", "name=userns"…
	SecurityOptions []string
	// Runtimes are the OCI runtimes the engine knows, by name.
	Runtimes       map[string]Runtime
	DefaultRuntime string
	// CPUs and MemoryMB (MiB) are what the engine has for containers; 0 if
	// unknown.
	CPUs, MemoryMB int
	// NoMemoryLimit, NoSwapLimit, NoCPULimit: it can't hold a container to
	// one (Docker without the cgroup controller for it).
	NoMemoryLimit, NoSwapLimit, NoCPULimit bool
}

// Runtime is an OCI runtime: its program ("runc", "/usr/bin/runsc"…) and the
// arguments the engine passes it.
type Runtime struct {
	Path string
	Args []string
	// Missing: the engine lists it, but its program isn't on this machine
	// (see platform.CheckRuntimes); it can't start a container.
	Missing bool
}
