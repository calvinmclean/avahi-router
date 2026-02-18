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
	ID       string
	Hostname string
	Cmd      *exec.Cmd
}

type ContainerManager struct {
	mu         sync.RWMutex
	containers map[string]*ContainerInfo
	hostIP     string
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	hostIP := getHostIP()
	fmt.Printf("Using host IP: %s\n", hostIP)

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Docker client: %v\n", err)
		os.Exit(1)
	}
	defer cli.Close()

	containers := &ContainerManager{
		containers: make(map[string]*ContainerInfo),
		hostIP:     hostIP,
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
	if info, exists := cm.containers[id]; exists {
		if info.Cmd != nil && info.Cmd.Process != nil {
			if err := info.Cmd.Process.Kill(); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to kill avahi-publish for %s: %v\n", info.Hostname, err)
			}
			if _, err := info.Cmd.Process.Wait(); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to wait for avahi-publish for %s: %v\n", info.Hostname, err)
			}
		}
	}

	// Start avahi-publish
	cmd := exec.CommandContext(ctx, "avahi-publish", "-a", "-R", hostname, cm.hostIP)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start avahi-publish for %s: %v\n", hostname, err)
		return
	}

	fmt.Printf("Advertising %s -> %s (PID: %d)\n", hostname, cm.hostIP, cmd.Process.Pid)

	cm.containers[id] = &ContainerInfo{
		ID:       id,
		Hostname: hostname,
		Cmd:      cmd,
	}

	// Monitor the process
	go func() {
		err := cmd.Wait()
		if err != nil && !strings.Contains(err.Error(), "signal: killed") {
			fmt.Fprintf(os.Stderr, "avahi-publish for %s exited: %v\n", hostname, err)
		}
	}()
}

func (cm *ContainerManager) Stop(id string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.stopLocked(id)
}

func (cm *ContainerManager) stopLocked(id string) {
	if info, exists := cm.containers[id]; exists {
		if info.Cmd != nil && info.Cmd.Process != nil {
			fmt.Printf("Stopping advertisement for %s\n", info.Hostname)
			if err := info.Cmd.Process.Kill(); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to kill avahi-publish for %s: %v\n", info.Hostname, err)
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

func getHostIP() string {
	// Check environment variable first
	if ip := os.Getenv("HOST_IP"); ip != "" {
		return ip
	}

	// Try to find the default interface IP
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to determine host IP: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}
