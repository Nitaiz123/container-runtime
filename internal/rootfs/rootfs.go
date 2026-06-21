// Package rootfs handles container root filesystem setup.
//
// The container rootfs is set up using pivot_root(2) or chroot(2):
//
//  1. The container's root filesystem (from the OCI bundle) is bind-mounted
//     to a temporary location.
//  2. Essential virtual filesystems (proc, sys, dev) are mounted inside.
//  3. pivot_root(2) changes the root mount for the container process.
//  4. The old root is unmounted and removed.
//
// pivot_root is preferred over chroot because it actually changes the root
// mount point in the mount namespace, making it impossible for the container
// to escape using chroot tricks.
//
// References:
//   - pivot_root(2) man page
//   - chroot(2) man page
//   - OCI Runtime Spec: https://github.com/opencontainers/runtime-spec
package rootfs

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Nitaiz123/container-runtime/pkg/spec"
)

// Setup prepares the container's root filesystem.
// This is called inside the container's mount namespace.
func Setup(rootPath string, mounts []spec.Mount, readonly bool) error {
	// Ensure the rootfs path exists
	if _, err := os.Stat(rootPath); err != nil {
		return fmt.Errorf("rootfs path %s does not exist: %w", rootPath, err)
	}

	// Make the current root mount private to prevent propagation
	if err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("making root private: %w", err)
	}

	// Bind mount the rootfs to itself (required for pivot_root)
	if err := syscall.Mount(rootPath, rootPath, "bind", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind mounting rootfs: %w", err)
	}

	// Apply additional mounts from the OCI spec
	for _, m := range mounts {
		if err := applyMount(rootPath, m); err != nil {
			return fmt.Errorf("applying mount %s: %w", m.Destination, err)
		}
	}

	// Setup default mounts (proc, sys, dev) if not already specified
	if err := setupDefaultMounts(rootPath); err != nil {
		return fmt.Errorf("setting up default mounts: %w", err)
	}

	// Perform pivot_root to change the root filesystem
	if err := pivotRoot(rootPath); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}

	// Make root readonly if requested
	if readonly {
		if err := syscall.Mount("", "/", "", syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
			return fmt.Errorf("making root readonly: %w", err)
		}
	}

	return nil
}

// pivotRoot changes the root filesystem using pivot_root(2).
// After this call, the container process sees newRoot as /.
func pivotRoot(newRoot string) error {
	// Create a directory inside newRoot to hold the old root
	putOld := filepath.Join(newRoot, ".pivot_root")
	if err := os.MkdirAll(putOld, 0700); err != nil {
		return fmt.Errorf("creating pivot_root dir: %w", err)
	}

	// pivot_root(newRoot, putOld) swaps the root mount
	if err := syscall.PivotRoot(newRoot, putOld); err != nil {
		return fmt.Errorf("pivot_root(%s, %s): %w", newRoot, putOld, err)
	}

	// Change to new root
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir to new root: %w", err)
	}

	// Unmount the old root (it's now at /.pivot_root)
	oldRoot := "/.pivot_root"
	if err := syscall.Unmount(oldRoot, syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("unmounting old root: %w", err)
	}

	// Remove the now-empty .pivot_root directory
	if err := os.Remove(oldRoot); err != nil {
		return fmt.Errorf("removing .pivot_root: %w", err)
	}

	return nil
}

// setupDefaultMounts sets up essential virtual filesystems inside the container.
func setupDefaultMounts(rootPath string) error {
	defaultMounts := []spec.Mount{
		{
			Destination: "/proc",
			Type:        "proc",
			Source:      "proc",
			Options:     []string{"nosuid", "noexec", "nodev"},
		},
		{
			Destination: "/sys",
			Type:        "sysfs",
			Source:      "sysfs",
			Options:     []string{"nosuid", "noexec", "nodev", "ro"},
		},
		{
			Destination: "/dev",
			Type:        "tmpfs",
			Source:      "tmpfs",
			Options:     []string{"nosuid", "strictatime", "mode=755", "size=65536k"},
		},
		{
			Destination: "/dev/pts",
			Type:        "devpts",
			Source:      "devpts",
			Options:     []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"},
		},
		{
			Destination: "/dev/shm",
			Type:        "tmpfs",
			Source:      "shm",
			Options:     []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"},
		},
		{
			Destination: "/dev/mqueue",
			Type:        "mqueue",
			Source:      "mqueue",
			Options:     []string{"nosuid", "noexec", "nodev"},
		},
	}

	for _, m := range defaultMounts {
		if err := applyMount(rootPath, m); err != nil {
			// Non-fatal: some mounts may not be available in all environments
			_ = err
		}
	}

	return nil
}

// applyMount applies a single mount inside the container rootfs.
func applyMount(rootPath string, m spec.Mount) error {
	target := filepath.Join(rootPath, m.Destination)

	// Create the mount target if it doesn't exist
	if err := os.MkdirAll(target, 0755); err != nil {
		return fmt.Errorf("creating mount target %s: %w", target, err)
	}

	// Parse mount flags from options
	flags, data := parseMountOptions(m.Options)

	// Determine mount type
	mountType := m.Type
	source := m.Source
	if source == "" {
		source = m.Type
	}

	// Bind mounts
	if containsOption(m.Options, "bind") || m.Type == "bind" {
		flags |= syscall.MS_BIND
		mountType = ""
	}

	if err := syscall.Mount(source, target, mountType, flags, data); err != nil {
		return fmt.Errorf("mount(%s, %s, %s): %w", source, target, mountType, err)
	}

	// Apply remount for bind mounts with additional flags (e.g., ro)
	if flags&syscall.MS_BIND != 0 && flags&syscall.MS_RDONLY != 0 {
		if err := syscall.Mount("", target, "", flags|syscall.MS_REMOUNT, data); err != nil {
			return fmt.Errorf("remount(%s): %w", target, err)
		}
	}

	return nil
}

// parseMountOptions converts fstab-style options to syscall flags and data string.
func parseMountOptions(options []string) (uintptr, string) {
	var flags uintptr
	var dataOptions []string

	optionFlags := map[string]uintptr{
		"ro":          syscall.MS_RDONLY,
		"rw":          0,
		"nosuid":      syscall.MS_NOSUID,
		"nodev":       syscall.MS_NODEV,
		"noexec":      syscall.MS_NOEXEC,
		"sync":        syscall.MS_SYNCHRONOUS,
		"remount":     syscall.MS_REMOUNT,
		"mand":        syscall.MS_MANDLOCK,
		"dirsync":     syscall.MS_DIRSYNC,
		"noatime":     syscall.MS_NOATIME,
		"nodiratime":  syscall.MS_NODIRATIME,
		"bind":        syscall.MS_BIND,
		"rbind":       syscall.MS_BIND | syscall.MS_REC,
		"unbindable":  syscall.MS_UNBINDABLE,
		"runbindable": syscall.MS_UNBINDABLE | syscall.MS_REC,
		"private":     syscall.MS_PRIVATE,
		"rprivate":    syscall.MS_PRIVATE | syscall.MS_REC,
		"shared":      syscall.MS_SHARED,
		"rshared":     syscall.MS_SHARED | syscall.MS_REC,
		"slave":       syscall.MS_SLAVE,
		"rslave":      syscall.MS_SLAVE | syscall.MS_REC,
		"relatime":    syscall.MS_RELATIME,
		"strictatime": syscall.MS_STRICTATIME,
	}

	for _, opt := range options {
		if f, ok := optionFlags[opt]; ok {
			flags |= f
		} else {
			dataOptions = append(dataOptions, opt)
		}
	}

	data := ""
	if len(dataOptions) > 0 {
		data = joinStrings(dataOptions, ",")
	}

	return flags, data
}

func containsOption(options []string, target string) bool {
	for _, o := range options {
		if o == target {
			return true
		}
	}
	return false
}

func joinStrings(strs []string, sep string) string {
	result := ""
	for i, s := range strs {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}

// SetHostname sets the container's hostname (requires UTS namespace).
func SetHostname(hostname string) error {
	return syscall.Sethostname([]byte(hostname))
}

// CreateDeviceNodes creates essential device nodes in /dev.
func CreateDeviceNodes(rootPath string) error {
	devices := []struct {
		path  string
		mode  uint32
		major uint32
		minor uint32
	}{
		{"/dev/null", syscall.S_IFCHR | 0666, 1, 3},
		{"/dev/zero", syscall.S_IFCHR | 0666, 1, 5},
		{"/dev/full", syscall.S_IFCHR | 0666, 1, 7},
		{"/dev/random", syscall.S_IFCHR | 0666, 1, 8},
		{"/dev/urandom", syscall.S_IFCHR | 0666, 1, 9},
		{"/dev/tty", syscall.S_IFCHR | 0666, 5, 0},
	}

	for _, dev := range devices {
		path := filepath.Join(rootPath, dev.path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			continue
		}
		devNum := int(dev.major<<8 | dev.minor)
		if err := syscall.Mknod(path, dev.mode, devNum); err != nil {
			// Non-fatal: may already exist or lack permissions
			_ = err
		}
	}

	return nil
}
