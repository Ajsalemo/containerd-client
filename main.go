package main

import (
	"context"

	"github.com/containerd/containerd/namespaces"
	containerd "github.com/containerd/containerd/v2/client"
	zap "go.uber.org/zap"
	"strconv"
)

func init() {
	zap.ReplaceGlobals(zap.Must(zap.NewProduction()))
}

func main() {
	if err := connect(); err != nil {
		zap.L().Fatal(err.Error())
	}
}

func connect() error {
	client, err := containerd.New("/run/containerd/containerd.sock")
	if err != nil {
		return err
	}
	defer client.Close()
	ctx := namespaces.WithNamespace(context.Background(), "default")
	containerdVersion, err := client.Version(context.Background())
	if err != nil {
		return err
	}
	zap.L().Info("Connected to containerd", zap.String("version", containerdVersion.Version), zap.String("revision", containerdVersion.Revision), zap.String("socket", "/run/containerd/containerd.sock"))
	// Pull an image
	if err := pullImage(client, ctx); err != nil {
		zap.L().Error("Failed to pull image", zap.Error(err))
	}
	return nil
}

func pullImage(client *containerd.Client, ctx context.Context) error {
	zap.L().Info("Pulling image..")
	image, err := client.Pull(ctx, "docker.io/library/redis:alpine", containerd.WithPullUnpack)
	if err != nil {
		return err
	}

	imageSize, err := image.Size(ctx)
	if err != nil {
		return err
	}
	zap.L().Info("Pulled image", zap.String("name", image.Name()), zap.String("digest", image.Target().Digest.String()), zap.String("size", strconv.FormatInt(imageSize, 10)), zap.String("mediaType", image.Target().MediaType))

	return nil
}
