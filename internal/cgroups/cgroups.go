// Package cgroups handles Linux control group (cgroup) management.
//
// Control groups (cgroups) are a Linux kernel feature that limits, accounts for,
// and isolates the resource usage (CPU, memory, disk I/O, network, etc.) of
// a collection of processes.
//
// cgroups v2 (unified hierarchy) is used here, which is the default in modern
// Linux distributions (Ubuntu 22.04+, Fedora 31+, Debian 11+).
//
// In cgroups v2, all controllers are under a single unified hierarchy at
// /sys/fs/cgroup/. A container gets its own cgroup directory, and the runtime
// writes resource limits to the controller files within that directory.
//
// Key controllers:
//   - memory: limits memory usage (memory.max, memory.swap.max)
//   - cpu: limits CPU time (cpu.max = quota/period)
//   - pids: limits number of processes (pids.max)
//   - io: limits block I/O bandwidth
//
// References:
//   - cgroups(7) man page
//   - Linux kernel docs: Documentation/admin-guide/cgroup-v2.rst
//   - systemd cgroup documentation
package cgroups

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Nitaiz123/container-runtime/pkg/spec"
)

const (
	// CgroupV2Root is the root of the cgroup v2 unified hierarchy
	CgroupV2Root = "/sys/fs/cgroup"
	// ContainerCgroupPrefix is the prefix for container cgroup paths
	ContainerCgroupPrefix = "container-runtime"
)

// Manager manages cgroups for a container.
type Manager struct {
	// ContainerID is the unique container identifier
	ContainerID string
	// CgroupPath is the path to the container's cgroup directory
	CgroupPath string
	// Resources are the resource constraints to apply
	Resources *spec.LinuxResources
}

// NewManager creates a new cgroup manager for a container.
func NewManager(containerID string, resources *spec.LinuxResources) *Manager {
	cgroupPath := filepath.Join(CgroupV2Root, ContainerCgroupPrefix, containerID)
	return &Manager{
		ContainerID: containerID,
		CgroupPath:  cgroupPath,
		Resources:   resources,
	}
}

// Setup creates the cgroup directory and applies resource limits.
func (m *Manager) Setup() error {
	// Create cgroup directory
	if err := os.MkdirAll(m.CgroupPath, 0755); err != nil {
		return fmt.Errorf("creating cgroup dir %s: %w", m.CgroupPath, err)
	}

	// Enable required controllers in the parent cgroup
	if err := m.enableControllers(); err != nil {
		// Non-fatal: controllers may already be enabled
		_ = err
	}

	// Apply resource limits
	if m.Resources != nil {
		if err := m.applyResources(); err != nil {
			return fmt.Errorf("applying cgroup resources: %w", err)
		}
	}

	return nil
}

// AddProcess adds a process to this cgroup by writing its PID to cgroup.procs.
func (m *Manager) AddProcess(pid int) error {
	procsPath := filepath.Join(m.CgroupPath, "cgroup.procs")
	return os.WriteFile(procsPath, []byte(strconv.Itoa(pid)), 0700)
}

// Destroy removes the cgroup directory (must be empty of processes first).
func (m *Manager) Destroy() error {
	return os.RemoveAll(m.CgroupPath)
}

// Stats returns current resource usage statistics for the cgroup.
func (m *Manager) Stats() (*CgroupStats, error) {
	stats := &CgroupStats{}

	// Memory current usage
	if data, err := m.readFile("memory.current"); err == nil {
		stats.MemoryUsageBytes, _ = strconv.ParseInt(strings.TrimSpace(data), 10, 64)
	}

	// Memory limit
	if data, err := m.readFile("memory.max"); err == nil {
		d := strings.TrimSpace(data)
		if d != "max" {
			stats.MemoryLimitBytes, _ = strconv.ParseInt(d, 10, 64)
		}
	}

	// CPU usage
	if data, err := m.readFile("cpu.stat"); err == nil {
		for _, line := range strings.Split(data, "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 && parts[0] == "usage_usec" {
				stats.CPUUsageMicroseconds, _ = strconv.ParseInt(parts[1], 10, 64)
			}
		}
	}

	// PID count
	if data, err := m.readFile("pids.current"); err == nil {
		stats.PIDCount, _ = strconv.ParseInt(strings.TrimSpace(data), 10, 64)
	}

	return stats, nil
}

// CgroupStats holds resource usage statistics.
type CgroupStats struct {
	MemoryUsageBytes     int64
	MemoryLimitBytes     int64
	CPUUsageMicroseconds int64
	PIDCount             int64
}

// enableControllers enables the required cgroup controllers in the parent.
func (m *Manager) enableControllers() error {
	parentPath := filepath.Dir(m.CgroupPath)
	subtreePath := filepath.Join(parentPath, "cgroup.subtree_control")
	controllers := "+memory +cpu +pids"
	return os.WriteFile(subtreePath, []byte(controllers), 0700)
}

// applyResources writes resource limits to the cgroup controller files.
func (m *Manager) applyResources() error {
	res := m.Resources

	// Memory limits
	if res.Memory != nil {
		if res.Memory.Limit != nil {
			if err := m.writeFile("memory.max", strconv.FormatInt(*res.Memory.Limit, 10)); err != nil {
				return fmt.Errorf("setting memory.max: %w", err)
			}
		}
		if res.Memory.Swap != nil {
			if err := m.writeFile("memory.swap.max", strconv.FormatInt(*res.Memory.Swap, 10)); err != nil {
				// Swap may not be available on all systems
				_ = err
			}
		}
	}

	// CPU limits
	// cpu.max format: "quota period" (e.g., "100000 1000000" = 10% CPU)
	// "max 1000000" means no limit
	if res.CPU != nil {
		if res.CPU.Quota != nil && res.CPU.Period != nil {
			cpuMax := fmt.Sprintf("%d %d", *res.CPU.Quota, *res.CPU.Period)
			if err := m.writeFile("cpu.max", cpuMax); err != nil {
				return fmt.Errorf("setting cpu.max: %w", err)
			}
		}
		if res.CPU.Shares != nil {
			// cpu.weight in cgroups v2 (range 1-10000, default 100)
			// Convert from v1 shares (range 2-262144, default 1024) to v2 weight
			weight := (*res.CPU.Shares * 10000) / 262144
			if weight < 1 {
				weight = 1
			}
			if err := m.writeFile("cpu.weight", strconv.FormatUint(weight, 10)); err != nil {
				_ = err // Non-fatal
			}
		}
	}

	// PID limits
	if res.Pids != nil {
		if err := m.writeFile("pids.max", strconv.FormatInt(res.Pids.Limit, 10)); err != nil {
			return fmt.Errorf("setting pids.max: %w", err)
		}
	}

	return nil
}

// writeFile writes content to a file within the cgroup directory.
func (m *Manager) writeFile(filename, content string) error {
	path := filepath.Join(m.CgroupPath, filename)
	return os.WriteFile(path, []byte(content), 0700)
}

// readFile reads content from a file within the cgroup directory.
func (m *Manager) readFile(filename string) (string, error) {
	path := filepath.Join(m.CgroupPath, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// IsV2 checks if the system is using cgroups v2 (unified hierarchy).
func IsV2() bool {
	_, err := os.Stat(filepath.Join(CgroupV2Root, "cgroup.controllers"))
	return err == nil
}

// GetControllers returns the list of available cgroup controllers.
func GetControllers() ([]string, error) {
	data, err := os.ReadFile(filepath.Join(CgroupV2Root, "cgroup.controllers"))
	if err != nil {
		return nil, err
	}
	return strings.Fields(strings.TrimSpace(string(data))), nil
}
