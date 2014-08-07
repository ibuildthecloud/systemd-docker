package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	dockerClient "github.com/fsouza/go-dockerclient"
)

var (
	SYSFS       string = "/sys/fs/cgroup"
	PROCS       string = "cgroup.procs"
	CGROUP_PROC string = "/proc/%d/cgroup"
)

type Context struct {
	Args          []string
	Namespaces    []string
	AllNamespaces bool
	Logs          bool
	Env           bool
	Id            string
	NotifySocket  string
	Cmd           *exec.Cmd
	Pid           int
	Client        *dockerClient.Client
}

func parseContext(args []string) (*Context, error) {
	c := &Context{
		Logs:          true,
		AllNamespaces: true,
	}

	/* Probably could have done this easily with flags */
	foundRun := false
	foundD := false

	for _, val := range args {
		if foundRun {
			if val == "-d" {
				foundD = true
			}
			c.Args = append(c.Args, val)
		} else {
			switch val {
			case "--env":
				c.Env = true
			case "--no-logs":
				c.Logs = false
			case "run":
				foundRun = true
			}
		}
	}

	if !foundRun {
		return nil, errors.New("run not found in arguments")
	}

	if !foundD {
		return nil, errors.New("-d is required")
	}

	c.NotifySocket = os.Getenv("NOTIFY_SOCKET")

	return c, nil
}

func runContainer(c *Context) error {
	args := append([]string{"run"}, c.Args...)
	c.Cmd = exec.Command("docker", args...)

	errorPipe, err := c.Cmd.StderrPipe()
	if err != nil {
		return err
	}

	outputPipe, err := c.Cmd.StdoutPipe()
	if err != nil {
		return err
	}

	err = c.Cmd.Start()
	if err != nil {
		return err
	}

	go io.Copy(os.Stderr, errorPipe)

	bytes, err := ioutil.ReadAll(outputPipe)
	if err != nil {
		return err
	}

	c.Id = strings.TrimSpace(string(bytes))

	err = c.Cmd.Wait()
	if err != nil {
		return err
	}

	if !c.Cmd.ProcessState.Success() {
		return err
	}

	c.Pid, err = getContainerPid(c)

	return err
}

func getClient(c *Context) (*dockerClient.Client, error) {
	if c.Client != nil {
		return c.Client, nil
	}

	endpoint := os.Getenv("DOCKER_HOST")
	if len(endpoint) == 0 {
		endpoint = "unix:///var/run/docker.sock"
	}

	return dockerClient.NewVersionedClient(endpoint, "1.11")
}

func getContainerPid(c *Context) (int, error) {
	client, err := getClient(c)
	if err != nil {
		return 0, err
	}

	container, err := client.InspectContainer(c.Id)
	if err != nil {
		return 0, err
	}

	if container == nil {
		return 0, errors.New(fmt.Sprintf("Failed to find container %s", c.Id))
	}

	if container.State.Pid <= 0 {
		return 0, errors.New(fmt.Sprintf("Pid is %d for container %s", container.State.Pid, c.Id))
	}

	return container.State.Pid, nil
}

func getNamespacesForPid(pid int) (map[string]string, error) {
	file, err := os.Open(fmt.Sprintf(CGROUP_PROC, pid))
	if err != nil {
		return nil, err
	}

	ret := map[string]string{}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.SplitN(scanner.Text(), ":", 3)
		if len(line) != 3 {
			continue
		}

		ret[line[1]] = line[2]
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return ret, nil
}

func constructCgroupPath(cgroupName string, cgroupPath string) string {
	return path.Join(SYSFS, strings.TrimPrefix(cgroupName, "name="), cgroupPath, PROCS)
}

func getCgroupPids(cgroupName string, cgroupPath string) ([]string, error) {
	ret := []string{}

	file, err := os.Open(constructCgroupPath(cgroupName, cgroupPath))
	if err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		ret = append(ret, strings.TrimSpace(scanner.Text()))
	}

	if err = scanner.Err(); err != nil {
		return nil, err
	}

	return ret, nil
}

func writePid(pid string, path string) error {
	return ioutil.WriteFile(path, []byte(pid), 0644)
}

func moveNamespaces(c *Context) (bool, error) {
	moved := false
	currentCgroups, err := getNamespacesForPid(os.Getpid())
	if err != nil {
		return false, err
	}

	containerCgroups, err := getNamespacesForPid(c.Pid)
	if err != nil {
		return false, err
	}

	var ns []string

	if c.AllNamespaces || c.Namespaces == nil {
		ns = make([]string, len(containerCgroups))
		for value, _ := range containerCgroups {
			ns = append(ns, value)
		}
	} else {
		ns = c.Namespaces
	}

	for _, nsName := range ns {
		currentPath, ok := currentCgroups[nsName]
		if !ok {
			continue
		}

		containerPath, ok := containerCgroups[nsName]
		if !ok {
			continue
		}

		if currentPath == containerPath || containerPath == "/" {
			continue
		}

		pids, err := getCgroupPids(nsName, containerPath)
		if err != nil {
			return false, err
		}

		for _, pid := range pids {
			pidInt, err := strconv.Atoi(pid)
			if err != nil {
				continue
			}

			if pidDied(pidInt) {
				continue
			}

			currentFullPath := constructCgroupPath(nsName, currentPath)
			log.Printf("Moving pid %s to %s\n", pid, currentFullPath)
			err = writePid(pid, currentFullPath)
			if err != nil {
				return false, err
			}

			moved = true
		}
	}

	return moved, nil
}

func pidDied(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return os.IsNotExist(err)
}

func notify(c *Context) error {
	if pidDied(c.Pid) {
		return errors.New("Container exited before we could notify systemd")
	}

	if len(c.NotifySocket) == 0 {
		return nil
	}

	conn, err := net.Dial("unixgram", c.NotifySocket)
	if err != nil {
		return err
	}

	defer conn.Close()

	_, err = conn.Write([]byte(fmt.Sprintf("MAINPID=%d", c.Pid)))
	if err != nil {
		return err
	}

	if pidDied(c.Pid) {
		conn.Write([]byte(fmt.Sprintf("MAINPID=%d", os.Getpid())))
		return errors.New("Container exited before we could notify systemd")
	}

	_, err = conn.Write([]byte("READY=1"))
	if err != nil {
		return err
	}

	return nil
}

func pipeLogs(c *Context) error {
	if !c.Logs {
		return nil
	}

	client, err := getClient(c)
	if err != nil {
		return err
	}

	err = client.Logs(dockerClient.LogsOptions{
		Container:    c.Id,
		Follow:       true,
		Stdout:       true,
		Stderr:       true,
		OutputStream: os.Stdout,
		ErrorStream:  os.Stderr,
	})

	return err
}

func main() {
	c, err := parseContext(os.Args)
	if err != nil {
		log.Fatal(err)
	}

	err = runContainer(c)
	if err != nil {
		log.Fatal(err)
	}

	_, err = moveNamespaces(c)
	if err != nil {
		log.Fatal(err)
	}

	err = notify(c)
	if err != nil {
		log.Fatal(err)
	}

	err = pipeLogs(c)
	if err != nil {
		log.Fatal(err)
	}
}
