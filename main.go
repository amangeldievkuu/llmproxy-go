// Command llmproxy sits between your app and an LLM API and records the model,
// tokens, cost and latency of every request.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

// version is set at release time with -ldflags "-X main.version=v1.2.3".
var version string

const usageText = `llmproxy tracks the cost and latency of your LLM API calls.

Usage:
  llmproxy serve [flags]   run the proxy
  llmproxy stats [flags]   show spend from the local database
  llmproxy version         print the version

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
	case "version", "-version", "--version":
		fmt.Println("llmproxy", buildVersion())
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

// buildVersion returns the release version, or the module version recorded by
// "go install", or "dev".
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
