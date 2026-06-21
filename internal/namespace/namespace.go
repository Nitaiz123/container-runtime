// Package namespace handles Linux namespace creation and configuration.
//
// Linux namespaces are the foundation of container isolation. Each namespace
// type isolates a different aspect of the system:
//
//   - PID namespace:     Processes see only processes within the namespace
//   - Mount namespace:   Filesystem mounts are isolated
//   - UTS namespace:     Hostname and NIS domain name are isolated
//   - IPC namespace:     System V IPC and POSIX message queues are isolated
//   - Network namespace: Network devices, stacks, ports are isolated
//   - User namespace:    User and group IDs are isolated (unprivileged containers)
//   - Cgroup namespace:  Cgroup root is isolated
//
// When a container is created, the runtime calls clone(2) with the appropriate
// CLONE_NEW* flags to create new namespaces for the container process.
//
// References:
//   - namespaces(7) man page
//   - unshare(1), nsenter(1) utilities
//   - Linux kernel source: kernel/nsproxy.c
package namespace

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Nitaiz123/container-runtime/pkg/spec"
)

// NamespaceFlags maps OCI namespace types to Linux clone flags.
var NamespaceFlags = map[spec.LinuxNamespaceType]uintptr{
	spec.PIDNamespace:     syscall.CLONE_NEWPID,
	spec.MountNamespace:   syscall.CLONE_NEWNS,
	spec.UTSNamespace:     syscall.CLONE_NEWUTS,
	spec.IPCNamespace:     syscall.CLONE_NEWIPC,
	spec.NetworkNamespace: syscall.CLONE_NEWNET,
	spec.UserNamespace:    syscall.CLONE_NEWUSER,
	// CLONE_NEWCGROUP is not in syscall package for older Go versions
	// spec.CgroupNamespace: 0x02000000,
}

// Config holds namespace configuration for a container.
type Config struct {
	// Namespaces to create for this container
	Namespaces []spec.LinuxNamespace
	// UIDMappings for user namespace (host UID -> container UID)
	UIDMappings []IDMapping
	// GIDMappings for user namespace (host GID -> container GID)
	GIDMappings []IDMapping
}

// IDMapping represents a UID/GID mapping for user namespaces.
type IDMapping struct {
	// ContainerID is the starting UID/GID inside the container
	ContainerID uint32
	// HostID is the starting UID/GID on the host
	HostID uint32
	// Size is the number of IDs to map
	Size uint32
}

// CloneFlags computes the clone(2) flags needed to create the requested namespaces.
func CloneFlags(namespaces []spec.LinuxNamespace) uintptr {
	var flags uintptr
	for _, ns := range namespaces {
		if ns.Path == "" {
			// Create a new namespace (no path = create new)
			if flag, ok := NamespaceFlags[ns.Type]; ok {
				flags |= flag
			}
		}
		// If ns.Path is set, we'll join an existing namespace via nsenter
	}
	return flags
}

// SetupUserNamespace writes UID/GID mappings for a user namespace.
// This must be called from the parent process after the child is created
// but before the child calls execve.
func SetupUserNamespace(pid int, uidMappings, gidMappings []IDMapping) error {
	// Write "deny" to setgroups before writing gid_map (required by kernel)
	setgroupsPath := fmt.Sprintf("/proc/%d/setgroups", pid)
	if err := os.WriteFile(setgroupsPath, []byte("deny"), 0); err != nil {
		return fmt.Errorf("writing setgroups: %w", err)
	}

	// Write UID mappings
	if len(uidMappings) > 0 {
		if err := writeIDMapping(pid, "uid_map", uidMappings); err != nil {
			return fmt.Errorf("writing uid_map: %w", err)
		}
	}

	// Write GID mappings
	if len(gidMappings) > 0 {
		if err := writeIDMapping(pid, "gid_map", gidMappings); err != nil {
			return fmt.Errorf("writing gid_map: %w", err)
		}
	}

	return nil
}

// writeIDMapping writes an ID mapping to /proc/<pid>/{uid_map,gid_map}.
func writeIDMapping(pid int, filename string, mappings []IDMapping) error {
	path := fmt.Sprintf("/proc/%d/%s", pid, filename)
	var content string
	for _, m := range mappings {
		content += fmt.Sprintf("%d %d %d\n", m.ContainerID, m.HostID, m.Size)
	}
	return os.WriteFile(path, []byte(content), 0)
}

// JoinNamespace joins an existing namespace by opening its fd and calling setns(2).
func JoinNamespace(nsPath string, nsType spec.LinuxNamespaceType) error {
	f, err := os.Open(nsPath)
	if err != nil {
		return fmt.Errorf("opening namespace %s: %w", nsPath, err)
	}
	defer f.Close()

	nsFlag, ok := NamespaceFlags[nsType]
	if !ok {
		return fmt.Errorf("unknown namespace type: %s", nsType)
	}

	if err := setns(int(f.Fd()), nsFlag); err != nil {
		return fmt.Errorf("setns(%s): %w", nsPath, err)
	}
	return nil
}

// setns wraps the setns(2) syscall.
// SYS_SETNS = 308 on Linux amd64
const sysSetns = 308

func setns(fd int, nstype uintptr) error {
	_, _, errno := syscall.Syscall(sysSetns, uintptr(fd), nstype, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// GetNamespacePath returns the path to a process's namespace file.
func GetNamespacePath(pid int, nsType spec.LinuxNamespaceType) string {
	nsName := map[spec.LinuxNamespaceType]string{
		spec.PIDNamespace:     "pid",
		spec.MountNamespace:   "mnt",
		spec.UTSNamespace:     "uts",
		spec.IPCNamespace:     "ipc",
		spec.NetworkNamespace: "net",
		spec.UserNamespace:    "user",
		spec.CgroupNamespace:  "cgroup",
	}
	name, ok := nsName[nsType]
	if !ok {
		name = string(nsType)
	}
	return filepath.Join("/proc", fmt.Sprintf("%d", pid), "ns", name)
}

// NamespaceExists checks if a namespace path exists and is accessible.
func NamespaceExists(nsPath string) bool {
	_, err := os.Stat(nsPath)
	return err == nil
}
