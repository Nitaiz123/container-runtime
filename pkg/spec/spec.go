// Package spec defines OCI Runtime Specification types.
//
// The OCI Runtime Specification (https://github.com/opencontainers/runtime-spec)
// defines how a container runtime should create and run containers.
// A container is described by a JSON config.json file containing:
//   - Process: the command to run, environment variables, working directory
//   - Root: the root filesystem path
//   - Mounts: additional filesystem mounts (proc, sys, dev, etc.)
//   - Linux: Linux-specific settings (namespaces, cgroups, capabilities)
//
// This package implements a subset of the OCI spec sufficient for running
// basic containers with full namespace and cgroup isolation.
package spec

// Spec is the top-level OCI runtime specification.
type Spec struct {
	// Version of the OCI Runtime Specification
	Version string `json:"ociVersion"`
	// Process configures the container process
	Process *Process `json:"process,omitempty"`
	// Root configures the container's root filesystem
	Root *Root `json:"root,omitempty"`
	// Hostname sets the container hostname
	Hostname string `json:"hostname,omitempty"`
	// Mounts configures additional mounts
	Mounts []Mount `json:"mounts,omitempty"`
	// Linux contains Linux-specific configuration
	Linux *Linux `json:"linux,omitempty"`
}

// Process contains information to start a specific application inside the container.
type Process struct {
	// Terminal creates an interactive terminal for the container process
	Terminal bool `json:"terminal,omitempty"`
	// User specifies user information for the process
	User User `json:"user"`
	// Args specifies the binary and arguments for the application to execute
	Args []string `json:"args"`
	// Env populates the process environment for the container
	Env []string `json:"env,omitempty"`
	// Cwd is the working directory that will be set for the executable
	Cwd string `json:"cwd"`
	// Capabilities are Linux capabilities that are kept for the process
	Capabilities *LinuxCapabilities `json:"capabilities,omitempty"`
	// Rlimits specifies rlimit options to apply to the process
	Rlimits []POSIXRlimit `json:"rlimits,omitempty"`
	// NoNewPrivileges controls whether additional privileges could be gained
	NoNewPrivileges bool `json:"noNewPrivileges,omitempty"`
}

// User specifies specific user (and optionally group) information for the container process.
type User struct {
	UID            uint32 `json:"uid"`
	GID            uint32 `json:"gid"`
	AdditionalGids []uint32 `json:"additionalGids,omitempty"`
}

// Root information for the container's filesystem.
type Root struct {
	// Path is the absolute path to the container's root filesystem
	Path string `json:"path"`
	// Readonly makes the root filesystem readonly inside the container
	Readonly bool `json:"readonly,omitempty"`
}

// Mount specifies a mount for a container.
type Mount struct {
	// Destination is the absolute path where the mount will be placed in the container
	Destination string `json:"destination"`
	// Type specifies the mount type (e.g., "bind", "proc", "tmpfs")
	Type string `json:"type,omitempty"`
	// Source specifies the source path of the mount
	Source string `json:"source,omitempty"`
	// Options are fstab style mount options
	Options []string `json:"options,omitempty"`
}

// Linux contains platform-specific configuration for Linux based containers.
type Linux struct {
	// Namespaces contains the namespaces that are created and/or joined by the container
	Namespaces []LinuxNamespace `json:"namespaces,omitempty"`
	// Devices are a list of device nodes supplied for the container
	Devices []LinuxDevice `json:"devices,omitempty"`
	// CgroupsPath specifies the path to cgroups that are created and/or joined by the container
	CgroupsPath string `json:"cgroupsPath,omitempty"`
	// Resources contain cgroup information for handling resource constraints
	Resources *LinuxResources `json:"resources,omitempty"`
	// Seccomp specifies the seccomp security settings for the container
	Seccomp *LinuxSeccomp `json:"seccomp,omitempty"`
	// RootfsPropagation is the rootfs mount propagation mode for the container
	RootfsPropagation string `json:"rootfsPropagation,omitempty"`
	// MaskedPaths masks over the provided paths inside the container
	MaskedPaths []string `json:"maskedPaths,omitempty"`
	// ReadonlyPaths sets the provided paths as RO inside the container
	ReadonlyPaths []string `json:"readonlyPaths,omitempty"`
}

// LinuxNamespaceType is one of the Linux namespaces
type LinuxNamespaceType string

const (
	PIDNamespace     LinuxNamespaceType = "pid"
	NetworkNamespace LinuxNamespaceType = "network"
	MountNamespace   LinuxNamespaceType = "mount"
	IPCNamespace     LinuxNamespaceType = "ipc"
	UTSNamespace     LinuxNamespaceType = "uts"
	UserNamespace    LinuxNamespaceType = "user"
	CgroupNamespace  LinuxNamespaceType = "cgroup"
)

// LinuxNamespace is the configuration for a Linux namespace
type LinuxNamespace struct {
	// Type is the type of namespace
	Type LinuxNamespaceType `json:"type"`
	// Path is a path to an existing namespace persisted on disk that can be joined
	// and is of the same type
	Path string `json:"path,omitempty"`
}

// LinuxDevice represents the mknod information for a Linux special device file
type LinuxDevice struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Major    int64  `json:"major"`
	Minor    int64  `json:"minor"`
	FileMode *uint32 `json:"fileMode,omitempty"`
	UID      *uint32 `json:"uid,omitempty"`
	GID      *uint32 `json:"gid,omitempty"`
}

// LinuxResources has container runtime resource constraints
type LinuxResources struct {
	// Memory restriction configuration
	Memory *LinuxMemory `json:"memory,omitempty"`
	// CPU resource restriction configuration
	CPU *LinuxCPU `json:"cpu,omitempty"`
	// Pids limit the number of processes/threads
	Pids *LinuxPids `json:"pids,omitempty"`
}

// LinuxMemory for Linux cgroup 'memory' resource management
type LinuxMemory struct {
	// Memory limit in bytes
	Limit *int64 `json:"limit,omitempty"`
	// Memory reservation or soft_limit in bytes
	Reservation *int64 `json:"reservation,omitempty"`
	// Total memory limit (memory + swap) in bytes
	Swap *int64 `json:"swap,omitempty"`
	// Kernel memory limit in bytes
	Kernel *int64 `json:"kernel,omitempty"`
}

// LinuxCPU for Linux cgroup 'cpu' resource management
type LinuxCPU struct {
	// CPU shares (relative weight vs. other cgroups with cpu shares)
	Shares *uint64 `json:"shares,omitempty"`
	// CPU hardcap limit (in usecs). Allowed cpu time in a given period
	Quota *int64 `json:"quota,omitempty"`
	// CPU period to be used for hardcapping (in usecs)
	Period *uint64 `json:"period,omitempty"`
	// CPUs to use within the cpuset. Default is to use any CPU available
	Cpus string `json:"cpus,omitempty"`
	// List of memory nodes in the cpuset. Default is to use any available memory node
	Mems string `json:"mems,omitempty"`
}

// LinuxPids for Linux cgroup 'pids' resource management
type LinuxPids struct {
	// Maximum number of PIDs
	Limit int64 `json:"limit"`
}

// LinuxCapabilities specifies the list of allowed capabilities that are kept for a process
type LinuxCapabilities struct {
	Bounding    []string `json:"bounding,omitempty"`
	Effective   []string `json:"effective,omitempty"`
	Inheritable []string `json:"inheritable,omitempty"`
	Permitted   []string `json:"permitted,omitempty"`
	Ambient     []string `json:"ambient,omitempty"`
}

// POSIXRlimit type and restrictions
type POSIXRlimit struct {
	Type string `json:"type"`
	Hard uint64 `json:"hard"`
	Soft uint64 `json:"soft"`
}

// LinuxSeccomp provides Seccomp security settings for the container
type LinuxSeccomp struct {
	DefaultAction string          `json:"defaultAction"`
	Architectures []string        `json:"architectures,omitempty"`
	Syscalls      []LinuxSyscall  `json:"syscalls,omitempty"`
}

// LinuxSyscall is used to match a syscall in Seccomp
type LinuxSyscall struct {
	Names  []string `json:"names"`
	Action string   `json:"action"`
}

// State holds information about the runtime state of a container.
type State struct {
	// Version is the OCI version for the state
	Version string `json:"ociVersion"`
	// ID is the container ID
	ID string `json:"id"`
	// Status is the runtime status of the container
	Status ContainerStatus `json:"status"`
	// PID is the ID of the container process
	PID int `json:"pid,omitempty"`
	// Bundle is the path to the container's bundle directory
	Bundle string `json:"bundle"`
	// Annotations are key values associated with the container
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ContainerStatus represents the lifecycle state of a container
type ContainerStatus string

const (
	StateCreating ContainerStatus = "creating"
	StateCreated  ContainerStatus = "created"
	StateRunning  ContainerStatus = "running"
	StateStopped  ContainerStatus = "stopped"
)
