package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// Client wraps the Docker API client.
type Client struct {
	cli     *client.Client
	runtime string
}

// ContainerInfo holds container details for display.
type ContainerInfo struct {
	Name   string
	Image  string
	Status string
	State  string // "running", "restarting", "exited", etc.
}

// New creates a new Docker client.
func New(ctx context.Context) (*Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	// Determine runtime (docker vs podman)
	runtime := "docker"
	info, err := cli.Info(ctx)
	if err == nil {
		if strings.Contains(strings.ToLower(info.OperatingSystem), "podman") {
			runtime = "podman"
		}
	}

	return &Client{
		cli:     cli,
		runtime: runtime,
	}, nil
}

// Runtime returns the detected container runtime name.
func (c *Client) Runtime() string {
	return c.runtime
}

// Ping checks if the Docker daemon is reachable.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := c.cli.Ping(ctx)
	return err
}

// ListRunning returns all running containers.
func (c *Client) ListRunning(ctx context.Context) ([]ContainerInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	containers, err := c.cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	result := make([]ContainerInfo, 0, len(containers))
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}

		image := c.Image
		if len(image) > 40 {
			image = image[:12] // Use short ID if too long
		}

		result = append(result, ContainerInfo{
			Name:   name,
			Image:  image,
			Status: c.Status,
		})
	}

	return result, nil
}

// Close closes the Docker client connection.
func (c *Client) Close() error {
	if c.cli != nil {
		return c.cli.Close()
	}
	return nil
}

// InspectByName returns info about a container by its name.
// Returns nil with no error if the container does not exist.
func (c *Client) InspectByName(ctx context.Context, name string) (*ContainerInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	data, err := c.cli.ContainerInspect(ctx, name)
	if err != nil {
		if client.IsErrNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to inspect container %q: %w", name, err)
	}

	image := data.Config.Image
	if len(image) > 40 {
		image = image[:12]
	}

	return &ContainerInfo{
		Name:   strings.TrimPrefix(data.Name, "/"),
		Image:  image,
		Status: data.State.Status,
		State:  data.State.Status,
	}, nil
}

// ContainerLogs fetches logs from a container.
// If tail > 0, only the last `tail` lines are returned.
// If tail == 0, all lines are returned.
func (c *Client) ContainerLogs(ctx context.Context, name string, tail int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tailStr := "all"
	if tail > 0 {
		tailStr = fmt.Sprintf("%d", tail)
	}

	reader, err := c.cli.ContainerLogs(ctx, name, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       tailStr,
		Timestamps: false,
	})
	if err != nil {
		return "", fmt.Errorf("failed to fetch logs for %q: %w", name, err)
	}
	defer reader.Close()

	raw, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read logs for %q: %w", name, err)
	}

	// Docker multiplexed logs have an 8-byte header per frame; strip them
	lines := strings.Split(string(raw), "\n")
	var cleaned []string
	for _, line := range lines {
		if len(line) >= 8 && (line[0] == 1 || line[0] == 2) {
			line = line[8:]
		}
		cleaned = append(cleaned, line)
	}

	return strings.Join(cleaned, "\n"), nil
}
