// Command hover is the native CLI for the Hover application.
//
// Usage:
//
//	hover jobs generate --pr <N> --anon-key <key> [--interval 30s] [--jobs 10] [--repeats 4] [--concurrency random]
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// version is set at build time via ldflags.
var version = "dev"

func main() {
	checkAndUpdate()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "jobs":
		if len(os.Args) < 3 || os.Args[2] != "generate" {
			fmt.Fprintln(os.Stderr, "Usage: hover jobs generate [flags]")
			if len(os.Args) >= 3 && (os.Args[2] == "help" || os.Args[2] == "--help" || os.Args[2] == "-h") {
				os.Exit(0)
			}
			os.Exit(1)
		}
		// Handle --help within "jobs generate" flags.
		for _, a := range os.Args[3:] {
			if a == "--help" || a == "-h" || a == "help" {
				printUsage()
				os.Exit(0)
			}
		}
		if err := runJobsGenerate(os.Args[3:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "version":
		fmt.Printf("hover v%s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// checkAndUpdate queries the GitHub API for the latest CLI release tag and
// auto-updates via npm if a newer version is available.
func checkAndUpdate() {
	if version == "dev" {
		return
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/good-native/hover/git/matching-refs/tags/cli-v")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var refs []struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refs); err != nil || len(refs) == 0 {
		return
	}

	semverRe := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	var latest string
	for _, r := range refs {
		v := strings.TrimPrefix(r.Ref, "refs/tags/cli-v")
		if !semverRe.MatchString(v) {
			continue
		}
		if compareSemver(v, latest) > 0 {
			latest = v
		}
	}
	if latest == "" || compareSemver(latest, version) <= 0 {
		return
	}

	fmt.Fprintf(os.Stderr, "Updating hover v%s → v%s...\n", version, latest)
	cmd := exec.Command("npm", "install", "-g", "@good-native/hover")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Auto-update failed: %v\nRun manually: npm install -g @good-native/hover\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "Updated to v%s — restart hover to use new version.\n\n", latest)
	}
}

// compareSemver returns >0 if a > b, <0 if a < b, 0 if equal.
func compareSemver(a, b string) int {
	parse := func(s string) [3]int {
		var parts [3]int
		_, _ = fmt.Sscanf(s, "%d.%d.%d", &parts[0], &parts[1], &parts[2]) //nolint:errcheck // best-effort parse, zero-value on failure is fine
		return parts
	}
	pa, pb := parse(a), parse(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] - pb[i]
		}
	}
	return 0
}

func printUsage() {
	usage := `hover — native CLI for the Hover application

Commands:
  jobs generate   Create load-test jobs against a preview or production instance
  version         Print version information

Usage:
  hover jobs generate --pr <N> --anon-key <key> [options]

  Hover options:
	--interval <dur>     Run batch every interval (e.g. 30s, 2m) [default: 3m]
	--jobs <N>           Jobs per batch [default: 10]
	--concurrency <N>    Per-job concurrency 1-50, or "random" [default: 20]
	--repeats <N>        How many times to run each domain [default: 4]
	
	--pr <N>             Target preview app hover-pr-<N>.fly.dev
	--anon-key <key>     Supabase publishable key (auto-discovered if omitted)
	--status-interval    Poll interval when waiting to rerun a domain [default: 30s]
	--auth-url <url>     Override Supabase auth base URL
	--api-url <url>      Override API base URL
	--yes, -y            Skip confirmation prompt`
	fmt.Fprintln(os.Stderr, strings.TrimSpace(usage))
}
