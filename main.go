// Command llmproxy sits between your app and an LLM API and records the model,
// tokens, cost and latency of every request.
package main

import (
	"fmt"
	"os"
)

const usageText = `llmproxy tracks the cost and latency of your LLM API calls.

Usage:
  llmproxy serve [flags]   run the proxy
  llmproxy stats [flags]   show spend from the local database

Run "llmproxy <command> -h" to see a command's flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	if err := loadDotEnv(".env"); err != nil {
		fmt.Fprintln(os.Stderr, "llmproxy:", err)
		os.Exit(1)
	}

	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "serve":
		err = runServe(args)
	case "stats":
		err = runStats(args, os.Stdout)
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "llmproxy: unknown command %q\n\n%s", cmd, usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "llmproxy:", err)
		os.Exit(1)
	}
}

// envOr returns the environment variable key, or fallback when it is unset.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
