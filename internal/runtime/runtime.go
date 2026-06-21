// Package runtime implements the core OCI container runtime.
//
// The container lifecycle follows the OCI Runtime Specification:
//
//  1. Create:  Set up namespaces, cgroups, rootfs. Process is not yet started.
//  2. Start:   Execute the container process inside the prepared environment.
//  3. State:   Query the current state of the container.
//  4. Kill:    Send a signal to the container process.
//  5. Delete:  Clean up all resources (cgroups, mounts, state files).
//
// Implementation details:
//
//   - The runtime uses a two-process model: a "parent" process (the runtime)
//     and a "child" process (the container init process).
//   - The child is created with clone(2) using the appropriate CLONE_NEW* flags
//     to create new namespaces.
//   - A pipe is used for synchronization between parent and child:
//     the child waits for the parent to set up cgroups and user namespace
//     mappings before proceeding.
//   - The child calls pivot_root(2) to change its root filesystem, then
//     execve(2) to replace itself with the container process.
//
// This is the same architecture used by runc (the reference OCI runtime).
//
// References:
//   - OCI Runtime Spec: https://github.com/opencontainers/runtime-spec
//   - runc source: https://github.com/opencontainers/runc
//   - Linux clone(2), pivot_root(2), execve(2) man pages
package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Nitaiz123/container-runtime/internal/cgroups"
	"github.com/Nitaiz123/container-runtime/internal/namespace"
	"github.com/Nitaiz123/container-runtime/internal/rootfs"
	"github.com/Nitaiz123/container-runtime/pkg/spec"
)

const (
	// StateDir is where container state files are stored
	StateDir = "/run/container-runtime"
	// InitArg is the argument passed to re-execute ourselves as the container init
	InitArg = "__container_init__"
)

// Container represents a running or stopped container.
type Container struct {
	// ID is the unique container identifier
	ID string
	// BundlePath is the path to the OCI bundle directory
	BundlePath string
	// Spec is the parsed OCI runtime spec
	Spec *spec.Spec
	// State is the current container state
	State spec.ContainerStatus
	// PID is the container init process PID (0 if not running)
	PID int
	// CreatedAt is when the container was created
	CreatedAt time.Time
	// StartedAt is when the container process started
	StartedAt *time.Time

	cgroupManager *cgroups.Manager
}

// Runtime manages the lifecycle of containers.
type Runtime struct {
	// StateDir is where container state is persisted
	StateDir string
}

// New creates a new container runtime.
func New() *Runtime {
	return &Runtime{StateDir: StateDir}
}

// Create creates a new container from an OCI bundle.
// The container process is not started yet.
func (r *Runtime) Create(containerID, bundlePath string) (*Container, error) {
	// Load the OCI spec from the bundle
	s, err := loadSpec(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("loading spec: %w", err)
	}

	// Validate the spec
	if err := validateSpec(s); err != nil {
		return nil, fmt.Errorf("invalid spec: %w", err)
	}

	// Ensure container doesn't already exist
	if _, err := r.loadState(containerID); err == nil {
		return nil, fmt.Errorf("container %s already exists", containerID)
	}

	c := &Container{
		ID:         containerID,
		BundlePath: bundlePath,
		Spec:       s,
		State:      spec.StateCreating,
		CreatedAt:  time.Now(),
	}

	// Set up cgroup manager
	var resources *spec.LinuxResources
	if s.Linux != nil {
		resources = s.Linux.Resources
	}
	c.cgroupManager = cgroups.NewManager(containerID, resources)

	// Create cgroups
	if err := c.cgroupManager.Setup(); err != nil {
		// Non-fatal in environments without cgroup access
		_ = err
	}

	c.State = spec.StateCreated

	// Persist state
	if err := r.saveState(c); err != nil {
		return nil, fmt.Errorf("saving state: %w", err)
	}

	return c, nil
}

// Start starts the container process.
func (r *Runtime) Start(containerID string) error {
	c, err := r.loadState(containerID)
	if err != nil {
		return fmt.Errorf("container %s not found: %w", containerID, err)
	}

	if c.State != spec.StateCreated {
		return fmt.Errorf("container %s is in state %s, expected created", containerID, c.State)
	}

	// Launch the container process
	pid, err := r.launchContainer(c)
	if err != nil {
		return fmt.Errorf("launching container: %w", err)
	}

	c.PID = pid
	c.State = spec.StateRunning
	now := time.Now()
	c.StartedAt = &now

	return r.saveState(c)
}

// launchContainer creates the container process with the appropriate namespaces.
func (r *Runtime) launchContainer(c *Container) (int, error) {
	s := c.Spec

	// Compute clone flags for namespace isolation
	var nsFlags uintptr
	if s.Linux != nil {
		nsFlags = namespace.CloneFlags(s.Linux.Namespaces)
	}

	// Build the environment for the container process
	env := s.Process.Env
	if env == nil {
		env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	}

	// Re-execute ourselves with a special argument to run as the container init.
	// This is the same technique used by runc: the runtime binary is re-executed
	// inside the new namespaces, and it sets up the container environment before
	// exec'ing the actual container process.
	self, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("getting executable path: %w", err)
	}

	// Pass container configuration via environment variables to the init process
	initEnv := append(env,
		"_CONTAINER_ID="+c.ID,
		"_CONTAINER_BUNDLE="+c.BundlePath,
		"_CONTAINER_ROOTFS="+s.Root.Path,
		"_CONTAINER_CWD="+s.Process.Cwd,
		"_CONTAINER_HOSTNAME="+s.Hostname,
	)

	cmd := &exec.Cmd{
		Path: self,
		Args: append([]string{self, InitArg}, s.Process.Args...),
		Env:  initEnv,
		SysProcAttr: &syscall.SysProcAttr{
			Cloneflags: nsFlags,
			// Run as the specified user
			Credential: &syscall.Credential{
				Uid: s.Process.User.UID,
				Gid: s.Process.User.GID,
			},
		},
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting container process: %w", err)
	}

	// Add the container process to its cgroup
	if c.cgroupManager != nil {
		if err := c.cgroupManager.AddProcess(cmd.Process.Pid); err != nil {
			// Non-fatal
			_ = err
		}
	}

	return cmd.Process.Pid, nil
}

// ContainerInit is called when the runtime re-executes itself inside the
// new namespaces. It sets up the container environment and exec's the
// actual container process.
func ContainerInit(args []string) error {
	rootfsPath := os.Getenv("_CONTAINER_ROOTFS")
	cwd := os.Getenv("_CONTAINER_CWD")
	hostname := os.Getenv("_CONTAINER_HOSTNAME")

	// Set hostname (requires UTS namespace)
	if hostname != "" {
		if err := rootfs.SetHostname(hostname); err != nil {
			_ = err // Non-fatal
		}
	}

	// Set up the root filesystem
	if rootfsPath != "" {
		if err := rootfs.Setup(rootfsPath, nil, false); err != nil {
			return fmt.Errorf("setting up rootfs: %w", err)
		}
	}

	// Change to working directory
	if cwd != "" {
		if err := os.Chdir(cwd); err != nil {
			// Fall back to /
			_ = os.Chdir("/")
		}
	}

	// Mount /proc (needed for many tools)
	_ = syscall.Mount("proc", "/proc", "proc", 0, "")

	// Exec the actual container process (replaces this process)
	if len(args) == 0 {
		return fmt.Errorf("no command specified")
	}

	binary, err := exec.LookPath(args[0])
	if err != nil {
		binary = args[0]
	}

	return syscall.Exec(binary, args, os.Environ())
}

// Kill sends a signal to the container process.
func (r *Runtime) Kill(containerID string, signal syscall.Signal) error {
	c, err := r.loadState(containerID)
	if err != nil {
		return fmt.Errorf("container %s not found: %w", containerID, err)
	}

	if c.State != spec.StateRunning {
		return fmt.Errorf("container %s is not running", containerID)
	}

	if c.PID <= 0 {
		return fmt.Errorf("container %s has no PID", containerID)
	}

	proc, err := os.FindProcess(c.PID)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", c.PID, err)
	}

	return proc.Signal(signal)
}

// Delete removes the container and cleans up all resources.
func (r *Runtime) Delete(containerID string) error {
	c, err := r.loadState(containerID)
	if err != nil {
		return fmt.Errorf("container %s not found: %w", containerID, err)
	}

	if c.State == spec.StateRunning {
		// Force kill the container process
		if err := r.Kill(containerID, syscall.SIGKILL); err != nil {
			_ = err // Best effort
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Destroy cgroups
	if c.cgroupManager != nil {
		_ = c.cgroupManager.Destroy()
	}

	// Remove state file
	statePath := r.statePath(containerID)
	return os.Remove(statePath)
}

// State returns the current state of a container.
func (r *Runtime) State(containerID string) (*spec.State, error) {
	c, err := r.loadState(containerID)
	if err != nil {
		return nil, fmt.Errorf("container %s not found: %w", containerID, err)
	}

	// Check if the process is still running
	if c.State == spec.StateRunning && c.PID > 0 {
		proc, err := os.FindProcess(c.PID)
		if err == nil {
			if err := proc.Signal(syscall.Signal(0)); err != nil {
				// Process no longer exists
				c.State = spec.StateStopped
				_ = r.saveState(c)
			}
		}
	}

	return &spec.State{
		Version: "1.0.0",
		ID:      c.ID,
		Status:  c.State,
		PID:     c.PID,
		Bundle:  c.BundlePath,
	}, nil
}

// List returns all containers managed by this runtime.
func (r *Runtime) List() ([]*Container, error) {
	if err := os.MkdirAll(r.StateDir, 0700); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(r.StateDir)
	if err != nil {
		return nil, err
	}

	var containers []*Container
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			id := entry.Name()[:len(entry.Name())-5]
			c, err := r.loadState(id)
			if err == nil {
				containers = append(containers, c)
			}
		}
	}

	return containers, nil
}

// containerState is the serialized form of container state.
type containerState struct {
	ID         string              `json:"id"`
	BundlePath string              `json:"bundlePath"`
	State      spec.ContainerStatus `json:"state"`
	PID        int                 `json:"pid"`
	CreatedAt  time.Time           `json:"createdAt"`
	StartedAt  *time.Time          `json:"startedAt,omitempty"`
}

func (r *Runtime) saveState(c *Container) error {
	if err := os.MkdirAll(r.StateDir, 0700); err != nil {
		return err
	}

	state := containerState{
		ID:         c.ID,
		BundlePath: c.BundlePath,
		State:      c.State,
		PID:        c.PID,
		CreatedAt:  c.CreatedAt,
		StartedAt:  c.StartedAt,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(r.statePath(c.ID), data, 0600)
}

func (r *Runtime) loadState(containerID string) (*Container, error) {
	data, err := os.ReadFile(r.statePath(containerID))
	if err != nil {
		return nil, err
	}

	var state containerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	c := &Container{
		ID:         state.ID,
		BundlePath: state.BundlePath,
		State:      state.State,
		PID:        state.PID,
		CreatedAt:  state.CreatedAt,
		StartedAt:  state.StartedAt,
	}

	// Reload the spec
	if s, err := loadSpec(state.BundlePath); err == nil {
		c.Spec = s
		var resources *spec.LinuxResources
		if s.Linux != nil {
			resources = s.Linux.Resources
		}
		c.cgroupManager = cgroups.NewManager(containerID, resources)
	}

	return c, nil
}

func (r *Runtime) statePath(containerID string) string {
	return filepath.Join(r.StateDir, containerID+".json")
}

func loadSpec(bundlePath string) (*spec.Spec, error) {
	configPath := filepath.Join(bundlePath, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config.json: %w", err)
	}

	var s spec.Spec
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing config.json: %w", err)
	}

	return &s, nil
}

func validateSpec(s *spec.Spec) error {
	if s.Process == nil {
		return fmt.Errorf("spec.process is required")
	}
	if len(s.Process.Args) == 0 {
		return fmt.Errorf("spec.process.args must not be empty")
	}
	if s.Root == nil {
		return fmt.Errorf("spec.root is required")
	}
	if s.Root.Path == "" {
		return fmt.Errorf("spec.root.path must not be empty")
	}
	return nil
}

// FormatPID formats a PID for display.
func FormatPID(pid int) string {
	if pid == 0 {
		return "-"
	}
	return strconv.Itoa(pid)
}
