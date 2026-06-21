# container-runtime

A **minimal OCI-compatible container runtime** built from scratch in Go, implementing the same core mechanisms as [runc](https://github.com/opencontainers/runc) — the reference implementation used by Docker and Kubernetes.

[![CI](https://github.com/Nitaiz123/container-runtime/actions/workflows/ci.yml/badge.svg)](https://github.com/Nitaiz123/container-runtime/actions)
[![Go](https://img.shields.io/badge/go-1.22+-00ADD8)](https://go.dev/)
[![OCI](https://img.shields.io/badge/OCI-Runtime%20Spec%201.0.2-blue)](https://github.com/opencontainers/runtime-spec)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     runc CLI (cmd/runc)                         │
│  create | start | run | state | kill | delete | list | spec     │
└───────────────────────────┬─────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│                   Container Runtime Core                        │
│                                                                 │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────┐  │
│  │  OCI Spec    │  │  State Mgmt  │  │  Container Lifecycle │  │
│  │  (pkg/spec)  │  │  (/run/cr)   │  │  create→start→delete │  │
│  └──────────────┘  └──────────────┘  └──────────────────────┘  │
└───────────────────────────┬─────────────────────────────────────┘
                            │
         ┌──────────────────┼────────────────────┐
         │                  │                    │
         ▼                  ▼                    ▼
┌─────────────────┐ ┌──────────────────┐ ┌──────────────────┐
│   Namespaces    │ │     Cgroups v2   │ │     Rootfs       │
│                 │ │                  │ │                  │
│  CLONE_NEWPID   │ │  memory.max      │ │  pivot_root(2)   │
│  CLONE_NEWNS    │ │  cpu.max         │ │  proc/sys/dev    │
│  CLONE_NEWUTS   │ │  pids.max        │ │  bind mounts     │
│  CLONE_NEWIPC   │ │  cgroup.procs    │ │  device nodes    │
│  CLONE_NEWNET   │ │  Stats/metrics   │ │                  │
│  CLONE_NEWUSER  │ └──────────────────┘ └──────────────────┘
└─────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Network (veth + bridge)                     │
│                                                                 │
│  Host: cr0 bridge (172.18.0.1) ←→ veth pair ←→ eth0 (container)│
│  NAT: iptables MASQUERADE for outbound traffic                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## How It Works

### Container Isolation via Linux Namespaces

Linux namespaces are the foundation of container isolation. When a container is created, the runtime calls `clone(2)` with `CLONE_NEW*` flags:

| Namespace | Flag | Isolates |
|-----------|------|---------|
| PID | `CLONE_NEWPID` | Process IDs — container sees only its own processes |
| Mount | `CLONE_NEWNS` | Filesystem mounts — container has its own mount table |
| UTS | `CLONE_NEWUTS` | Hostname and domain name |
| IPC | `CLONE_NEWIPC` | System V IPC, POSIX message queues |
| Network | `CLONE_NEWNET` | Network interfaces, routing tables, ports |
| User | `CLONE_NEWUSER` | UID/GID mappings (unprivileged containers) |

### Resource Limits via cgroups v2

The unified cgroup hierarchy at `/sys/fs/cgroup/` is used to enforce resource limits:

```
/sys/fs/cgroup/container-runtime/<container-id>/
├── memory.max          # Memory limit (e.g., "536870912" = 512MB)
├── memory.current      # Current memory usage
├── cpu.max             # CPU quota/period (e.g., "100000 1000000" = 10%)
├── cpu.weight          # Relative CPU priority
├── pids.max            # Max number of processes
├── pids.current        # Current process count
└── cgroup.procs        # Write PID here to add process to cgroup
```

### Filesystem Isolation via pivot_root

Instead of `chroot(2)` (which can be escaped), the runtime uses `pivot_root(2)`:

```
1. Bind-mount container rootfs to itself (required by pivot_root)
2. Mount proc, sys, dev inside the rootfs
3. pivot_root(newRoot, newRoot/.pivot_root)
   → Container process now sees newRoot as /
4. Unmount old root (/.pivot_root) with MNT_DETACH
5. Remove .pivot_root directory
```

### Two-Process Model

The runtime uses a two-process model (same as runc):

```
Parent (runtime)                    Child (container init)
─────────────────                   ──────────────────────
1. clone(CLONE_NEW*)  ─────────────▶  Created in new namespaces
2. Write uid_map/gid_map             Wait for parent signal
3. Add PID to cgroup  ─────────────▶  Proceed
4. Wait for child                    Setup rootfs (pivot_root)
                                     Set hostname
                                     execve(container process)
```

---

## OCI Runtime Spec Compliance

This runtime implements the [OCI Runtime Specification v1.0.2](https://github.com/opencontainers/runtime-spec):

| Operation | Implemented | Notes |
|-----------|-------------|-------|
| `create` | ✅ | Namespaces, cgroups, rootfs setup |
| `start` | ✅ | Executes container process |
| `state` | ✅ | Returns JSON state |
| `kill` | ✅ | Sends signal to container PID |
| `delete` | ✅ | Cleans up cgroups, state |
| Namespace isolation | ✅ | PID, Mount, UTS, IPC, Network, User |
| cgroups v2 | ✅ | Memory, CPU, PIDs |
| `pivot_root` | ✅ | Secure rootfs isolation |
| Bind mounts | ✅ | OCI mount spec |
| Network (veth) | ✅ | Bridge networking |
| Seccomp | ⚠️ | Spec parsing only |
| Capabilities | ⚠️ | Spec parsing only |

---

## Getting Started

### Prerequisites

- Linux kernel 5.0+ (cgroups v2)
- Go 1.22+
- Root privileges (for namespace and cgroup operations)

### Build

```bash
git clone https://github.com/Nitaiz123/container-runtime.git
cd container-runtime
go build -o runc ./cmd/runc
```

### Create a Container

```bash
# 1. Create a minimal rootfs (using Alpine as an example)
mkdir -p bundle/rootfs
docker export $(docker create alpine) | tar -C bundle/rootfs -xf -

# 2. Generate an OCI spec
./runc spec > bundle/config.json

# 3. Create the container
sudo ./runc create mycontainer ./bundle

# 4. Start it
sudo ./runc start mycontainer

# 5. Check state
sudo ./runc state mycontainer

# 6. Clean up
sudo ./runc delete mycontainer
```

### Or use `run` (create + start in one step):

```bash
sudo ./runc run mycontainer ./bundle
```

### List All Containers

```bash
sudo ./runc list
```

### Running Tests

```bash
go test ./tests/... -v
```

---

## Project Structure

```
container-runtime/
├── cmd/runc/
│   └── main.go                 # CLI entry point (create/start/run/state/kill/delete/list)
├── internal/
│   ├── runtime/
│   │   └── runtime.go          # Core container lifecycle management
│   ├── namespace/
│   │   └── namespace.go        # Linux namespace creation (clone flags, setns, uid/gid maps)
│   ├── cgroups/
│   │   └── cgroups.go          # cgroups v2: memory, CPU, PIDs limits and stats
│   ├── rootfs/
│   │   └── rootfs.go           # pivot_root, mount setup, device nodes
│   └── network/
│       └── network.go          # veth pairs, bridge networking, NAT
├── pkg/
│   └── spec/
│       └── spec.go             # OCI Runtime Spec types (Spec, Process, Linux, etc.)
└── tests/
    └── runtime_test.go         # 11 unit tests
```

---

## Comparison with runc

| Feature | This Runtime | runc |
|---------|-------------|------|
| Language | Go | Go |
| OCI Spec | v1.0.2 (subset) | v1.0.2 (full) |
| Namespaces | All 7 types | All 7 types |
| cgroups | v2 only | v1 + v2 |
| Seccomp | Spec parsing | Full enforcement |
| Capabilities | Spec parsing | Full enforcement |
| Rootfs | pivot_root | pivot_root |
| Network | veth + bridge | via CNI plugins |
| Lines of code | ~1,500 | ~50,000+ |

---

## References

- [OCI Runtime Specification](https://github.com/opencontainers/runtime-spec)
- [runc — Reference OCI Runtime](https://github.com/opencontainers/runc)
- [Linux namespaces(7)](https://man7.org/linux/man-pages/man7/namespaces.7.html)
- [Linux cgroups(7)](https://man7.org/linux/man-pages/man7/cgroups.7.html)
- [Liz Rice — Containers from Scratch (GopherCon 2016)](https://www.youtube.com/watch?v=8fi7uSYlOdc)

---

## License

MIT — see [LICENSE](LICENSE)
