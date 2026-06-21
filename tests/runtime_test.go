package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nitaiz123/container-runtime/internal/cgroups"
	"github.com/Nitaiz123/container-runtime/internal/namespace"
	"github.com/Nitaiz123/container-runtime/internal/runtime"
	"github.com/Nitaiz123/container-runtime/pkg/spec"
)

// ---- Spec Tests ----

func TestSpecSerialization(t *testing.T) {
	memLimit := int64(512 * 1024 * 1024)
	cpuQuota := int64(100000)
	cpuPeriod := uint64(1000000)
	pidsLimit := int64(100)

	s := spec.Spec{
		Version: "1.0.2",
		Process: &spec.Process{
			Args: []string{"/bin/sh"},
			Env:  []string{"PATH=/usr/bin"},
			Cwd:  "/",
			User: spec.User{UID: 0, GID: 0},
		},
		Root: &spec.Root{Path: "rootfs"},
		Linux: &spec.Linux{
			Namespaces: []spec.LinuxNamespace{
				{Type: spec.PIDNamespace},
				{Type: spec.MountNamespace},
				{Type: spec.NetworkNamespace},
			},
			Resources: &spec.LinuxResources{
				Memory: &spec.LinuxMemory{Limit: &memLimit},
				CPU:    &spec.LinuxCPU{Quota: &cpuQuota, Period: &cpuPeriod},
				Pids:   &spec.LinuxPids{Limit: pidsLimit},
			},
		},
	}

	// Serialize
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}

	// Deserialize
	var s2 spec.Spec
	if err := json.Unmarshal(data, &s2); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}

	if s2.Version != s.Version {
		t.Errorf("version mismatch: got %s, want %s", s2.Version, s.Version)
	}
	if s2.Process.Args[0] != s.Process.Args[0] {
		t.Errorf("args mismatch: got %v, want %v", s2.Process.Args, s.Process.Args)
	}
	if *s2.Linux.Resources.Memory.Limit != memLimit {
		t.Errorf("memory limit mismatch")
	}
	if *s2.Linux.Resources.CPU.Quota != cpuQuota {
		t.Errorf("cpu quota mismatch")
	}
	if s2.Linux.Resources.Pids.Limit != pidsLimit {
		t.Errorf("pids limit mismatch")
	}
}

func TestSpecValidation(t *testing.T) {
	tests := []struct {
		name    string
		spec    spec.Spec
		wantErr bool
	}{
		{
			name: "valid spec",
			spec: spec.Spec{
				Process: &spec.Process{Args: []string{"/bin/sh"}},
				Root:    &spec.Root{Path: "rootfs"},
			},
			wantErr: false,
		},
		{
			name: "missing process",
			spec: spec.Spec{
				Root: &spec.Root{Path: "rootfs"},
			},
			wantErr: true,
		},
		{
			name: "empty args",
			spec: spec.Spec{
				Process: &spec.Process{Args: []string{}},
				Root:    &spec.Root{Path: "rootfs"},
			},
			wantErr: true,
		},
		{
			name: "missing root",
			spec: spec.Spec{
				Process: &spec.Process{Args: []string{"/bin/sh"}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSpec(&tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSpec() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func validateSpec(s *spec.Spec) error {
	if s.Process == nil {
		return errorf("spec.process is required")
	}
	if len(s.Process.Args) == 0 {
		return errorf("spec.process.args must not be empty")
	}
	if s.Root == nil {
		return errorf("spec.root is required")
	}
	if s.Root.Path == "" {
		return errorf("spec.root.path must not be empty")
	}
	return nil
}

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }
func errorf(msg string) error            { return &validationError{msg} }

// ---- Namespace Tests ----

func TestNamespaceCloneFlags(t *testing.T) {
	namespaces := []spec.LinuxNamespace{
		{Type: spec.PIDNamespace},
		{Type: spec.MountNamespace},
		{Type: spec.UTSNamespace},
		{Type: spec.NetworkNamespace},
	}

	flags := namespace.CloneFlags(namespaces)

	// All 4 namespace flags should be set
	if flags == 0 {
		t.Error("expected non-zero clone flags")
	}

	// Test individual flags
	for _, ns := range namespaces {
		expectedFlag := namespace.NamespaceFlags[ns.Type]
		if flags&expectedFlag == 0 {
			t.Errorf("expected flag for %s namespace to be set", ns.Type)
		}
	}
}

func TestNamespaceCloneFlagsEmpty(t *testing.T) {
	flags := namespace.CloneFlags(nil)
	if flags != 0 {
		t.Errorf("expected 0 flags for empty namespaces, got %d", flags)
	}
}

func TestNamespaceCloneFlagsWithPath(t *testing.T) {
	// Namespaces with a path should NOT set clone flags (they join existing ns)
	namespaces := []spec.LinuxNamespace{
		{Type: spec.PIDNamespace, Path: "/proc/1/ns/pid"},
	}
	flags := namespace.CloneFlags(namespaces)
	if flags != 0 {
		t.Errorf("expected 0 flags for namespace with path, got %d", flags)
	}
}

func TestNamespaceGetPath(t *testing.T) {
	// Test that namespace paths are constructed correctly
	path := namespace.GetNamespacePath(1, spec.PIDNamespace)
	expected := "/proc/1/ns/pid"
	if path != expected {
		t.Errorf("got %s, want %s", path, expected)
	}

	path = namespace.GetNamespacePath(1, spec.NetworkNamespace)
	expected = "/proc/1/ns/net"
	if path != expected {
		t.Errorf("got %s, want %s", path, expected)
	}
}

// ---- Cgroup Tests ----

func TestCgroupManagerCreation(t *testing.T) {
	memLimit := int64(256 * 1024 * 1024)
	resources := &spec.LinuxResources{
		Memory: &spec.LinuxMemory{Limit: &memLimit},
		Pids:   &spec.LinuxPids{Limit: 50},
	}

	mgr := cgroups.NewManager("test-container-123", resources)
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}

	if mgr.ContainerID != "test-container-123" {
		t.Errorf("wrong container ID: %s", mgr.ContainerID)
	}

	if mgr.CgroupPath == "" {
		t.Error("expected non-empty cgroup path")
	}
}

func TestCgroupV2Detection(t *testing.T) {
	// Just test that the function runs without panicking
	isV2 := cgroups.IsV2()
	t.Logf("cgroups v2 available: %v", isV2)
}

// ---- Runtime State Tests ----

func TestRuntimeCreateAndState(t *testing.T) {
	// Create a temporary directory for state
	stateDir, err := os.MkdirTemp("", "container-runtime-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(stateDir)

	// Create a minimal OCI bundle
	bundleDir, err := os.MkdirTemp("", "bundle-*")
	if err != nil {
		t.Fatalf("creating bundle dir: %v", err)
	}
	defer os.RemoveAll(bundleDir)

	rootfsDir := filepath.Join(bundleDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		t.Fatalf("creating rootfs dir: %v", err)
	}

	// Write a minimal config.json
	s := spec.Spec{
		Version: "1.0.2",
		Process: &spec.Process{
			Args: []string{"/bin/sh"},
			Cwd:  "/",
			User: spec.User{UID: 0, GID: 0},
		},
		Root:     &spec.Root{Path: rootfsDir},
		Hostname: "test-container",
		Linux: &spec.Linux{
			Namespaces: []spec.LinuxNamespace{
				{Type: spec.PIDNamespace},
			},
		},
	}

	configData, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshaling spec: %v", err)
	}

	if err := os.WriteFile(filepath.Join(bundleDir, "config.json"), configData, 0644); err != nil {
		t.Fatalf("writing config.json: %v", err)
	}

	// Create runtime with custom state dir
	rt := &runtime.Runtime{StateDir: stateDir}

	// Create container
	c, err := rt.Create("test-container-001", bundleDir)
	if err != nil {
		t.Fatalf("creating container: %v", err)
	}

	if c.ID != "test-container-001" {
		t.Errorf("wrong container ID: %s", c.ID)
	}
	if c.State != spec.StateCreated {
		t.Errorf("wrong state: %s, expected created", c.State)
	}

	// Query state
	state, err := rt.State("test-container-001")
	if err != nil {
		t.Fatalf("querying state: %v", err)
	}

	if state.ID != "test-container-001" {
		t.Errorf("wrong state ID: %s", state.ID)
	}
	if state.Status != spec.StateCreated {
		t.Errorf("wrong status: %s", state.Status)
	}

	// List containers
	containers, err := rt.List()
	if err != nil {
		t.Fatalf("listing containers: %v", err)
	}
	if len(containers) != 1 {
		t.Errorf("expected 1 container, got %d", len(containers))
	}

	// Delete container
	if err := rt.Delete("test-container-001"); err != nil {
		t.Fatalf("deleting container: %v", err)
	}

	// Should no longer exist
	if _, err := rt.State("test-container-001"); err == nil {
		t.Error("expected error querying deleted container")
	}
}

func TestRuntimeDuplicateCreate(t *testing.T) {
	stateDir, err := os.MkdirTemp("", "container-runtime-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(stateDir)

	bundleDir, err := os.MkdirTemp("", "bundle-*")
	if err != nil {
		t.Fatalf("creating bundle dir: %v", err)
	}
	defer os.RemoveAll(bundleDir)

	rootfsDir := filepath.Join(bundleDir, "rootfs")
	os.MkdirAll(rootfsDir, 0755)

	s := spec.Spec{
		Version: "1.0.2",
		Process: &spec.Process{Args: []string{"/bin/sh"}, Cwd: "/"},
		Root:    &spec.Root{Path: rootfsDir},
	}
	configData, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(filepath.Join(bundleDir, "config.json"), configData, 0644)

	rt := &runtime.Runtime{StateDir: stateDir}

	// First create should succeed
	if _, err := rt.Create("dup-test", bundleDir); err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	// Second create should fail
	if _, err := rt.Create("dup-test", bundleDir); err == nil {
		t.Error("expected error for duplicate container ID")
	}

	// Cleanup
	rt.Delete("dup-test")
}

// ---- OCI Spec Generation Test ----

func TestSpecGeneration(t *testing.T) {
	memLimit := int64(512 * 1024 * 1024)
	cpuQuota := int64(100000)
	cpuPeriod := uint64(1000000)
	pidsLimit := int64(100)

	s := spec.Spec{
		Version: "1.0.2",
		Process: &spec.Process{
			Args: []string{"/bin/sh"},
			Cwd:  "/",
		},
		Root: &spec.Root{Path: "rootfs"},
		Linux: &spec.Linux{
			Namespaces: []spec.LinuxNamespace{
				{Type: spec.PIDNamespace},
				{Type: spec.MountNamespace},
				{Type: spec.UTSNamespace},
				{Type: spec.IPCNamespace},
				{Type: spec.NetworkNamespace},
			},
			Resources: &spec.LinuxResources{
				Memory: &spec.LinuxMemory{Limit: &memLimit},
				CPU:    &spec.LinuxCPU{Quota: &cpuQuota, Period: &cpuPeriod},
				Pids:   &spec.LinuxPids{Limit: pidsLimit},
			},
		},
	}

	if len(s.Linux.Namespaces) != 5 {
		t.Errorf("expected 5 namespaces, got %d", len(s.Linux.Namespaces))
	}

	// Verify all namespace types are present
	nsTypes := make(map[spec.LinuxNamespaceType]bool)
	for _, ns := range s.Linux.Namespaces {
		nsTypes[ns.Type] = true
	}

	required := []spec.LinuxNamespaceType{
		spec.PIDNamespace, spec.MountNamespace, spec.UTSNamespace,
		spec.IPCNamespace, spec.NetworkNamespace,
	}
	for _, nsType := range required {
		if !nsTypes[nsType] {
			t.Errorf("missing namespace type: %s", nsType)
		}
	}
}
