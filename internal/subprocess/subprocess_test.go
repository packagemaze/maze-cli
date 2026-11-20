package subprocess

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets the test binary act as the command: SUBPROCESS_TEST_CHILD
// says how it behaves.
func TestMain(m *testing.M) {
	switch os.Getenv("SUBPROCESS_TEST_CHILD") {
	case "":
		os.Exit(m.Run())
	case "exit":
		fmt.Print("value=" + os.Getenv("SUBPROCESS_TEST_VALUE") + " args=" + strings.Join(os.Args[1:], ","))
		code, _ := strconv.Atoi(os.Getenv("SUBPROCESS_TEST_EXIT"))
		os.Exit(code)
	case "wait":
		fmt.Println("ready")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func childSpec(mode string, env ...string) Spec {
	return Spec{
		Path: os.Args[0],
		Env:  append(os.Environ(), append([]string{"SUBPROCESS_TEST_CHILD=" + mode}, env...)...),
	}
}

func TestRunPassesEnvironmentAndArgumentsAndReturnsTheExitStatus(t *testing.T) {
	var stdout bytes.Buffer
	spec := childSpec("exit", "SUBPROCESS_TEST_VALUE=from-env", "SUBPROCESS_TEST_EXIT=7")
	spec.Args = []string{"one", "two"}
	spec.Stdout = &stdout
	code, err := Run(spec, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit status = %d, want 7", code)
	}
	if stdout.String() != "value=from-env args=one,two" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunReportsACommandThatCannotStart(t *testing.T) {
	_, err := Run(Spec{Path: "maze-subprocess-test-no-such-command"}, nil)
	if err == nil || !strings.Contains(err.Error(), "start maze-subprocess-test-no-such-command") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunForwardsASignalAndReportsIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows delivers no signals to a child process")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	signals := make(chan os.Signal, 1)
	spec := childSpec("wait")
	spec.Stdout = writer
	result := make(chan int, 1)
	go func() {
		code, runErr := Run(spec, signals)
		if runErr != nil {
			t.Errorf("Run: %v", runErr)
		}
		result <- code
	}()
	ready := make([]byte, len("ready\n"))
	if _, err := reader.Read(ready); err != nil {
		t.Fatalf("the command did not start: %v", err)
	}
	signals <- syscall.SIGTERM
	select {
	case code := <-result:
		if code != 128+int(syscall.SIGTERM) {
			t.Fatalf("exit status = %d, want %d", code, 128+int(syscall.SIGTERM))
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the signal was not forwarded")
	}
	_ = writer.Close()
}
