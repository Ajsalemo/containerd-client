package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/namespaces"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/oci"
	gocni "github.com/containerd/go-cni"
	"github.com/containernetworking/cni/pkg/version"
	uuid "github.com/google/uuid"
	zap "go.uber.org/zap"
)

func init() {
	zap.ReplaceGlobals(zap.Must(zap.NewProduction()))
}

func initGoCni() (gocni.CNI, error) {
	// Initialize gocni
	cni, err := gocni.New(
		// one for loopback network interface
		gocni.WithMinNetworkCount(2),
		gocni.WithPluginConfDir("/etc/cni/net.d"),
		gocni.WithConfListFile("/etc/cni/net.d/10-net.conflist"),
		gocni.WithPluginDir([]string{"/opt/cni/bin"}),
		gocni.WithInterfacePrefix("eth"),
	)
	if err != nil {
		zap.L().Error("Failed to initialize CNI", zap.Error(err))
	}
	// Load the CNI configuration
	if err := cni.Load(gocni.WithLoNetwork, gocni.WithDefaultConf); err != nil {
		zap.L().Error("Failed to load CNI configuration", zap.Error(err))
	}

	return cni, err
}

//	TODO - need force kill tasks if they haven't responded after 30 seconds
//
// Delete a containerd task
// This will:
// 1. Check if the task is already in a STOPPED state. If it is, then delete the task
// 2. If it's not STOPPED, call stop on the task
// 3. Since some tasks (eg. processes) may not respond to SIGTERM right away, poll the task status every 500ms for 30 seconds
// 4. If it hasn't stoppedd after 30 seconds, return an error
// 5. Else, the task has been stopped and then call delete on it
func deleteContainerdTask(ctx context.Context, client *containerd.Client, taskId string) {
	zap.L().Info("Deleting task", zap.String("taskId", taskId))
	clientTask := client.TaskService()
	// Check if this is in a `STOPPED` state
	c, err := clientTask.Get(ctx, &tasks.GetRequest{ContainerID: string(taskId)})
	if err != nil {
		zap.L().Error("Failed to get task", zap.Error(err))
		return
	}
	if c.Process.Status.String() == "STOPPED" {
		zap.L().Info("Task is already in a stopped state, deleting..", zap.String("taskId", taskId))
		_, err := clientTask.Delete(ctx, &tasks.DeleteTaskRequest{ContainerID: string(taskId)})
		if err != nil {
			zap.L().Error("Failed to delete task", zap.Error(err))
			return
		}
		zap.L().Info("Deleted task", zap.String("taskId", taskId))
		return
	} else {
		zap.L().Error("Task is not stopped, cannot delete. Stopping task before deletion.", zap.String("taskId", taskId), zap.String("status", c.Process.Status.String()))
		// Stop the task before deleting it
		_, err := clientTask.Kill(ctx, &tasks.KillRequest{ContainerID: string(taskId), Signal: 15})
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
			c, err := clientTask.Get(ctx, &tasks.GetRequest{ContainerID: string(taskId)})
			if err != nil {
				zap.L().Error("Failed to get task", zap.Error(err))
				return
			}

			select {
			case <-timeout:
				zap.L().Error("Timeout of 30 seconds was hit waiting on task to be stopped before deletion. Unable to stop task.", zap.String("taskId", taskId))
				zap.L().Error("Force killing task with SIGKILL", zap.String("taskId", taskId))
				// Kill the task with SIGKILL (signal 9) - https://www.man7.org/linux/man-pages/man7/signal.7.html
				_, err := clientTask.Kill(ctx, &tasks.KillRequest{ContainerID: string(taskId), Signal: 9})
				if err != nil {
					zap.L().Error("Failed to force kill task", zap.Error(err))
					return
				}
				zap.L().Info("Force killed task", zap.String("taskId", taskId))
				// Wait 1 second prior to running delete to make sure the task `STATUS` has changed to `STOPPED`
				zap.L().Info("Waiting 1 second before deleting task after force kill", zap.String("taskId", taskId))
				time.Sleep(1 * time.Second)
				// Delete the task after killing it
				d, err := clientTask.Delete(ctx, &tasks.DeleteTaskRequest{ContainerID: string(taskId)})
				if err != nil {
					zap.L().Error("Failed to delete task after force killing it", zap.Error(err))
					return
				}
				// Log out the exit code from the task
				zap.L().Info("Task deletion response", zap.String("taskId", taskId), zap.Int32("exitStatus", int32(d.ExitStatus)))
				// Check if there was an exit code. If so, the task was successfully deleted
				if int32(d.ExitStatus) >= 0 {
					zap.L().Info("Deleted task", zap.String("taskId", taskId))
					return
				}

				return
			case <-ticker:
				if c.Process.Status.String() == "STOPPED" {
					zap.L().Info("Task successfully stopped before deletion", zap.String("taskId", taskId))
					d, err := clientTask.Delete(ctx, &tasks.DeleteTaskRequest{ContainerID: string(taskId)})

					if err != nil {
						zap.L().Error("Failed to delete task after stopping it", zap.Error(err))
						return
					}
					// Log out the exit code from the task
					zap.L().Info("Task deletion response", zap.String("taskId", taskId), zap.Int32("exitStatus", int32(d.ExitStatus)))
					// Check if there was an exit code. If so, the task was successfully deleted
					if int32(d.ExitStatus) >= 0 {
						zap.L().Info("Deleted task", zap.String("taskId", taskId))
						return
					}
				}
				zap.L().Info("Polling status of the task to ensure it's stopped before deletion..", zap.String("taskId", taskId))
			}
		}
	}
}

func main() {
	// Parse command line flags
	stop := flag.Bool("stop", false, "Stop the running task")
	deleteTask := flag.Bool("delete-task", false, "Delete the task after stopping it")
	deleteContainer := flag.Bool("delete-container", false, "Delete the container")
	containerId := flag.String("container", "", "Container ID to delete")
	taskId := flag.String("task", "", "Task ID to stop")
	containerImage := flag.String("image", "", "Image to pull and run")
	listContainers := flag.Bool("list-containers", false, "List all containers")
	run := flag.Bool("run", false, "Run the task after creating it")
	tail := flag.Bool("tail", false, "Tail the logs of the task")
	hostPort := flag.String("host-port", "", "Port to map from the host to the container")
	containerPort := flag.String("container-port", "", "Container port - the port the container will listen on")
	portMap := flag.Bool("port-map", false, "Enable port mapping from a host port to a container port")
	registryUsername := flag.String("registry-username", "", "Username for the container registry")
	registryPassword := flag.String("registry-password", "", "Password for the container registry")
	flag.Parse()
	ctx := namespaces.WithNamespace(context.Background(), "default")
	// Set up port mapping to an empty struct here
	// If a user wants to use portmapping - user defined args will be added to this struct and passed in as a capability later on
	// Else, it'll remain empty - which is okay
	var portMapping []gocni.PortMapping
	client, err := containerd.New("/run/containerd/containerd.sock")
	if err != nil {
		zap.L().Error("Failed to connect to containerd", zap.Error(err))
		return
	}

	zap.L().Info("Connected to containerd", zap.String("socket", "/run/containerd/containerd.sock"))
	// Check the current CNI version
	v := version.Current()
	zap.L().Info("CNI Version", zap.String("version", v))

	// Run - this executes most lifecycle events for container and task creation
	if *run {
		// If an image isn't provided then fail immediately
		if *containerImage == "" {
			zap.L().Error("No image provided to run. Use the --image flag to specify an image to pull and run.")
			return
		}
		// Return an error is --host or --container port is provided without the --port-map flag
		if (*hostPort != "" || *containerPort != "") && !*portMap {
			zap.L().Error("Cannot use --host-port or --container-port without the --port-map flag. Use the --port-map flag to enable port mapping from a host port to a container port.")
			return
		}
		// Return an error for any other flags provided
		if *stop || *deleteTask || *deleteContainer || *listContainers || *tail {
			zap.L().Error("Cannot run a task with the --stop, --delete-task, --delete-container, --list-containers or --tail flags. Use the --run flag to run a task and provide an image with --image.")
			return
		}
		defer client.Close()
		containerdVersion, err := client.Version(context.Background())
		if err != nil {
			zap.L().Error("Failed to get containerd version", zap.Error(err))
			return
		}
		zap.L().Info("Connected to containerd", zap.String("version", containerdVersion.Version), zap.String("revision", containerdVersion.Revision), zap.String("socket", "/run/containerd/containerd.sock"))
		// Image is defined up here to be used if a public or private registry is set
		var image containerd.Image
		// Pull an authenticated image from a private registry
		if *registryUsername != "" && *registryPassword != "" {
			zap.L().Info("Pulling image from private registry", zap.String("image", *containerImage))
			// Resolver is used to authenticate pulls through  private registries
			resolver := docker.NewResolver(docker.ResolverOptions{
				Credentials: func(host string) (string, string, error) {
					return *registryUsername, *registryPassword, nil
				},
			})
			// Note to self - the containerd.WithResolver() for v2 clients expects github.com/containerd/containerd/v2/core/remotes/docker
			// There will be a type mismatch if you try to use github.com/containerd/containerd/remotes/docker
			image, err = client.Pull(ctx, *containerImage, containerd.WithPullUnpack, containerd.WithResolver(resolver))
			if err != nil {
				zap.L().Error("Failed to pull image", zap.Error(err))
				return
			}
		} else {
			// Pull an image from public registry
			zap.L().Info("Pulling image from public registry", zap.String("image", *containerImage))
			image, err = client.Pull(ctx, *containerImage, containerd.WithPullUnpack)
			if err != nil {
				zap.L().Error("Failed to pull image", zap.Error(err))
				return
			}
		}

		imageSize, err := image.Size(ctx)
		if err != nil {
			zap.L().Error("Failed to get image size", zap.Error(err))
			return
		}
		zap.L().Info("Pulled image", zap.String("name", image.Name()), zap.String("digest", image.Target().Digest.String()), zap.String("size", strconv.FormatInt(imageSize, 10)), zap.String("mediaType", image.Target().MediaType))
		// Create a container
		u := uuid.New()
		containerName := fmt.Sprintf("container-%s", u.String())
		// Setup port mapping capability if its enabled
		// If iptable rules pile up and arent eventually cleaned up - this may cause weird routing problems
		// Notably with portmapping failing to map from localhost to the container port. we'll get a 'connection refused' error
		// Even though the container ip on the cni0 bridge is reachable
		if *portMap {
			zap.L().Info("Port mapping enabled", zap.String("hostPort", *hostPort), zap.String("containerPort", *containerPort))
			// Check if hostPort is not provided
			if *hostPort == "" {
				zap.L().Error("No host port provided. Use the --hostPort flag to specify a host port to map to the container port.")
				return
			}
			// Check if containerPort is not provided
			if *containerPort == "" {
				zap.L().Error("No container port provided. Use the --containerPort flag to specify a container port to map from the host port.")
				return
			}
			// Convert hostPort (type str) into an integer
			hostPort, err := strconv.ParseInt(*hostPort, 10, 32)
			if err != nil {
				zap.L().Error("Invalid host port provided to map. Use the --host-port flag to specify a valid host port to map to the container port.")
				return
			}
			// Convert containerPort (type str) into an integer
			containerPort, err := strconv.ParseInt(*containerPort, 10, 32)
			if err != nil {
				zap.L().Error("Invalid container port provided to map. Use the --container-port flag to specify a valid container port to map from the host port.")
				return
			}

			zap.L().Info("Using port mapping", zap.Int32("hostPort", int32(hostPort)), zap.Int32("containerPort", int32(containerPort)))
			// Set up user defined port mapping
			portMapping = []gocni.PortMapping{
				{
					HostPort:      int32(hostPort),
					ContainerPort: int32(containerPort),
					Protocol:      "tcp",
					HostIP:        "0.0.0.0",
				},
			}
		}

		zap.L().Info("Creating container " + containerName + " with snapshot " + fmt.Sprintf("snapshot-%s", u.String()))
		container, err := client.NewContainer(
			ctx,
			containerName,
			containerd.WithNewSnapshot(fmt.Sprintf("snapshot-%s", u.String()), image),
			containerd.WithNewSpec(
				oci.WithImageConfig(image),
			),
		)
		if err != nil {
			zap.L().Error("Failed to create container", zap.Error(err))
			return
		}

		defer container.Delete(ctx, containerd.WithSnapshotCleanup)

		zap.L().Info("Created container " + containerName + " with container ID " + container.ID())
		// Create a task from the container
		zap.L().Info("Creating task for container", zap.String("containerID", container.ID()))
		task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
		if err != nil {
			zap.L().Error("Failed to create task", zap.Error(err))
			return
		}
		defer task.Delete(ctx)
		zap.L().Info("Created task for container", zap.String("containerID", container.ID()), zap.String("taskID", task.ID()))
		// First variable is the exit status
		task.Wait(ctx)
		// call start on the task to execute the redis server
		if err := task.Start(ctx); err != nil {
			zap.L().Error("Failed to start task", zap.Error(err))
			return
		}
		// Run the task
		zap.L().Info("Task started", zap.String("taskID", task.ID()))
		// Construct a path to the network namespace for the pid
		netNsPath := fmt.Sprintf("/proc/%d/ns/net", task.Pid())

		zap.L().Info("Network namespace path", zap.String("netNsPath", netNsPath))
		// Initialize gocni so we have a reference to it
		// New() needs to be called, else this will be a nil dereference
		cni, err := initGoCni()
		if err != nil {
			zap.L().Error("Failed to initialize CNI", zap.Error(err))
			return
		}
		// Setup networking for the container
		// Pass portmapping capabilities in if it's defined to be used by the user
		result, err := cni.Setup(ctx, containerName, netNsPath, gocni.WithCapabilityPortMap(portMapping))
		if err != nil {
			zap.L().Error("Failed to setup CNI network", zap.Error(err))
			return
		}
		// There seems to be a problem where cni0 isn't setup with an ipv4 address
		// This can be seen with `ifconfig`. Not sure if this is a problem with how I'm setting up the cni bridge or not
		// To be safe, add the subnet to the cni0 bridge - 10-net.conflist still handles everything else
		bridgeSetupCmd := exec.Command("ip", "addr", "show", "cni0")
		if output, err := bridgeSetupCmd.Output(); err == nil {
			if !strings.Contains(string(output), "10.10.0.1/16") {
				zap.L().Info("Adding gateway IP to cni0 bridge")
				addIPCmd := exec.Command("ip", "addr", "add", "10.10.0.1/16", "dev", "cni0")
				if err := addIPCmd.Run(); err != nil {
					zap.L().Warn("Failed to add IP to cni0 bridge (may already exist)", zap.Error(err))
				}
			}

			zap.L().Info("cni0 bridge already has gateway IP of 10.10.0.1/16, skipping 'ip address add 10.10.0.1/16 dev cni0'")
		}
		zap.L().Info("CNI network setup for container", zap.String("containerName", containerName), zap.String("networkNamespace", netNsPath))
		zap.L().Info(result.Interfaces["eth0"].IPConfigs[0].IP.String())
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
		deleteContainerdTask(ctx, client, *taskId)
	}
	// Delete a container
	if *deleteContainer && *containerId != "" {
		zap.L().Info("Deleting container", zap.String("container", *containerId))
		container, err := client.LoadContainer(ctx, *containerId)
		if err != nil {
			zap.L().Error("Failed to load container", zap.Error(err))
			return
		}
		// Get the PID of the task running for this container
		cTaskToDelete, err := container.Task(ctx, nil)
		if err != nil {
			// There may be a case where there is no task associated with the container (eg. deleted already)
			// If so, skip to deleting the container
			if strings.Contains(err.Error(), "no running task found") {
				zap.L().Warn("No running task found for container, skipping task deletion and deleting the container", zap.String("containerId", *containerId))
				// Delete the container
				if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
					zap.L().Error("Failed to delete container", zap.Error(err))
					return
				}
				zap.L().Info("Deleted container", zap.String("container", *containerId))
			} else {
				zap.L().Error("Failed to get task for container", zap.Error(err))
				return
			}
		} else {
			// Get the PID of the task running for this container
			pid := cTaskToDelete.Pid()
			// Construct a path to the network namespace for the pid
			// This is the pid associated with the task of the container we want to delete - and is what network ns the container is created in if defined
			ns := fmt.Sprintf("/proc/%d/ns/net", pid)
			zap.L().Info("Network namespace path for container", zap.String("ns", ns))
			// Initialize gocni so we have a reference to it
			// New() needs to be called, else this will be a nil dereference
			cni, err := initGoCni()
			if err != nil {
				zap.L().Error("Failed to initialize CNI", zap.Error(err))
				return
			}
			// Delete the task before deleting the container - this also stops the task prior to task deletion
			// Stopping the task is required before calling container delete or else container deletion will fail
			deleteContainerdTask(ctx, client, *containerId)
			// Delete the container
			if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
				zap.L().Error("Failed to delete container", zap.Error(err))
				return
			}
			zap.L().Info("Deleted container", zap.String("container", *containerId))
			// After container deletion remove the CNI network
			// Delete the cni0 network this container was attached to when the container is deleted
			if err := cni.Remove(ctx, container.ID(), ns); err != nil {
				zap.L().Error("Failed to remove CNI network for container", zap.Error(err))
			}

			zap.L().Info("Removed CNI network for container", zap.String("containerID", container.ID()), zap.String("networkNamespace", ns))
		}
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
	// Attach to a container to view logs
	if *tail && *containerId != "" {
		container, err := client.LoadContainer(ctx, *containerId)
		if err != nil {
			zap.L().Error("Failed to load container", zap.Error(err))
			return
		}

		task, err := container.Task(ctx, cio.NewAttach(cio.WithStdio))
		if err != nil {
			zap.L().Error("Failed to load task", zap.Error(err))
			return
		}

		zap.L().Info("Attaching to task", zap.String("taskId", task.ID()), zap.String("containerId", container.ID()))
		// At this point, logs from the container should be streaming to stdout
		// Block using select {} until the user does an interrupt - CTRL + C / SIGTERM / etc.
		// Notify the application of the below signals to be handled on shutdown
		s := make(chan os.Signal, 1)
		signal.Notify(s,
			syscall.SIGINT,
			syscall.SIGTERM,
			syscall.SIGQUIT)
		// Goroutine to clean up prior to shutting down
		go func() {
			sig := <-s
			switch sig {
			case os.Interrupt:
				zap.L().Warn("CTRL+C / os.Interrupt recieved, shutting log streaming..")
				os.Exit(0)
			case syscall.SIGTERM:
				zap.L().Warn("SIGTERM recieved.., shutting log streaming..")
				os.Exit(0)
			case syscall.SIGQUIT:
				zap.L().Warn("SIGQUIT recieved.., shutting log streaming..")
				os.Exit(0)
			case syscall.SIGINT:
				zap.L().Warn("SIGINT recieved.., shutting log streaming..")
				os.Exit(0)
			}
		}()
		select {}
	}
}
