package main

import (
	"fmt"
	"os"

	"github.com/chenhg5/cc-connect/agent/planguard"
)

func runPlanGuardCommand() bool {
	if len(os.Args) < 2 || (os.Args[1] != "_cc-plan-hook" && os.Args[1] != "_cc-plan-mcp") {
		return false
	}
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "planning bridge: expected state path")
		os.Exit(2)
	}
	var err error
	if os.Args[1] == "_cc-plan-hook" {
		err = planguard.RunHook(os.Args[2], os.Stdin, os.Stdout)
	} else {
		err = planguard.RunMCP(os.Args[2], os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "planning bridge failed")
		os.Exit(2)
	}
	return true
}
