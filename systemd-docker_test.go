package main

import (
	"log"
	"os"
	"os/exec"
	"path"
	"syscall"
	"testing"
)

func TestParseNoRun(t *testing.T) {
	_, err := parseContext([]string{"a", "b", "-d"})
	if err == nil {
		t.Fatal("parse succeeded")
	}
}

func TestParseNoD(t *testing.T) {
	_, err := parseContext([]string{"a", "run"})
	if err == nil {
		t.Fatal("parse succeeded")
	}
}

func TestParseArgs(t *testing.T) {
	c, err := parseContext([]string{"a", "run", "-d", "c", "d"})
	if err != nil {
		t.Fatal("failed to parse:", err)
	}

	if c.Args[0] != "-d" ||
		c.Args[1] != "c" ||
		c.Args[2] != "d" {
		t.Fatal("Invalid args", c.Args)
	}
}

func TestParseEnv(t *testing.T) {
	c, err := parseContext([]string{"a", "run", "--env", "-d"})
	if err != nil {
		t.Fatal("failed to parse:", err)
	}

	if c.Env {
		t.Fatal("env shouldn't be set")
	}

	c, err = parseContext([]string{"a", "--env", "run", "-d"})
	if err != nil {
		t.Fatal("failed to parse:", err)
	}

	if !c.Env {
		t.Fatal("env should be set")
	}
}

func TestParseLogs(t *testing.T) {
	c, err := parseContext([]string{"a", "run", "--no-logs", "-d"})
	if err != nil {
		t.Fatal("failed to parse:", err)
	}

	if !c.Logs {
		t.Fatal("logs should be set")
	}

	c, err = parseContext([]string{"a", "--no-logs", "run", "-d"})
	if err != nil {
		t.Fatal("failed to parse:", err)
	}

	if c.Logs {
		t.Fatal("logs shouldn't be set")
	}
}

func TestBadExec(t *testing.T) {
	c := &Context{
		Args: []string{"-bad"},
	}

	err := runContainer(c)

	if err == nil {
		t.Fatal("Exec should have failed")
		return
	}

	if e, ok := err.(*exec.ExitError); ok {
		if status, ok := e.Sys().(syscall.WaitStatus); ok {
			if status.ExitStatus() != 2 {
				log.Fatal("Expect 2 exit code got ", status.ExitStatus())
			}
		}
	} else {
		t.Fatal("Expect exec.ExitError", err)
	}
}

func TestGoodExec(t *testing.T) {
	c := &Context{
		Args: []string{"-d", "busybox", "echo", "hi"},
	}

	err := runContainer(c)

	if err != nil {
		t.Fatal("Exec should not have failed", err)
		return
	}

	if c.Cmd.ProcessState.Pid() <= 0 {
		t.Fatal("Bad pid", c.Cmd.ProcessState.Pid())
	}

	if c.Pid <= 0 {
		t.Fatal("Bad container pid", c.Pid)
	}
}

func TestParseCgroups(t *testing.T) {
	cgroups, err := getNamespacesForPid(os.Getpid())
	if err != nil {
		log.Fatal("Error:", err)
	}

	if val, ok := cgroups["cpu"]; ok {
		p := path.Join(SYSFS, "cpu", val)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			log.Fatalf("Path does not exist %s", p, err)
		}
	} else {
		log.Fatal("Failed to find cpu cgroup", val)
	}
}

func TestMoveNamespace(t *testing.T) {
	c := &Context{
		Args: []string{"-d", "busybox", "echo", "hi"},
	}

	err := runContainer(c)

	if err != nil {
		t.Fatal("Exec should not have failed", err)
		return
	}

	if c.Cmd.ProcessState.Pid() <= 0 {
		t.Fatal("Bad pid", c.Cmd.ProcessState.Pid())
	}

	if c.Pid <= 0 {
		t.Fatal("Bad container pid", c.Pid)
	}

	moved, err := moveNamespaces(c)
	if !moved || err != nil {
		t.Fatal("Failed to move namespaces ", moved, err)
	}
}
