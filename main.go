package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

const (
	labelKey         = "avahi.hostname"
	traefikEnableKey = "traefik.enable"
	traefikRuleKey   = "traefik.http.routers."
)

type ContainerInfo struct {
	ID        string
	Hostname  string
	Processes []*PublishProcess
}

type PublishProcess struct {
	Interface string
	IP        string
	Cmd       *exec.Cmd
}

type HostAddress struct {
	Interface string
	IP        string
}

type ContainerManager struct {
	mu         sync.RWMutex
	containers map[string]*ContainerInfo
	hostAddrs  []HostAddress
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	hostAddrs, err := getHostAddresses()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to determine host addresses: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Using host addresses: %s\n", formatHostAddresses(hostAddrs))

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Docker client: %v\n", err)
		os.Exit(1)
	}
	defer cli.Close()

	containers := &ContainerManager{
		containers: make(map[string]*ContainerInfo),
		hostAddrs:  hostAddrs,
	}

	// List existing containers with the label
	fltrs := filters.NewArgs()
	fltrs.Add("label", labelKey)
	containersList, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: fltrs,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list containers: %v\n", err)
		os.Exit(1)
	}

	for _, c := range containersList {
		if hostname := getHostnameFromLabels(c.Labels); hostname != "" {
			if c.State == "running" {
				fmt.Printf("Found existing running container %s with hostname %s\n", c.ID[:12], hostname)
				containers.Start(ctx, c.ID, hostname)
			}
		}
	}

	// If Traefik support is enabled, also look for Traefik-enabled containers
	if os.Getenv("TRAEFIK_ENABLED") == "true" {
		fltrs = filters.NewArgs()
		fltrs.Add("label", traefikEnableKey)
		containersList, err := cli.ContainerList(ctx, container.ListOptions{
			All:     true,
			Filters: fltrs,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to list Traefik containers: %v\n", err)
		} else {
			for _, c := range containersList {
				// Skip if already using avahi.hostname
				if _, ok := c.Labels[labelKey]; ok {
					continue
				}
				if hostname := getHostnameFromLabels(c.Labels); hostname != "" {
					if c.State == "running" {
						fmt.Printf("Found existing running container %s with Traefik hostname %s\n", c.ID[:12], hostname)
						containers.Start(ctx, c.ID, hostname)
					}
				}
			}
		}
	}

	// Listen for events
	fltrs = filters.NewArgs()
	fltrs.Add("type", "container")
	msgChan, errChan := cli.Events(ctx, types.EventsOptions{
		Filters: fltrs,
	})

	fmt.Println("Monitoring Docker containers...")

	for {
		select {
		case msg := <-msgChan:
			handleEvent(ctx, cli, msg, containers)
		case err := <-errChan:
			fmt.Fprintf(os.Stderr, "Event error: %v\n", err)
		case sig := <-sigChan:
			fmt.Printf("\nReceived signal %v, shutting down gracefully...\n", sig)
			cancel()
		case <-ctx.Done():
			// Context was cancelled (either by signal or other means)
			// Stop all running advertisements
			containers.StopAll()
			fmt.Println("Shutdown complete")
			return
		}
	}
}

func handleEvent(ctx context.Context, cli *client.Client, msg events.Message, cm *ContainerManager) {
	switch msg.Action {
	case "start":
		containerJSON, err := cli.ContainerInspect(ctx, msg.Actor.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to inspect container %s: %v\n", msg.Actor.ID, err)
			return
		}

		if hostname := getHostnameFromLabels(containerJSON.Config.Labels); hostname != "" {
			src := "avahi.hostname"
			if _, ok := containerJSON.Config.Labels[labelKey]; !ok {
				src = "traefik"
			}
			fmt.Printf("Container %s started with hostname %s (from %s)\n", msg.Actor.ID[:12], hostname, src)
			cm.Start(ctx, msg.Actor.ID, hostname)
		}

	case "stop", "die", "kill":
		if cm.Has(msg.Actor.ID) {
			fmt.Printf("Container %s stopped\n", msg.Actor.ID[:12])
			cm.Stop(msg.Actor.ID)
		}
	}
}

func (cm *ContainerManager) Start(ctx context.Context, id, hostname string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Stop any existing avahi-publish for this container
	cm.stopLocked(id)

	processes := make([]*PublishProcess, 0, len(cm.hostAddrs))
	for _, hostAddr := range cm.hostAddrs {
		cmd := exec.CommandContext(ctx, "avahi-publish", "-a", "-R", hostname, hostAddr.IP)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start avahi-publish for %s on %s -> %s: %v\n", hostname, hostAddr.Interface, hostAddr.IP, err)
			continue
		}

		fmt.Printf("Advertising %s on %s -> %s (PID: %d)\n", hostname, hostAddr.Interface, hostAddr.IP, cmd.Process.Pid)
		processes = append(processes, &PublishProcess{
			Interface: hostAddr.Interface,
			IP:        hostAddr.IP,
			Cmd:       cmd,
		})

		// Monitor the process.
		go func(hostname string, hostAddr HostAddress, cmd *exec.Cmd) {
			err := cmd.Wait()
			if err != nil && !strings.Contains(err.Error(), "signal: killed") {
				fmt.Fprintf(os.Stderr, "avahi-publish for %s on %s -> %s exited: %v\n", hostname, hostAddr.Interface, hostAddr.IP, err)
			}
		}(hostname, hostAddr, cmd)
	}

	if len(processes) == 0 {
		fmt.Fprintf(os.Stderr, "Failed to advertise %s on all configured addresses\n", hostname)
		return
	}

	cm.containers[id] = &ContainerInfo{
		ID:        id,
		Hostname:  hostname,
		Processes: processes,
	}
}

func (cm *ContainerManager) Stop(id string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.stopLocked(id)
}

func (cm *ContainerManager) stopLocked(id string) {
	if info, exists := cm.containers[id]; exists {
		for _, process := range info.Processes {
			if process.Cmd != nil && process.Cmd.Process != nil {
				fmt.Printf("Stopping advertisement for %s on %s -> %s\n", info.Hostname, process.Interface, process.IP)
				if err := process.Cmd.Process.Kill(); err != nil {
					fmt.Fprintf(os.Stderr, "Failed to kill avahi-publish for %s on %s -> %s: %v\n", info.Hostname, process.Interface, process.IP, err)
				}
			}
		}
		delete(cm.containers, id)
	}
}

func (cm *ContainerManager) StopAll() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	fmt.Printf("Stopping all %d advertisements...\n", len(cm.containers))
	for id := range cm.containers {
		cm.stopLocked(id)
	}
}

func (cm *ContainerManager) Has(id string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	_, exists := cm.containers[id]
	return exists
}

func getHostnameFromLabels(labels map[string]string) string {
	// First check for the standard avahi.hostname label
	if hostname, ok := labels[labelKey]; ok {
		return hostname
	}

	// Check if Traefik support is enabled via env var
	if os.Getenv("TRAEFIK_ENABLED") != "true" {
		return ""
	}

	// Check if Traefik is enabled for this container
	if enable, ok := labels[traefikEnableKey]; !ok || enable != "true" {
		return ""
	}

	// Look for Traefik router rules with wildcard match
	for key, value := range labels {
		if strings.HasPrefix(key, traefikRuleKey) && strings.Contains(key, ".rule") {
			if hostname := extractHostnameFromTraefikRule(value); hostname != "" {
				return hostname
			}
		}
	}

	return ""
}

func extractHostnameFromTraefikRule(rule string) string {
	// Look for Host(`hostname`) or Host("hostname") pattern
	var endQuote byte
	start := strings.Index(rule, "Host(`")
	if start == -1 {
		start = strings.Index(rule, "Host(\"")
		if start == -1 {
			return ""
		}
		start += 6 // len("Host(\"")
		endQuote = '"'
	} else {
		start += 6 // len("Host(`")
		endQuote = '`'
	}

	// Find the end of the hostname using the matching end quote
	end := strings.IndexByte(rule[start:], endQuote)
	if end == -1 {
		return ""
	}
	end += start

	if end <= start {
		return ""
	}

	return rule[start:end]
}

func getHostAddresses() ([]HostAddress, error) {
	if interfaces := parseInterfaceNames(os.Getenv("HOST_INTERFACES")); len(interfaces) > 0 {
		return getAddressesForInterfaces(interfaces)
	}

	// Check environment variable first for the legacy single-IP mode.
	if ip := os.Getenv("HOST_IP"); ip != "" {
		return []HostAddress{{Interface: "HOST_IP", IP: ip}}, nil
	}

	// Fall back to the default-route IP for backwards compatibility.
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return []HostAddress{{Interface: "default-route", IP: localAddr.IP.String()}}, nil
}

func parseInterfaceNames(value string) []string {
	parts := strings.Split(value, ",")
	names := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func getAddressesForInterfaces(interfaceNames []string) ([]HostAddress, error) {
	addresses := make([]HostAddress, 0, len(interfaceNames))
	seenIPs := make(map[string]struct{})
	var missing []string

	for _, interfaceName := range interfaceNames {
		iface, err := net.InterfaceByName(interfaceName)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s (%v)", interfaceName, err))
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			fmt.Fprintf(os.Stderr, "Skipping interface %s because it is down\n", interfaceName)
			continue
		}

		ifaceAddrs, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("failed to list addresses for interface %s: %w", interfaceName, err)
		}

		foundOnInterface := false
		for _, ifaceAddr := range ifaceAddrs {
			ip := extractIPv4(ifaceAddr)
			if ip == "" {
				continue
			}
			if _, exists := seenIPs[ip]; exists {
				continue
			}
			seenIPs[ip] = struct{}{}
			addresses = append(addresses, HostAddress{Interface: interfaceName, IP: ip})
			foundOnInterface = true
		}

		if !foundOnInterface {
			fmt.Fprintf(os.Stderr, "No usable IPv4 address found on interface %s\n", interfaceName)
		}
	}

	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "Skipping unknown interfaces from HOST_INTERFACES: %s\n", strings.Join(missing, ", "))
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("HOST_INTERFACES=%q did not resolve to any usable IPv4 address", strings.Join(interfaceNames, ","))
	}

	return addresses, nil
}

func extractIPv4(addr net.Addr) string {
	var ip net.IP
	switch addr := addr.(type) {
	case *net.IPNet:
		ip = addr.IP
	case *net.IPAddr:
		ip = addr.IP
	default:
		return ""
	}

	ip = ip.To4()
	if ip == nil || !ip.IsGlobalUnicast() {
		return ""
	}
	return ip.String()
}

func formatHostAddresses(addresses []HostAddress) string {
	parts := make([]string, 0, len(addresses))
	for _, address := range addresses {
		parts = append(parts, fmt.Sprintf("%s=%s", address.Interface, address.IP))
	}
	return strings.Join(parts, ", ")
}
