package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"time"

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
	deleteTask := flag.Bool("delete-task", false, "Delete the task after stopping it")
	deleteContainer := flag.Bool("delete-container", false, "Delete the container")
	containerId := flag.String("container", "", "Container ID to delete")
	taskId := flag.String("task", "", "Task ID to stop")
	image := flag.String("image", "", "Image to pull and run")
	listContainers := flag.Bool("list-containers", false, "List all containers")
	run := flag.Bool("run", false, "Run the task after creating it")
	flag.Parse()
	ctx := namespaces.WithNamespace(context.Background(), "default")

	client, err := containerd.New("/run/containerd/containerd.sock")
	if err != nil {
		return
	}
	// Run - this executes most lifecycle events for container and task creation
	if *run {
		// If an image isn't provided then fail immediately
		if *image == "" {
			zap.L().Error("No image provided to run. Use the --image flag to specify an image to pull and run.")
			return
		}
		defer client.Close()
		containerdVersion, err := client.Version(context.Background())
		if err != nil {
			return
		}
		zap.L().Info("Connected to containerd", zap.String("version", containerdVersion.Version), zap.String("revision", containerdVersion.Revision), zap.String("socket", "/run/containerd/containerd.sock"))
		// Pull an image
		zap.L().Info("Pulling image..")
		image, err := client.Pull(ctx, *image, containerd.WithPullUnpack)
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
		// First variable is the exit status
		task.Wait(ctx)
		// call start on the task to execute the redis server
		if err := task.Start(ctx); err != nil {
			return
		}
		// Run the task
		zap.L().Info("Task started", zap.String("taskID", task.ID()))
	}
	// Stop a task. This does NOT delete it. This just puts it into a "Stopped" state
	// Kill = stopped
	if *stop && *taskId != "" {
		zap.L().Info("Stopping task", zap.String("taskId", *taskId))

		clientTask := client.TaskService()
		_, err := clientTask.Kill(ctx, &tasks.KillRequest{ContainerID: string(*taskId), Signal: 15})
		if err != nil {
			// If the task is already stopped, this will return an error
			// Check if this is in a `STOPPED` state
			c, err := clientTask.Get(ctx, &tasks.GetRequest{ContainerID: string(*taskId)})
			if err != nil {
				zap.L().Error("Failed to get task", zap.Error(err))
				return
			}
			// If the task is already stopped, return out of this block
			// This is pretty much a no-op
			if c.Process.Status.String() == "STOPPED" {
				zap.L().Info("Task is already stopped", zap.String("taskId", *taskId))
				return
			}
			zap.L().Error("Failed to stop task", zap.Error(err))
			return
		}

		zap.L().Info("Task stopped", zap.String("taskId", *taskId))
	}
	// Delete a task (after stopping it)
	if *deleteTask && *taskId != "" {
		zap.L().Info("Deleting task", zap.String("taskId", *taskId))
		clientTask := client.TaskService()
		// Check if this is in a `STOPPED` state
		c, err := clientTask.Get(ctx, &tasks.GetRequest{ContainerID: string(*taskId)})
		if err != nil {
			zap.L().Error("Failed to get task", zap.Error(err))
			return
		}
		if c.Process.Status.String() == "STOPPED" {
			zap.L().Info("Task is already in a stopped state, deleting..", zap.String("taskId", *taskId))
			_, err := clientTask.Delete(ctx, &tasks.DeleteTaskRequest{ContainerID: string(*taskId)})
			if err != nil {
				zap.L().Error("Failed to delete task", zap.Error(err))
				return
			}
			zap.L().Info("Deleted task", zap.String("taskId", *taskId))
			return
		} else {
			zap.L().Error("Task is not stopped, cannot delete. Stopping task before deletion.", zap.String("taskId", *taskId), zap.String("status", c.Process.Status.String()))
			// Stop the task before deleting it
			_, err := clientTask.Kill(ctx, &tasks.KillRequest{ContainerID: string(*taskId), Signal: 15})
			if err != nil {
				zap.L().Error("Failed to stop task before deletion", zap.Error(err))
				return
			}
			// The below loop interates every .5 seconds for 30 seconds to poll for task deletion
			// A task may not immediately stop after task.Delete() is called
			timeout := time.After(30 * time.Second)
			ticker := time.Tick(500 * time.Millisecond)

			for {
				// Wait for the task to be stopped
				c, err := clientTask.Get(ctx, &tasks.GetRequest{ContainerID: string(*taskId)})
				if err != nil {
					zap.L().Error("Failed to get task", zap.Error(err))
					return
				}

				select {
				case <-timeout:
					zap.L().Error("Timeout of 30 seconds was hit waiting on task to be stopped before deletion. Unable to stop task.", zap.String("taskId", *taskId))
					return
				case <-ticker:
					if c.Process.Status.String() == "STOPPED" {
						zap.L().Info("Task successfully stopped before deletion", zap.String("taskId", *taskId))
						d, err := clientTask.Delete(ctx, &tasks.DeleteTaskRequest{ContainerID: string(*taskId)})

						if err != nil {
							zap.L().Error("Failed to delete task after stopping it", zap.Error(err))
							return
						}
						// Log out the exit code from the task
						zap.L().Info("Task deletion response", zap.String("taskId", *taskId), zap.Int32("exitStatus", int32(d.ExitStatus)))
						// Check if there was an exit code. If so, the task was successfully deleted
						if int32(d.ExitStatus) >= 0 {
							zap.L().Info("Deleted task", zap.String("taskId", *taskId))
							return
						}
					}
					zap.L().Info("Polling status of the task to ensure it's stopped before deletion..", zap.String("taskId", *taskId))
				}
			}
		}
	}
	// Delete a container
	if *deleteContainer && *containerId != "" {
		zap.L().Info("Deleting container", zap.String("container", *containerId))
		container, err := client.LoadContainer(ctx, *containerId)
		if err != nil {
			zap.L().Error("Failed to load container", zap.Error(err))
			return
		}
		if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
			zap.L().Error("Failed to delete container", zap.Error(err))
			return
		}
		zap.L().Info("Deleted container", zap.String("container", *containerId))
	}
	// List all containers
	if *listContainers {
		zap.L().Info("Listing all containers")
		containers, err := client.Containers(ctx)
		if err != nil {
			zap.L().Error("Failed to list containers", zap.Error(err))
			return
		}
		for _, c := range containers {
			// Get the image for the container
			image, err := c.Image(ctx)
			if err != nil {
				zap.L().Error("Failed to get image for container", zap.Error(err))
				continue
			}
			// Get the container info
			// This is being used to pull out the container runtime information - eg. io.containerd.runc.v2
			info, err := c.Info(ctx)
			if err != nil {
				zap.L().Error("Failed to get container info for container", zap.Error(err))
				continue
			}
			zap.L().Info("Container", zap.String("id", c.ID()), zap.String("image", image.Name()), zap.String("runtime", info.Runtime.Name))
		}
	}
}
