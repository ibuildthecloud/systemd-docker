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
	"time"

	"github.com/docker/docker/opts"
	flag "github.com/docker/docker/pkg/mflag"

	dockerClient "github.com/fsouza/go-dockerclient"
)

var (
	SYSFS       string        = "/sys/fs/cgroup"
	PROCS       string        = "cgroup.procs"
	CGROUP_PROC string        = "/proc/%d/cgroup"
	INTERVAL    time.Duration = 1000
)

type Context struct {
	Args         []string
	Cgroups      []string
	AllCgroups   bool
	Logs         bool
	Notify       bool
	Name         string
	Env          bool
	Rm           bool
	Id           string
	NotifySocket string
	Cmd          *exec.Cmd
	Pid          int
	PidFile      string
	Client       *dockerClient.Client
	Detach       bool
}

func setupEnvironment(c *Context) {
	newArgs := []string{}
	if c.Notify && len(c.NotifySocket) > 0 {
		newArgs = append(newArgs, "-e", fmt.Sprintf("NOTIFY_SOCKET=%s", c.NotifySocket))
		newArgs = append(newArgs, "-v", fmt.Sprintf("%s:%s", c.NotifySocket, c.NotifySocket))
	} else {
		c.Notify = false
	}

	if c.Env {
		for _, val := range os.Environ() {
			if !strings.HasPrefix(val, "HOME=") && !strings.HasPrefix(val, "PATH=") {
				newArgs = append(newArgs, "-e", val)
			}
		}
	}

	if len(newArgs) > 0 {
		c.Args = append(newArgs, c.Args...)
	}
}

func parseContext(args []string) (*Context, error) {
	c := &Context{
		Logs:       true,
		AllCgroups: false,
	}

	flags := flag.NewFlagSet("systemd-docker", flag.ContinueOnError)

	flCgroups := opts.NewListOpts(nil)

	flags.StringVar(&c.PidFile, []string{"p", "-pid-file"}, "", "pipe file")
	flags.BoolVar(&c.Logs, []string{"l", "-logs"}, true, "pipe logs")
	flags.BoolVar(&c.Notify, []string{"n", "-notify"}, false, "setup systemd notify for container")
	flags.BoolVar(&c.Env, []string{"e", "-env"}, false, "inherit environment variable")
	flags.Var(&flCgroups, []string{"c", "-cgroups"}, "cgroups to take ownership of or 'all' for all cgroups available")

	err := flags.Parse(args)
	if err != nil {
		return nil, err
	}

	foundD := false
	var name string

	runArgs := flags.Args()
	if len(runArgs) == 0 || runArgs[0] != "run" {
		log.Println("Args:", runArgs)
		return nil, errors.New("run not found in arguments")
	}

	runArgs = runArgs[1:]
	newArgs := make([]string, 0, len(runArgs))

	for i, arg := range runArgs {
		/* This is tedious, but flag can't ignore unknown flags and I don't want to define them all */
		add := true

		switch {
		case arg == "-rm" || arg == "--rm":
			c.Rm = true
			add = false
		case arg == "-d" || arg == "-detach" || arg == "--detach":
			foundD = true
		case strings.HasPrefix(arg, "-name") || strings.HasPrefix(arg, "--name"):
			if strings.Contains(arg, "=") {
				name = strings.SplitN(arg, "=", 2)[1]
			} else if len(runArgs) > i+1 {
				name = runArgs[i+1]
			}
		}

		if add {
			newArgs = append(newArgs, arg)
		}
	}

	if !foundD {
		newArgs = append([]string{"-d"}, newArgs...)
	}

	c.Name = name
	c.NotifySocket = os.Getenv("NOTIFY_SOCKET")
	c.Args = newArgs
	c.Cgroups = flCgroups.GetAll()
	c.Detach = foundD

	for _, val := range c.Cgroups {
		if val == "all" {
			c.Cgroups = nil
			c.AllCgroups = true
			break
		}
	}

	setupEnvironment(c)

	return c, nil
}

func lookupNamedContainer(c *Context) error {
	client, err := getClient(c)
	if err != nil {
		return err
	}

	container, err := client.InspectContainer(c.Name)
	if _, ok := err.(*dockerClient.NoSuchContainer); ok {
		return nil
	}
	if err != nil || container == nil {
		return err
	}

	if container.State.Running {
		c.Id = container.ID
		c.Pid = container.State.Pid
		return nil
	} else if c.Rm {
		return client.RemoveContainer(dockerClient.RemoveContainerOptions{
			ID:    container.ID,
			Force: true,
		})
	} else {
		client, err := getClient(c)
		err = client.StartContainer(container.ID, nil)
		if err != nil {
			return err
		}

		container, err = client.InspectContainer(c.Name)
		if err != nil {
			return err
		}

		c.Id = container.ID
		c.Pid = container.State.Pid

		return nil
	}
}

func launchContainer(c *Context) error {
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

func runContainer(c *Context) error {
	if len(c.Name) > 0 {
		err := lookupNamedContainer(c)
		if err != nil {
			return err
		}

	}

	if len(c.Id) == 0 {
		err := launchContainer(c)
		if err != nil {
			return err
		}
	}

	if c.Pid == 0 {
		return errors.New("Failed to launch container, pid is 0")
	}

	return nil
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

func getCgroupsForPid(pid int) (map[string]string, error) {
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

func moveCgroups(c *Context) (bool, error) {
	moved := false
	currentCgroups, err := getCgroupsForPid(os.Getpid())
	if err != nil {
		return false, err
	}

	containerCgroups, err := getCgroupsForPid(c.Pid)
	if err != nil {
		return false, err
	}

	var ns []string

	if c.AllCgroups || c.Cgroups == nil || len(c.Cgroups) == 0 {
		ns = make([]string, 0, len(containerCgroups))
		for value, _ := range containerCgroups {
			ns = append(ns, value)
		}
	} else {
		ns = c.Cgroups
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

	if !c.Notify {
		_, err = conn.Write([]byte("READY=1"))
		if err != nil {
			return err
		}
	}

	return nil
}

func pidFile(c *Context) error {
	if len(c.PidFile) == 0 || c.Pid <= 0 {
		return nil
	}

	err := ioutil.WriteFile(c.PidFile, []byte(strconv.Itoa(c.Pid)), 0644)
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

func keepAlive(c *Context) error {
	if c.Logs || c.Rm {
		client, err := getClient(c)
		if err != nil {
			return err
		}

		/* Good old polling... */
		for true {
			container, err := client.InspectContainer(c.Id)
			if err != nil {
				return err
			}

			if container.State.Running {
				time.Sleep(INTERVAL * time.Millisecond)
			} else {
				return nil
			}
		}
	}

	return nil
}

func rmContainer(c *Context) error {
	if !c.Rm {
		return nil
	}

	client, err := getClient(c)
	if err != nil {
		return err
	}

	return client.RemoveContainer(dockerClient.RemoveContainerOptions{
		ID:    c.Id,
		Force: true,
	})
}

func mainWithArgs(args []string) (*Context, error) {
	c, err := parseContext(args)
	if err != nil {
		return c, err
	}

	err = runContainer(c)
	if err != nil {
		return c, err
	}

	_, err = moveCgroups(c)
	if err != nil {
		return c, err
	}

	err = notify(c)
	if err != nil {
		return c, err
	}

	err = pidFile(c)
	if err != nil {
		return c, err
	}

  if !c.Detach {
    go pipeLogs(c)
  
  	err = keepAlive(c)
  	if err != nil {
  		return c, err
  	}
    
  	err = rmContainer(c)
  	if err != nil {
  		return c, err
  	}
  }

	return c, nil
}

func main() {
	_, err := mainWithArgs(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
}
