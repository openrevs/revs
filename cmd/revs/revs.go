package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/go-playground/validator/v10"
	"github.com/openrevs/revs/keys"
	"github.com/openrevs/revs/ratingset"
	"github.com/openrevs/revs/server"
	"github.com/openrevs/revs/tui"
	"github.com/openrevs/revs/ui"
	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

// cli is the full command-line grammar. Every flag can also be set in a
// YAML config file under a section named for its command (see
// site/specs/configuration.md); flags given on the
// command line win.
//
// Each command's help tag is its one-line summary in the command list;
// the Help method on its type supplies the longer text shown by
// "revs <command> --help" and on usage errors.
type cli struct {
	Config kong.ConfigFlag `help:"Load configuration from a file (overrides the default config paths)." placeholder:"FILE"`

	TUI    tuiCmd    `cmd:"" group:"common" help:"Manage your movies and reviews in the terminal"`
	UI     uiCmd     `cmd:"" name:"ui" group:"common" help:"Manage your movies and reviews in a local web UI"`
	Keygen keygenCmd `cmd:"" group:"common" help:"Generate a new identity keypair"`

	Server serverCmd `cmd:"" group:"servers" help:"Run a ratings publishing server"`

	Sign   signCmd   `cmd:"" group:"documents" help:"Sign a ratings document"`
	Verify verifyCmd `cmd:"" group:"documents" help:"Check a signed document's signature"`
	Diff   diffCmd   `cmd:"" group:"documents" help:"Create a patch from one document to another"`
	Patch  patchCmd  `cmd:"" group:"documents" help:"Apply a patch to a document"`

	YAML yamlCmd `cmd:"" group:"dev" help:"Convert a YAML ratings file to a signed document"`
	Add  addCmd  `cmd:"" group:"dev" help:"Add a title to data.yaml (prints the result)"`
}

type tuiCmd struct {
	DataDir string `name:"data" help:"Data directory" default:"${revs_home}" type:"path"`
}

func (tuiCmd) Help() string {
	return "Opens a full-screen terminal UI for your movies, viewings and reviews, " +
		"your identity, and the servers you publish to. It shows the same data as " +
		"\"revs ui\", and both can run at once."
}

type uiCmd struct {
	DataDir     string `name:"data" help:"Data directory" default:"${revs_home}" type:"path"`
	Listen      string `help:"HTTP listen address" default:"${default_ui_listen}"`
	NoBrowser   bool   `help:"Don't open a web browser"`
	LogRequests bool   `help:"Log every HTTP request served (method, path, status, size, duration; never bodies)"`
}

func (uiCmd) Help() string {
	return "Starts a web UI on a local address and opens it in your browser. " +
		"It shows the same data as \"revs tui\". Stop it with Ctrl-C."
}

type keygenCmd struct{}

func (keygenCmd) Help() string {
	return "Prints a new ID, public key and private key. Nothing is saved: keep " +
		"the private key somewhere safe and pass it to commands that sign, " +
		"such as \"revs sign --private-key KEY\"."
}

type serverCmd struct {
	DataDir       string        `name:"data" help:"Data directory" default:"${revs_home}/server" type:"path"`
	Listen        string        `help:"HTTP listen address" default:"${default_server_listen}"`
	CutInterval   time.Duration `help:"Epoch cut interval" default:"${default_cut_interval}"`
	SnapshotEvery int           `help:"Publish a full snapshot every N epochs" default:"${default_snapshot_every}"`
	PartSize      int           `help:"Target size in bytes of each snapshot and patch archive part" default:"${default_part_size}"`
	CompactEvery  []int         `help:"Epoch intervals at which patches are compacted into aligned ranges (comma separated)" default:"${default_compact_every}"`
	Pull          []string      `help:"Pull documents from the server at URL (repeatable)" placeholder:"URL"`
	PullInterval  time.Duration `help:"How often to pull from --pull servers" default:"${default_pull_interval}"`
	LogRequests   bool          `help:"Log every HTTP request served (method, path, status, size, duration; never bodies)"`
	AdminListen   string        `help:"Serve the admin web UI on this address (e.g. 127.0.0.1:1324); its password is read from ${admin_password_env}" placeholder:"ADDR"`
}

func (serverCmd) Help() string {
	return "Accepts signed ratings documents and periodically publishes them as an " +
		"epoch on each of its channels, with a full snapshot every " +
		"--snapshot-every epochs and patches compacted at the --compact-every " +
		"boundaries. With --pull, it also pulls the documents other servers " +
		"publish and republishes the ones newer than its own, remembering which " +
		"server each came from. The data directory holds the server's identity " +
		"key, its document store and the published tree; back it up to keep the " +
		"server's identity. With --admin-listen, it also serves an admin web UI " +
		"on a separate address, protected by the password in " + adminPasswordEnv + "."
}

type signCmd struct {
	Input      *os.File `arg:"" help:"JSONL document to sign (\"-\" for stdin)"`
	Output     string   `arg:"" optional:"" help:"Where to write the signed document (default: stdout)"`
	PrivateKey string   `required:"" placeholder:"KEY" help:"Private key to sign with (from \"revs keygen\")"`
}

func (signCmd) Help() string {
	return "Signs a JSONL ratings document with your private key. If the input is " +
		"already signed, its old signature line is replaced, so an edited document " +
		"can simply be signed again. Check the result with \"revs verify\"."
}

type verifyCmd struct {
	Input *os.File `arg:"" optional:"" help:"Signed document to check (default: stdin)"`
}

func (verifyCmd) Help() string {
	return "Checks that a signed document is intact and was signed by the key it " +
		"names, then prints its hash and signer ID. Exits non-zero if the check fails."
}

type diffCmd struct {
	Base *os.File `arg:"" help:"Original document"`
	Dest *os.File `arg:"" help:"Updated document"`
}

func (diffCmd) Help() string {
	return "Prints a patch that turns <base> into <dest>. Apply it with \"revs patch\"."
}

type patchCmd struct {
	Base *os.File `arg:"" help:"Original document"`
	Diff *os.File `arg:"" help:"Patch from \"revs diff\""`
}

func (patchCmd) Help() string {
	return "Applies a patch made by \"revs diff\" to <base> and prints the result."
}

type yamlCmd struct {
	Input      *os.File `arg:"" help:"YAML ratings file"`
	PrivateKey string   `required:"" placeholder:"KEY" help:"Private key to sign with (from \"revs keygen\")"`
}

func (yamlCmd) Help() string {
	return "Converts a YAML ratings file (like data.yaml) to a JSONL document, signs " +
		"it, and prints the result."
}

type addCmd struct{}

func (addCmd) Help() string {
	return "Prompts for a movie title, adds it to ./data.yaml in memory, and prints " +
		"the resulting YAML. The file itself is not changed."
}

var CLI cli

// adminPasswordEnv names the environment variable holding the server's
// admin UI password. It is not a flag, so it stays out of process
// listings and config files.
const adminPasswordEnv = "REVS_ADMIN_PASSWORD"

const tagline = "Portable, signed movie ratings that you own."

// commandGroups titles the sections of the top-level help. Kong lists
// groups in the order their commands appear in the cli struct.
var commandGroups = []kong.Group{
	{Key: "common", Title: "Common commands:"},
	{Key: "servers", Title: "Servers:"},
	{Key: "documents", Title: "Documents:"},
	{Key: "dev", Title: "Development:"},
}

// revsHome is the root that data directories and the user config file
// default to: $REVS_HOME if set, else ~/.revs. Pointing REVS_HOME at a
// different directory gives a fully separate instance (identity, store,
// servers and config), which is handy for testing as several users.
func revsHome() string {
	if home := os.Getenv("REVS_HOME"); home != "" {
		return home
	}
	return "~/.revs"
}

// defaultConfigPaths are searched in order; values in later files
// override earlier ones, and --config overrides them all.
func defaultConfigPaths() []string {
	return []string{filepath.Join(revsHome(), "config.yaml"), "./revs.yaml"}
}

// newParser builds the CLI parser with config-file support layered in.
// Precedence, lowest to highest: struct-tag defaults, config files (in
// configPaths order, then --config), then command-line flags.
func newParser(c *cli, configPaths ...string) (*kong.Kong, error) {
	return kong.New(c,
		kong.Name("revs"),
		kong.Description(tagline),
		kong.ExplicitGroups(commandGroups),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true, FlagsLast: true}),
		// On a usage error, show the command's help above the error.
		kong.UsageOnError(),
		kong.Configuration(configResolver, configPaths...),
		kong.Vars{
			"revs_home":              revsHome(),
			"default_server_listen":  server.DefaultListen,
			"default_ui_listen":      ui.DefaultListen,
			"default_cut_interval":   server.DefaultCutInterval.String(),
			"default_snapshot_every": strconv.Itoa(server.DefaultSnapshotEvery),
			"default_part_size":      strconv.Itoa(server.DefaultPartSize),
			"default_compact_every":  joinInts(server.DefaultCompactEvery),
			"default_pull_interval":  server.DefaultPullInterval.String(),
			"admin_password_env":     adminPasswordEnv,
		},
	)
}

func main() {
	parser, err := newParser(&CLI, defaultConfigPaths()...)
	if err != nil {
		panic(err)
	}
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"--help"}
	}
	ctx, err := parser.Parse(args)
	parser.FatalIfErrorf(err)

	switch ctx.Command() {
	case "server":
		c := CLI.Server
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		password := os.Getenv(adminPasswordEnv)
		if c.AdminListen != "" && password == "" {
			exitIfErr("revs server", fmt.Errorf("--admin-listen needs a password in %s", adminPasswordEnv))
		}
		srv := openServer(c.DataDir, server.Options{
			CutInterval:   c.CutInterval,
			SnapshotEvery: c.SnapshotEvery,
			PartSize:      c.PartSize,
			CompactEvery:  nonNil(c.CompactEvery),
			Pull:          c.Pull,
			PullInterval:  c.PullInterval,
			LogRequests:   c.LogRequests,
			AdminListen:   c.AdminListen,
			AdminPassword: password,
		})
		exitIfErr("revs server", srv.ListenAndServe(ctx, c.Listen))
	case "tui":
		if err := tui.Run(tui.Options{DataDir: CLI.TUI.DataDir}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "ui":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := ui.Run(ctx, ui.Options{
			DataDir:     CLI.UI.DataDir,
			Listen:      CLI.UI.Listen,
			OpenBrowser: !CLI.UI.NoBrowser,
			LogRequests: CLI.UI.LogRequests,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "revs ui:", err)
			os.Exit(1)
		}

	case "keygen":
		u := ratingset.NewUser()
		pub, prv := u.Keys()

		fmt.Printf("ID:          %s\n", u.ID)
		fmt.Printf("Public key:  %s\n", pub)
		fmt.Printf("Private key: %s\n", prv)

	case "yaml <input>":
		data, err := io.ReadAll(CLI.YAML.Input)
		if err != nil {
			panic(err)
		}
		pk, err := keys.ParsePrivate(CLI.YAML.PrivateKey)
		parser.FatalIfErrorf(err, "--private-key")
		signed, err := ratingset.ConvertYAML(data, pk)
		exitIfErr("revs yaml", err)
		os.Stdout.Write(signed)

	case "sign <input>", "sign <input> <output>":
		data, err := io.ReadAll(CLI.Sign.Input)
		if err != nil {
			panic(err)
		}
		pk, err := keys.ParsePrivate(CLI.Sign.PrivateKey)
		parser.FatalIfErrorf(err, "--private-key")
		signed, err := signDocument(data, pk)
		if err != nil {
			fmt.Fprintln(os.Stderr, "revs sign:", err)
			os.Exit(1)
		}
		if CLI.Sign.Output == "" {
			os.Stdout.Write(signed)
		} else if err := os.WriteFile(CLI.Sign.Output, signed, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "revs sign:", err)
			os.Exit(1)
		}

	case "verify", "verify <input>":
		input := os.Stdin
		if CLI.Verify.Input != nil {
			input = CLI.Verify.Input
		}
		data, err := io.ReadAll(input)
		if err != nil {
			panic(err)
		}
		info, err := jsonlines.Verify(data)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("File verification succeeded")
		fmt.Printf("Hash:      %s\n", info.Hash)
		fmt.Printf("Signer ID: %s\n", info.SignerID)

	case "diff <base> <dest>":
		base, err := io.ReadAll(CLI.Diff.Base)
		if err != nil {
			panic(err)
		}
		dest, err := io.ReadAll(CLI.Diff.Dest)
		if err != nil {
			panic(err)
		}
		patch, err := jsonlines.Diff(base, dest)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(string(patch))
	case "patch <base> <diff>":
		base, err := io.ReadAll(CLI.Patch.Base)
		if err != nil {
			panic(err)
		}
		diff, err := io.ReadAll(CLI.Patch.Diff)
		if err != nil {
			panic(err)
		}
		result, _, err := jsonlines.Patch(base, diff)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(string(result))

	case "add":
		if err := addNewMovie(); err != nil {
			panic(err)
		}

	default:
		panic(ctx.Command())
	}
}

// signDocument signs JSONL data lines. An existing sjsonl control line
// (an already-signed document) is dropped first, so a document can be
// re-signed after editing.
func signDocument(data []byte, key ed25519.PrivateKey) ([]byte, error) {
	first, rest, _ := bytes.Cut(data, []byte("\n"))
	if gjson.GetBytes(first, "sjsonl").Exists() {
		data = rest
	}
	signed, _, err := jsonlines.Sign(data, key)
	return signed, err
}

// openServer opens the server in dataDir, creating the directory if
// needed, or exits.
func openServer(dataDir string, opts server.Options) *server.Server {
	ensureDir(dataDir)
	srv, err := server.Open(dataDir, opts)
	exitIfErr("revs", err)
	return srv
}

// exitIfErr prints err, prefixed by what failed, and exits non-zero.
func exitIfErr(what string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		os.Exit(1)
	}
}

// ensureDir creates a server data directory if it doesn't exist yet,
// as the tui and ui commands do for theirs.
func ensureDir(path string) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "revs: data directory: %v\n", err)
		os.Exit(1)
	}
}

func addNewMovie() error {
	data, err := os.ReadFile("data.yaml")
	if err != nil {
		return fmt.Errorf("Error reading data.yaml: %w", err)
	}
	validate := validator.New()

	var conf ratingset.Example
	if err := yaml.Unmarshal(data, &conf); err != nil {
		return err
	}

	if err := validate.Struct(conf); err != nil {
		return err
	}

	fmt.Println("Enter movie title:")
	reader := bufio.NewReader(os.Stdin)
	text, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("Error reading input: %w", err)
	}
	text = strings.TrimSpace(text)

	conf.Items = append(conf.Items, ratingset.Item{
		Type:  ratingset.ItemTypeMovie,
		Title: text,
	})

	d, err := yaml.Marshal(conf)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	fmt.Printf("--- m dump:\n%s\n\n", string(d))

	return nil
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// nonNil turns a nil slice (an empty flag value) into an empty one, so
// that an explicitly empty --compact-every disables compaction.
func nonNil(ns []int) []int {
	if ns == nil {
		return []int{}
	}
	return ns
}
