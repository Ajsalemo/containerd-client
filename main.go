package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"

	"github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/namespaces"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/oci"
	uuid "github.com/google/uuid"
	zap "go.uber.org/zap"
)

func init() {
	zap.ReplaceGlobals(zap.Must(zap.NewProduction()))
}

func main() {
	// Parse command line flags
	stop := flag.Bool("stop", false, "Stop the running task")
	delete := flag.Bool("delete", false, "Delete the task after stopping it")
	taskId := flag.String("task", "", "Task ID to stop")
	run := flag.Bool("run", false, "Run the task after creating it")
	flag.Parse()
	ctx := namespaces.WithNamespace(context.Background(), "default")

	client, err := containerd.New("/run/containerd/containerd.sock")
	if err != nil {
		return
	}
	// Run - this executes most lifecycle events for container and task creation
	if *run {
		defer client.Close()
		containerdVersion, err := client.Version(context.Background())
		if err != nil {
			return
		}
		zap.L().Info("Connected to containerd", zap.String("version", containerdVersion.Version), zap.String("revision", containerdVersion.Revision), zap.String("socket", "/run/containerd/containerd.sock"))
		// Pull an image
		zap.L().Info("Pulling image..")
		image, err := client.Pull(ctx, "docker.io/library/redis:alpine", containerd.WithPullUnpack)
		if err != nil {
			return
		}

		imageSize, err := image.Size(ctx)
		if err != nil {
			return
		}
		zap.L().Info("Pulled image", zap.String("name", image.Name()), zap.String("digest", image.Target().Digest.String()), zap.String("size", strconv.FormatInt(imageSize, 10)), zap.String("mediaType", image.Target().MediaType))
		// Create a container
		u := uuid.New()
		containerName := fmt.Sprintf("container-%s", u.String())
		zap.L().Info("Creating container " + containerName + " with snapshot " + fmt.Sprintf("snapshot-%s", u.String()))
		container, err := client.NewContainer(
			ctx,
			containerName,
			containerd.WithNewSnapshot(fmt.Sprintf("snapshot-%s", u.String()), image),
			containerd.WithNewSpec(oci.WithImageConfig(image)),
		)
		if err != nil {
			return
		}
		defer container.Delete(ctx, containerd.WithSnapshotCleanup)

		zap.L().Info("Created container " + containerName + " with container ID " + container.ID())
		// Create a task from the container
		zap.L().Info("Creating task for container", zap.String("containerID", container.ID()))
		task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
		if err != nil {
			return
		}
		defer task.Delete(ctx)
		zap.L().Info("Created task for container", zap.String("containerID", container.ID()), zap.String("taskID", task.ID()))
		// Run the task
		zap.L().Info("Task started", zap.String("taskID", task.ID()))
		// First variable is the exit status
		task.Wait(ctx)
		// call start on the task to execute the redis server
		if err := task.Start(ctx); err != nil {
			return
		}
		zap.L().Info("Task started", zap.String("taskID", task.ID()))
	}
	// Stop a task.  This does NOT delete it. This just puts it into a "Stopped" state
	if *stop && *taskId != "" {
		zap.L().Info("Stopping task", zap.String("taskId", *taskId))

		clientTask := client.TaskService()
		_, err := clientTask.Kill(ctx, &tasks.KillRequest{ContainerID: string(*taskId), Signal: 15})
		if err != nil {
			zap.L().Error("Failed to kill task", zap.Error(err))
			return
		}
		zap.L().Info("Killed task", zap.String("taskId", *taskId))
	}
	// Delete a task (after stopping it)
	if *delete && *taskId != "" {
		zap.L().Info("Deleting task", zap.String("taskId", *taskId))

		clientTask := client.TaskService()
		_, err := clientTask.Delete(ctx, &tasks.DeleteTaskRequest{ContainerID: string(*taskId)})
		if err != nil {
			zap.L().Error("Failed to delete task", zap.Error(err))
			return
		}
		zap.L().Info("Deleted task", zap.String("taskId", *taskId))
	}
}
