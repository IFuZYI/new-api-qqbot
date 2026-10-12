package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMainFailsClosedOnEnvReadFailure(t *testing.T) {
	if os.Getenv("QQBOT_TEST_ENV_CHILD") == "1" {
		flag.CommandLine = flag.NewFlagSet("qqbot", flag.ExitOnError)
		os.Args = []string{"qqbot", "-addr", "127.0.0.1:0", "-data", os.Getenv("QQBOT_TEST_DATA")}
		main()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainFailsClosedOnEnvReadFailure$")
	cmd.Env = append(os.Environ(), "QQBOT_TEST_ENV_CHILD=1", "QQBOT_TEST_DATA="+t.TempDir(), "QQBOT_ENV_FILE="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("startup continued after env failure: %s", out)
	}
	if err == nil {
		t.Fatal("startup accepted unreadable env")
	}
	if strings.Contains(string(out), "qqbot listening") {
		t.Fatalf("server exposed before env accepted: %s", out)
	}
}
