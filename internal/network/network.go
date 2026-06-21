// Package network handles container network namespace setup.
//
// Container networking works by:
//  1. Creating a new network namespace for the container (via CLONE_NEWNET)
//  2. Creating a veth pair: one end (veth0) in the host namespace,
//     one end (eth0) in the container namespace
//  3. Assigning IP addresses to both ends
//  4. Setting up routing (default gateway) inside the container
//  5. Configuring NAT (iptables MASQUERADE) on the host for outbound traffic
//
// This is the same approach used by Docker's bridge networking mode.
//
// Network topology:
//
//	Host namespace:
//	  docker0 (bridge): 172.17.0.1/16
//	    └── veth0 (host end of veth pair)
//
//	Container namespace:
//	  eth0 (container end of veth pair): 172.17.0.2/16
//	  lo: 127.0.0.1/8
//
// References:
//   - ip-link(8), ip-netns(8) man pages
//   - Linux veth(4) driver
//   - Docker networking internals
package network

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	// DefaultBridgeName is the name of the container bridge interface
	DefaultBridgeName = "cr0"
	// DefaultBridgeIP is the IP address of the bridge (gateway for containers)
	DefaultBridgeIP = "172.18.0.1"
	// DefaultSubnet is the subnet for container IPs
	DefaultSubnet = "172.18.0.0/16"
	// DefaultMTU is the MTU for container interfaces
	DefaultMTU = 1500
)

// Config holds network configuration for a container.
type Config struct {
	// ContainerID is used to generate unique interface names
	ContainerID string
	// IPAddress is the container's IP address (e.g., "172.18.0.2")
	IPAddress string
	// Gateway is the default gateway for the container
	Gateway string
	// Subnet is the network subnet (e.g., "172.18.0.0/16")
	Subnet string
	// BridgeName is the host bridge interface name
	BridgeName string
	// MTU for the container interface
	MTU int
}

// DefaultConfig returns a default network configuration for a container.
func DefaultConfig(containerID string, containerIP string) *Config {
	return &Config{
		ContainerID: containerID,
		IPAddress:   containerIP,
		Gateway:     DefaultBridgeIP,
		Subnet:      DefaultSubnet,
		BridgeName:  DefaultBridgeName,
		MTU:         DefaultMTU,
	}
}

// SetupBridge creates the host bridge interface if it doesn't exist.
// This is called once per host, not per container.
func SetupBridge(bridgeName, bridgeIP string) error {
	// Check if bridge already exists
	if interfaceExists(bridgeName) {
		return nil
	}

	// Create bridge
	if err := runIP("link", "add", bridgeName, "type", "bridge"); err != nil {
		return fmt.Errorf("creating bridge %s: %w", bridgeName, err)
	}

	// Assign IP to bridge
	if err := runIP("addr", "add", bridgeIP+"/16", "dev", bridgeName); err != nil {
		return fmt.Errorf("assigning IP to bridge: %w", err)
	}

	// Bring bridge up
	if err := runIP("link", "set", bridgeName, "up"); err != nil {
		return fmt.Errorf("bringing bridge up: %w", err)
	}

	// Enable IP forwarding
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644); err != nil {
		return fmt.Errorf("enabling IP forwarding: %w", err)
	}

	return nil
}

// SetupVeth creates a veth pair and connects the container to the host bridge.
// hostVeth is the host-side interface, containerVeth is moved to the container namespace.
func SetupVeth(containerPID int, cfg *Config) (hostVeth, containerVeth string, err error) {
	// Generate unique interface names based on container ID (max 15 chars for Linux)
	shortID := cfg.ContainerID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	hostVeth = "veth" + shortID
	containerVeth = "eth0"

	// Create veth pair
	if err = runIP("link", "add", hostVeth, "type", "veth", "peer", "name", containerVeth); err != nil {
		return "", "", fmt.Errorf("creating veth pair: %w", err)
	}

	// Attach host end to bridge
	if err = runIP("link", "set", hostVeth, "master", cfg.BridgeName); err != nil {
		_ = runIP("link", "del", hostVeth) // cleanup
		return "", "", fmt.Errorf("attaching veth to bridge: %w", err)
	}

	// Bring host end up
	if err = runIP("link", "set", hostVeth, "up"); err != nil {
		_ = runIP("link", "del", hostVeth)
		return "", "", fmt.Errorf("bringing host veth up: %w", err)
	}

	// Move container end to container's network namespace
	if err = runIP("link", "set", containerVeth, "netns", strconv.Itoa(containerPID)); err != nil {
		_ = runIP("link", "del", hostVeth)
		return "", "", fmt.Errorf("moving veth to container netns: %w", err)
	}

	return hostVeth, containerVeth, nil
}

// ConfigureContainerNetwork sets up networking inside the container namespace.
// This must be called from within the container's network namespace.
func ConfigureContainerNetwork(ifaceName, ipAddr, subnet, gateway string) error {
	// Parse CIDR prefix length
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return fmt.Errorf("parsing subnet %s: %w", subnet, err)
	}
	prefixLen, _ := ipNet.Mask.Size()
	cidr := fmt.Sprintf("%s/%d", ipAddr, prefixLen)

	// Bring loopback up
	if err := runIP("link", "set", "lo", "up"); err != nil {
		return fmt.Errorf("bringing lo up: %w", err)
	}

	// Assign IP to container interface
	if err := runIP("addr", "add", cidr, "dev", ifaceName); err != nil {
		return fmt.Errorf("assigning IP %s to %s: %w", cidr, ifaceName, err)
	}

	// Bring container interface up
	if err := runIP("link", "set", ifaceName, "up"); err != nil {
		return fmt.Errorf("bringing %s up: %w", ifaceName, err)
	}

	// Add default route via gateway
	if err := runIP("route", "add", "default", "via", gateway); err != nil {
		return fmt.Errorf("adding default route via %s: %w", gateway, err)
	}

	return nil
}

// SetupNAT configures iptables NAT for outbound container traffic.
func SetupNAT(bridgeName, subnet string) error {
	// Enable MASQUERADE for traffic from the container subnet
	cmd := exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING",
		"-s", subnet, "!", "-o", bridgeName, "-j", "MASQUERADE")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("setting up NAT: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// TeardownVeth removes the host-side veth interface.
func TeardownVeth(hostVeth string) error {
	return runIP("link", "del", hostVeth)
}

// interfaceExists checks if a network interface exists.
func interfaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

// runIP executes an `ip` command with the given arguments.
func runIP(args ...string) error {
	cmd := exec.Command("ip", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

// AllocateIP allocates the next available IP in the subnet.
// In production, this would use a proper IPAM (IP Address Management) system.
func AllocateIP(subnet string, allocated []string) (string, error) {
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("parsing subnet: %w", err)
	}

	allocatedSet := make(map[string]bool)
	for _, ip := range allocated {
		allocatedSet[ip] = true
	}

	// Start from .2 (skip .1 which is the gateway)
	ip := ipNet.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("only IPv4 supported")
	}

	for i := 2; i < 255; i++ {
		candidate := fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], byte(i))
		if !allocatedSet[candidate] {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no available IPs in subnet %s", subnet)
}
