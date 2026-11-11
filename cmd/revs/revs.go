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
	"strconv"
	"strings"
	"syscall"

	"time"

	"github.com/alecthomas/kong"
	"github.com/go-playground/validator/v10"
	"github.com/openrevs/revs"
	"github.com/openrevs/revs/keys"
	"github.com/openrevs/revs/tui"
	"github.com/openrevs/revs/ui"
	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

// cli is the full command-line grammar. Every flag can also be set in a
// YAML config file under a section named for its command (see
// wip-docs/configuration.md in the website repo); flags given on the
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

	Server       serverCmd       `cmd:"" group:"servers" help:"Run a ratings publishing server"`
	Client       clientCmd       `cmd:"" group:"servers" help:"Run the local web client"`
	ClientServer clientServerCmd `cmd:"" name:"clientserver" group:"servers" help:"Run the web client and a server together"`

	Sign   signCmd   `cmd:"" group:"documents" help:"Sign a ratings document"`
	Verify verifyCmd `cmd:"" group:"documents" help:"Check a signed document's signature"`
	Diff   diffCmd   `cmd:"" group:"documents" help:"Create a patch from one document to another"`
	Patch  patchCmd  `cmd:"" group:"documents" help:"Apply a patch to a document"`

	YAML yamlCmd `cmd:"" group:"dev" help:"Convert a YAML ratings file to a signed document"`
	Add  addCmd  `cmd:"" group:"dev" help:"Add a title to data.yaml (prints the result)"`
}

type tuiCmd struct {
	DataDir string `name:"data" help:"Data directory" default:"~/.revs" type:"path"`
}

func (tuiCmd) Help() string {
	return "Opens a full-screen terminal UI for your movies, viewings and reviews, " +
		"your identity, and the servers you publish to. It shows the same data as " +
		"\"revs ui\", and both can run at once."
}

type uiCmd struct {
	DataDir   string `name:"data" help:"Data directory" default:"~/.revs" type:"path"`
	Listen    string `help:"HTTP listen address" default:"${default_ui_listen}"`
	NoBrowser bool   `help:"Don't open a web browser"`
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
	DataDir           string        `name:"data" help:"Data directory" default:"~/.revs/server" type:"path"`
	Listen            string        `help:"HTTP listen address" default:"${default_server_listen}"`
	CutInterval       time.Duration `help:"Epoch cut interval" default:"${default_cut_interval}"`
	SnapshotEvery     int           `help:"Publish a full snapshot every N epochs" default:"${default_snapshot_every}"`
	SnapshotChunkSize int           `help:"Snapshot chunk target size in bytes" default:"${default_snapshot_chunk_size}"`
}

func (serverCmd) Help() string {
	return "Accepts signed ratings documents and periodically publishes them as an " +
		"epoch, with a full snapshot every --snapshot-every epochs. The data " +
		"directory holds the server's identity key, its document store and the " +
		"published tree; back it up to keep the server's identity."
}

type clientCmd struct {
	DataDir string `name:"data" help:"Data directory" default:"~/.revs/server" type:"path"`
	Listen  string `help:"HTTP listen address" default:"${default_client_listen}"`
	Demo    bool   `help:"Insert demo data"`
}

func (clientCmd) Help() string {
	return "Serves the web client from the given data directory."
}

type clientServerCmd struct {
	DataDir           string        `name:"data" help:"Data directory" default:"~/.revs/server" type:"path"`
	ServerListen      string        `help:"Server HTTP listen address" default:"${default_server_listen}"`
	ClientListen      string        `help:"Client HTTP listen address" default:"${default_client_listen}"`
	Demo              bool          `help:"Insert demo data"`
	CutInterval       time.Duration `help:"Epoch cut interval" default:"${default_cut_interval}"`
	SnapshotEvery     int           `help:"Publish a full snapshot every N epochs" default:"${default_snapshot_every}"`
	SnapshotChunkSize int           `help:"Snapshot chunk target size in bytes" default:"${default_snapshot_chunk_size}"`
}

func (clientServerCmd) Help() string {
	return "Runs \"revs server\" and \"revs client\" in one process, sharing a data " +
		"directory. Handy for local development."
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

const tagline = "Portable, signed movie ratings that you own."

// commandGroups titles the sections of the top-level help. Kong lists
// groups in the order their commands appear in the cli struct.
var commandGroups = []kong.Group{
	{Key: "common", Title: "Common commands:"},
	{Key: "servers", Title: "Servers:"},
	{Key: "documents", Title: "Documents:"},
	{Key: "dev", Title: "Development:"},
}

// defaultConfigPaths are searched in order; values in later files
// override earlier ones, and --config overrides them all.
var defaultConfigPaths = []string{"~/.revs/config.yaml", "./revs.yaml"}

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
			"default_server_listen":       revs.DefaultServerListen,
			"default_client_listen":       revs.DefaultClientListen,
			"default_ui_listen":           ui.DefaultListen,
			"default_cut_interval":        revs.DefaultCutInterval.String(),
			"default_snapshot_every":      strconv.Itoa(revs.DefaultSnapshotEvery),
			"default_snapshot_chunk_size": strconv.Itoa(revs.DefaultSnapshotChunkSize),
		},
	)
}

func main() {
	parser, err := newParser(&CLI, defaultConfigPaths...)
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
		ensureDir(CLI.Server.DataDir)
		revs.Start2(CLI.Server.DataDir, revs.ServerOptions{
			Listen:            CLI.Server.Listen,
			CutInterval:       CLI.Server.CutInterval,
			SnapshotEvery:     CLI.Server.SnapshotEvery,
			SnapshotChunkSize: CLI.Server.SnapshotChunkSize,
		})
	case "client":
		ensureDir(CLI.Client.DataDir)
		revs.Start(CLI.Client.DataDir, CLI.Client.Listen, CLI.Client.Demo)
	case "clientserver":
		ensureDir(CLI.ClientServer.DataDir)
		go revs.Start2(CLI.ClientServer.DataDir, revs.ServerOptions{
			Listen:            CLI.ClientServer.ServerListen,
			CutInterval:       CLI.ClientServer.CutInterval,
			SnapshotEvery:     CLI.ClientServer.SnapshotEvery,
			SnapshotChunkSize: CLI.ClientServer.SnapshotChunkSize,
		})
		revs.Start(CLI.ClientServer.DataDir, CLI.ClientServer.ClientListen, CLI.ClientServer.Demo)
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
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "revs ui:", err)
			os.Exit(1)
		}

	case "keygen":
		u := revs.NewUser()
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
		if _, err := revs.ConvertYAML(data, pk); err != nil {
			panic(err)
		}

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

	var conf revs.Example
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

	conf.Titles = append(conf.Titles, revs.Title2{
		Name: text,
	})

	d, err := yaml.Marshal(conf)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	fmt.Printf("--- m dump:\n%s\n\n", string(d))

	return nil
}
