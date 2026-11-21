// Command wrench-login signs a user in to a Wrench stack through its GitLab mock and prints a device key, for
// scripts such as the backup drill.
//
//	wrench-login -url http://127.0.0.1:18085 -gitlab http://127.0.0.1:19085 -email user1@example.com
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/ordo-systems/wrench/tests/login"
)

func main() {
	wrench := flag.String("url", "", "Wrench base URL")
	gitlab := flag.String("gitlab", "", "GitLab mock base URL")
	email := flag.String("email", "user1@example.com", "allowlisted email to sign in as")
	flag.Parse()
	if *wrench == "" || *gitlab == "" {
		fmt.Fprintln(os.Stderr, "wrench-login: set -url and -gitlab")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	key, err := login.DeviceKey(ctx, *wrench, *gitlab, *email)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wrench-login:", err)
		os.Exit(1)
	}
	fmt.Println(key)
}
