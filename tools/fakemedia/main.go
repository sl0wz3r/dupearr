// Command fakemedia serves a fake Plex Media Server plus Radarr, Radarr 4K and Sonarr instances
// (package internal/testutil/fakemedia) for local demos, web-UI development and manual end-to-end
// testing of Dupearr. It never contacts real servers and only writes inside its own data directory,
// which holds a tree of sparse dummy video files (large reported sizes, almost no disk use).
//
// Usage:
//
//	go run ./tools/fakemedia -data ./tmp/fake -scenario default \
//	    -plex-port 32400 -radarr-port 7878 -radarr4k-port 7879 -sonarr-port 8989
//
// It prints the URLs, the Plex token, the *arr API keys and the path mappings to configure in
// Dupearr (the fake servers report Docker-style paths under /data/media, which live in the data
// directory locally), then serves until interrupted. On shutdown it lists any request Dupearr must
// never make (whole-item deletes, emptyTrash, bulk *arr deletes, rescans without an id, deletes of
// an optimized version or of the last available copy of an item, per-file deletes inside a
// full-disc backup, …) and any full-disc backup left incomplete.
//
// The "discs" scenario holds full-disc backups (Blu-ray / UHD BDMV trees, a DVD VIDEO_TS, an ISO,
// a multi-disc set, a disc-only folder, a Radarr-tracked clip, a damaged disc and a TV season
// disc). Plex's default scanner hides them; -disc-scanner makes the movie libraries use Plex's
// legacy "Plex Movie Scanner with Disc Image Support", which exposes each disc as one version
// with one Part per BDMV/STREAM clip (300 for the UHD disc):
//
//	go run ./tools/fakemedia -scenario discs -disc-scanner
//
// The "looseclips" scenario holds flattened disc backups: numbered .m2ts clips (and DVD VOBs) lying
// loose in movie folders, which Plex's default scanner lists as one version PER CLIP (60–190
// "copies" of one movie). Deleting any of them through Plex or an *arr is reported on shutdown.
//
// Run with -h for all flags and -list for the available scenarios.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// errUsage marks command-line errors (exit status 2).
var errUsage = errors.New("invalid usage")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		fmt.Fprintln(os.Stderr, "fakemedia:", err)
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "fakemedia:", err)
		os.Exit(1)
	}
}

// run parses args, starts the fake servers and blocks until ctx is done.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("fakemedia", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: fakemedia [flags]\n\n"+
			"Serves a fake Plex Media Server, Radarr, Radarr 4K and Sonarr for Dupearr development.\n"+
			"Port 0 picks a free port.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	var (
		dataDir       = fs.String("data", "", "data `directory` (created when missing; must be empty or created by fakemedia, whose media tree is reset on start; empty = temporary directory removed on exit)")
		scenario      = fs.String("scenario", fakemedia.ScenarioDefault, "scenario `name` (see -list)")
		list          = fs.Bool("list", false, "list the scenarios and exit")
		host          = fs.String("host", "127.0.0.1", "listen `address` (0.0.0.0 makes the fakes reachable from containers)")
		plexPort      = fs.Int("plex-port", 32400, "fake Plex Media Server `port`")
		radarrPort    = fs.Int("radarr-port", 7878, "fake Radarr `port`")
		radarr4kPort  = fs.Int("radarr4k-port", 7879, "fake Radarr 4K `port`")
		sonarrPort    = fs.Int("sonarr-port", 8989, "fake Sonarr `port`")
		mediaDeletion = fs.Bool("media-deletion", true, "enable Plex's \"Allow media deletion\" setting")
		autoEmpty     = fs.Bool("auto-empty-trash", false, "enable Plex's \"Empty trash automatically after every scan\" setting")
		playing       = fs.String("playing", "", "comma-separated rating `keys` reported as playing by /status/sessions")
		lenient       = fs.Bool("lenient-accept", false, "serve Plex JSON without Accept: application/json (handy for curl and browsers)")
		discScanner   = fs.Bool("disc-scanner", false, "movie libraries use Plex's legacy disc-image scanner: full-disc backups become versions with one Part per BDMV/STREAM clip")
		verbose       = fs.Bool("v", false, "log every request to stderr (secrets redacted)")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("%w: unexpected arguments %q", errUsage, fs.Args())
	}
	if *list {
		for _, name := range fakemedia.ScenarioNames() {
			sc, err := fakemedia.ByName(name)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%-8s %s\n", name, sc.Description)
		}
		fmt.Fprintln(stdout, "\nThe discs scenario hides its full-disc backups from Plex (default scanner); add -disc-scanner to expose them as versions.")
		fmt.Fprintln(stdout, "The looseclips scenario stores Blu-ray/DVD backups flattened: Plex lists every loose clip as its own version.")
		return nil
	}

	sc, err := fakemedia.ByName(*scenario)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	sc.Server.AllowMediaDeletion = *mediaDeletion
	sc.Server.AutoEmptyTrash = *autoEmpty

	ports := []struct {
		server string
		port   int
	}{
		{fakemedia.ServerPlex, *plexPort},
		{fakemedia.InstanceRadarr, *radarrPort},
		{fakemedia.InstanceRadarr4K, *radarr4kPort},
		{fakemedia.InstanceSonarr, *sonarrPort},
	}
	addrs := make(map[string]string, len(ports))
	for _, p := range ports {
		if p.port < 0 || p.port > 65535 {
			return fmt.Errorf("%w: invalid %s port %d", errUsage, p.server, p.port)
		}
		addrs[p.server] = net.JoinHostPort(*host, strconv.Itoa(p.port))
	}

	opts := fakemedia.Options{Dir: *dataDir, Scenario: sc, Addrs: addrs, LenientAccept: *lenient, DiscImageScanner: *discScanner}
	if *verbose {
		opts.Logf = log.New(stderr, "fakemedia ", log.LstdFlags|log.Lmicroseconds).Printf
	}
	env, err := fakemedia.New(opts)
	if err != nil {
		return err
	}
	if keys := splitList(*playing); len(keys) > 0 {
		env.SetPlaying(keys...)
	}

	env.Describe(stdout)
	if allInterfaces(*host) {
		fmt.Fprintln(stdout, "\nListening on all interfaces: from a container use the host's address (e.g. host.docker.internal) instead of 127.0.0.1.")
		fmt.Fprintln(stdout, "The credentials above are fixed, public test values: anyone who can reach these ports can delete files in the data directory.")
	}
	if *dataDir == "" {
		fmt.Fprintln(stdout, "\nThe data directory is temporary and removed on exit (use -data to keep it).")
	}
	fmt.Fprintln(stdout, "\nServing until interrupted (Ctrl-C)…")

	<-ctx.Done()
	fmt.Fprintln(stdout, "\nShutting down…")
	if v := env.Violations(); len(v) > 0 {
		fmt.Fprintf(stdout, "%d forbidden request(s) received:\n", len(v))
		for _, x := range v {
			fmt.Fprintf(stdout, "  %s\n", x)
		}
	}
	if p := env.DiscProblems(); len(p) > 0 {
		fmt.Fprintf(stdout, "%d full-disc backup(s) left incomplete:\n", len(p))
		for _, x := range p {
			fmt.Fprintf(stdout, "  %s\n", x)
		}
	}
	if err := env.Close(); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}
	return nil
}

// allInterfaces reports whether host is a wildcard listen address ("", 0.0.0.0, ::).
func allInterfaces(host string) bool {
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// splitList splits a comma-separated flag value, dropping empty entries.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
