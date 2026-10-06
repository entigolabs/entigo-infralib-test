// infralib-test is the in-image command of the entigo-infralib-testing
// framework. The host orchestrator script calls it inside the per-cloud test
// image; it never talks to docker itself.
//
//	infralib-test envs                        list environments
//	infralib-test generate [flags]            write agent configurations
//	infralib-test run [flags] [module...]     run module tests and format the result
package main

import (
	"fmt"
	"os"
)

const usage = `usage: infralib-test <command> [flags]

commands:
  envs       list the environments of the repository
  generate   write agent configurations under agents/
  run        run module tests with go test and summarise the result

Run "infralib-test <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "envs":
		err = envsCommand(os.Args[2:])
	case "generate":
		err = generateCommand(os.Args[2:])
	case "run":
		err = runCommand(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		var exit exitError
		if asExit(err, &exit) {
			if exit.message != "" {
				fmt.Fprintln(os.Stderr, exit.message)
			}
			os.Exit(exit.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// exitError carries an exit code without a stack of wrapping.
type exitError struct {
	code    int
	message string
}

func (e exitError) Error() string { return e.message }

func asExit(err error, target *exitError) bool {
	e, ok := err.(exitError)
	if ok {
		*target = e
	}
	return ok
}

// stringList is a repeatable flag.
type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}
