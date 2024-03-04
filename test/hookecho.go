// Hook Echo is a simply utility used for testing the Webhook package.

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// TODO worth refactoring since the function is very straightforward in the way it works with OS arguments
func main() {
	if len(os.Args) > 1 {
		fmt.Printf("arg: %s\n", strings.Join(os.Args[1:], " "))
	}

	var env []string
	for _, v := range os.Environ() {
		if strings.HasPrefix(v, "HOOK_") {
			env = append(env, v)
		}
	}

	if len(env) > 0 {
		fmt.Printf("env: %s\n", strings.Join(env, " "))
	}

	if len(os.Args) > 1 {
		var exitCode string
		for _, arg := range os.Args[1:] {
			if strings.HasPrefix(arg, "sleep=") {
				timeout, err := time.ParseDuration(arg[6:])
				if err != nil {
					fmt.Printf(err.Error())
					os.Exit(-1)
				}
				time.Sleep(timeout)
			}
			if strings.HasPrefix(arg, "exit=") {
				exitCode = arg[5:]
			}
		}

		if exitCode != "" {
			code, err := strconv.Atoi(exitCode)
			if err != nil {
				fmt.Printf("Exit code %s not an int!", code)
				os.Exit(-1)
			}
			os.Exit(code)
		}
	}
}
