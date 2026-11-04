package service

import (
	"context"
	"fmt"
	"os"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
)

type serviceConfig struct {
	Network string
}

func loadConfig() serviceConfig {
	network := os.Getenv("DBTEST_DOCKER_NETWORK")
	if network == "" {
		network = "dbtest-net"
	}
	return serviceConfig{Network: network}
}

// ensureNetwork creates networkName if it does not exist.
func ensureNetwork(ctx context.Context, docker *dockerclient.Client, networkName string) error {
	_, err := docker.NetworkInspect(ctx, networkName, network.InspectOptions{})
	if err == nil {
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect network %s: %w", networkName, err)
	}
	if _, err := docker.NetworkCreate(ctx, networkName, network.CreateOptions{}); err != nil && !errdefs.IsConflict(err) {
		return fmt.Errorf("create network %s: %w", networkName, err)
	}
	return nil
}

// joinNetwork connects the named container to networkName,
// create the network if it is missing.
func joinNetwork(ctx context.Context, docker *dockerclient.Client, networkName, containerName string, joined map[string]*network.EndpointSettings) error {
	if _, found := joined[networkName]; found {
		return nil
	}
	if err := ensureNetwork(ctx, docker, networkName); err != nil {
		return err
	}
	if err := docker.NetworkConnect(ctx, networkName, containerName, nil); err != nil {
		return fmt.Errorf("connect %s to %s: %w", containerName, networkName, err)
	}
	return nil
}
