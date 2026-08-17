// Command fpm-registry serves the fpm package registry.
//
// It replaces nginx's WebDAV on the write path. Reads are still plain files
// under a document root, so they can be served by this process, by nginx, or
// from a CDN in front of the same directory — and an unmodified fpm client
// cannot tell the difference beyond the base URL.
//
//	fpm-registry serve  --root /var/fpm-repo --publishers /etc/fpm/publishers.json
//	fpm-registry issue  --publishers /etc/fpm/publishers.json --name acme --org acme
//
// The `issue` subcommand exists because a registry nobody can publish to is
// not useful, and hand-editing sha256 hashes into a JSON file is the kind of
// task people work around by reusing one token everywhere.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fpm/internal/registryd"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "issue":
		err = issue(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "fpm-registry: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `fpm-registry — the fpm package registry

  serve   Run the registry
  issue   Issue a publisher token

Run a subcommand with --help for its flags.
`)
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	root := flags.String("root", envOr("FPM_REGISTRY_ROOT", "/var/fpm-repo"),
		"document root holding artifacts and metadata")
	publishers := flags.String("publishers", envOr("FPM_REGISTRY_PUBLISHERS", ""),
		"JSON file of publisher records; without it the registry accepts no writes")
	addr := flags.String("addr", envOr("FPM_REGISTRY_ADDR", ":8080"), "listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}

	store, err := registryd.NewStore(*root)
	if err != nil {
		return err
	}

	auth, err := registryd.LoadTokenAuthenticator(*publishers)
	if err != nil {
		return fmt.Errorf("loading publishers: %w", err)
	}
	if *publishers == "" {
		// Refusing every write is the safe direction, but silently refusing
		// them looks identical to a broken deployment, so say so.
		fmt.Fprintln(os.Stderr,
			"warning: no publishers file configured; every write will be refused")
	}

	handler := registryd.NewServer(store, auth)
	if *publishers != "" {
		// A token that has already been handed to a publisher but is forgotten
		// on restart fails later, in their pipeline, for no visible reason.
		handler.OnPublishersChanged(func() error {
			return saveRecords(*publishers, auth.Publishers())
		})
	}

	server := &http.Server{
		Addr:    *addr,
		Handler: handler.Handler(),
		// Artifacts bundle vendored wheels and are legitimately large, so the
		// write timeout is generous. The read header timeout stays short —
		// that one only ever protects against a slow-loris client.
		ReadHeaderTimeout: 15 * time.Second,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	fmt.Printf("fpm-registry serving %s on %s\n", *root, *addr)
	return server.ListenAndServe()
}

func issue(args []string) error {
	flags := flag.NewFlagSet("issue", flag.ExitOnError)
	path := flags.String("publishers", envOr("FPM_REGISTRY_PUBLISHERS", ""),
		"JSON file of publisher records")
	name := flags.String("name", "", "publisher name")
	orgs := flags.String("org", "", "comma-separated organisations this publisher may write to")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if *path == "" || *name == "" || *orgs == "" {
		return fmt.Errorf("--publishers, --name and --org are all required")
	}

	token, err := newToken()
	if err != nil {
		return err
	}

	records, err := loadRecords(*path)
	if err != nil {
		return err
	}

	owned := strings.Split(*orgs, ",")
	for i := range owned {
		owned[i] = strings.TrimSpace(owned[i])
	}

	records = append(records, registryd.Publisher{
		Name: *name,
		Orgs: owned,
		// Only the hash is stored. A leak of this file must not hand over the
		// ability to publish, which a stored plaintext token would.
		TokenSHA256: registryd.HashToken(token),
	})

	if err := saveRecords(*path, records); err != nil {
		return err
	}

	fmt.Printf(`Issued a publisher token for %s (orgs: %s).

Shown once and not recoverable — only its hash is stored:

  %s

Give it to the publisher as an environment variable rather than a flag, so it
does not end up in shell history:

  fpm repo add <name> <url> --username %s
  export FPM_REPO_<NAME>_PASSWORD=%s
`, *name, strings.Join(owned, ", "), token, *name, token)
	return nil
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "fpm_" + hex.EncodeToString(buf), nil
}

func loadRecords(path string) ([]registryd.Publisher, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var records []registryd.Publisher
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("%s is not a valid publishers file: %w", path, err)
	}
	return records, nil
}

func saveRecords(path string, records []registryd.Publisher) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 0600: this file governs who may publish.
	return os.WriteFile(path, data, 0o600)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
