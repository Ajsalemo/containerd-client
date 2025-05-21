package main

import (
	"context"

	containerd "github.com/containerd/containerd/v2/client"
	zap "go.uber.org/zap"
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

	containerdVersion, err := client.Version(context.Background())
	if err != nil {
		return err
	}
	zap.L().Info("Connected to containerd", zap.String("version", containerdVersion.Version), zap.String("revision", containerdVersion.Revision), zap.String("socket", "/run/containerd/containerd.sock"))

	return nil
}
