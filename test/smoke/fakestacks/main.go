// Command fakestacks serves the fake Stacks API of this project on a port, so that the smoke
// deployment of W-21 can run it as a pod beside the controller. It is a test program: no release
// builds it.
//
// STACK_KEY names the stack that the fake answers for, and LISTEN the address, :8080 by default.
package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

func main() {
	if err := serve(); err != nil {
		log.Fatalf("fakestacks: %v", err)
	}
}

func serve() error {
	fake := fakestacks.New()
	defer fake.Close()

	if key := os.Getenv("STACK_KEY"); key != "" {
		fake.StackKey = key
	}

	address := os.Getenv("LISTEN")
	if address == "" {
		address = ":8080"
	}

	server := &http.Server{Addr: address, Handler: fake.Handler(), ReadHeaderTimeout: 5 * time.Second}

	log.Printf("fakestacks: serving the stack %q on %q", fake.StackKey, address) //nolint:gosec // G706: the environment of the pod, quoted

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}
