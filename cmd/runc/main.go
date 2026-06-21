// runc is a minimal OCI-compatible container runtime.
//
// Usage:
//
//	runc create <container-id> <bundle>   Create a container
//	runc start  <container-id>            Start a created container
//	runc run    <container-id> <bundle>   Create and start a container
//	runc state  <container-id>            Query container state
//	runc kill   <container-id> [signal]   Send signal to container
//	runc delete <container-id>            Delete a container
//	runc list                             List all containers
//	runc spec                             Generate a sample OCI spec
//
// This runtime implements a subset of the OCI Runtime Specification:
// https://github.com/opencontainers/runtime-spec
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"text/tabwriter"

	"github.com/Nitaiz123/container-runtime/internal/runtime"
	"github.com/Nitaiz123/container-runtime/pkg/spec"
)

func main() {
	// Check if we're being re-executed as the container init process
	if len(os.Args) >= 2 && os.Args[1] == runtime.InitArg {
		if err := runtime.ContainerInit(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "container init error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	rt := runtime.New()
	command := os.Args[1]

	var err error
	switch command {
	case "create":
		err = cmdCreate(rt)
	case "start":
		err = cmdStart(rt)
	case "run":
		err = cmdRun(rt)
	case "state":
		err = cmdState(rt)
	case "kill":
		err = cmdKill(rt)
	case "delete":
		err = cmdDelete(rt)
	case "list", "ps":
		err = cmdList(rt)
	case "spec":
		err = cmdSpec()
	case "version":
		fmt.Println("container-runtime version 1.0.0")
		fmt.Println("OCI Runtime Spec: 1.0.2")
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func cmdCreate(rt *runtime.Runtime) error {
	if len(os.Args) < 4 {
		return fmt.Errorf("usage: runc create <container-id> <bundle>")
	}
	containerID := os.Args[2]
	bundlePath := os.Args[3]

	c, err := rt.Create(containerID, bundlePath)
	if err != nil {
		return err
	}
	fmt.Printf("Container %s created (state: %s)\n", c.ID, c.State)
	return nil
}

func cmdStart(rt *runtime.Runtime) error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: runc start <container-id>")
	}
	containerID := os.Args[2]
	return rt.Start(containerID)
}

func cmdRun(rt *runtime.Runtime) error {
	if len(os.Args) < 4 {
		return fmt.Errorf("usage: runc run <container-id> <bundle>")
	}
	containerID := os.Args[2]
	bundlePath := os.Args[3]

	if _, err := rt.Create(containerID, bundlePath); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	return rt.Start(containerID)
}

func cmdState(rt *runtime.Runtime) error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: runc state <container-id>")
	}
	containerID := os.Args[2]

	state, err := rt.State(containerID)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func cmdKill(rt *runtime.Runtime) error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: runc kill <container-id> [signal]")
	}
	containerID := os.Args[2]

	sig := syscall.SIGTERM
	if len(os.Args) >= 4 {
		sigNum, err := strconv.Atoi(os.Args[3])
		if err != nil {
			return fmt.Errorf("invalid signal: %s", os.Args[3])
		}
		sig = syscall.Signal(sigNum)
	}

	return rt.Kill(containerID, sig)
}

func cmdDelete(rt *runtime.Runtime) error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: runc delete <container-id>")
	}
	containerID := os.Args[2]
	return rt.Delete(containerID)
}

func cmdList(rt *runtime.Runtime) error {
	containers, err := rt.List()
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tPID\tCREATED\tBUNDLE")
	for _, c := range containers {
		pid := runtime.FormatPID(c.PID)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			c.ID, c.State, pid,
			c.CreatedAt.Format("2006-01-02T15:04:05Z"),
			c.BundlePath,
		)
	}
	return w.Flush()
}

func cmdSpec() error {
	memLimit := int64(512 * 1024 * 1024) // 512MB
	cpuQuota := int64(100000)
	cpuPeriod := uint64(1000000)
	pidsLimit := int64(100)

	s := spec.Spec{
		Version: "1.0.2",
		Process: &spec.Process{
			Terminal: false,
			User:     spec.User{UID: 0, GID: 0},
			Args:     []string{"/bin/sh"},
			Env: []string{
				"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				"TERM=xterm",
			},
			Cwd:             "/",
			NoNewPrivileges: true,
		},
		Root: &spec.Root{
			Path:     "rootfs",
			Readonly: false,
		},
		Hostname: "container",
		Mounts: []spec.Mount{
			{Destination: "/proc", Type: "proc", Source: "proc"},
			{Destination: "/dev", Type: "tmpfs", Source: "tmpfs",
				Options: []string{"nosuid", "strictatime", "mode=755", "size=65536k"}},
			{Destination: "/sys", Type: "sysfs", Source: "sysfs",
				Options: []string{"nosuid", "noexec", "nodev", "ro"}},
		},
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
				CPU: &spec.LinuxCPU{
					Quota:  &cpuQuota,
					Period: &cpuPeriod,
				},
				Pids: &spec.LinuxPids{Limit: pidsLimit},
			},
			MaskedPaths: []string{
				"/proc/acpi", "/proc/kcore", "/proc/keys",
				"/proc/latency_stats", "/proc/timer_list",
				"/proc/timer_stats", "/proc/sched_debug",
				"/proc/scsi", "/sys/firmware",
			},
			ReadonlyPaths: []string{
				"/proc/asound", "/proc/bus", "/proc/fs",
				"/proc/irq", "/proc/sys", "/proc/sysrq-trigger",
			},
		},
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func printUsage() {
	fmt.Println(`container-runtime - A minimal OCI container runtime

USAGE:
  runc <command> [arguments]

COMMANDS:
  create  <id> <bundle>    Create a container from an OCI bundle
  start   <id>             Start a created container
  run     <id> <bundle>    Create and start a container
  state   <id>             Query the state of a container
  kill    <id> [signal]    Send a signal to a container (default: SIGTERM)
  delete  <id>             Delete a container and clean up resources
  list                     List all containers
  spec                     Generate a sample OCI runtime spec (config.json)
  version                  Print version information

EXAMPLES:
  # Generate a spec
  runc spec > bundle/config.json

  # Create and run a container
  runc run mycontainer ./bundle

  # Check container state
  runc state mycontainer

  # Stop and remove
  runc kill mycontainer
  runc delete mycontainer

OCI Runtime Spec: https://github.com/opencontainers/runtime-spec`)
}
