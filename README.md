# containerd-client
A command line application managing containers with the containerd client

This application uses the containerd [go-client SDK](https://github.com/containerd/containerd/blob/main/docs/getting-started.md) to interface with typical containerd (`ctr`) commands such as:
- Run a container. Provide an image (either public or authentication) - optionally enable port mapping for host port to container port mapping. This uses the [go-cni](https://github.com/containerd/go-cni) library to help set up container networking
- Delete a container
- Stop a task
- Delete a task
- Tail logs from a running task
- List containers
