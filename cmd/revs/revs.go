package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"time"

	"github.com/alecthomas/kong"
	"github.com/go-playground/validator/v10"
	"github.com/openrevs/revs"
	"github.com/openrevs/revs/tui"
	"github.com/openrevs/revs/tui/workspace"
	jsonlines "github.com/openrevs/sjsonl-go"
	"gopkg.in/yaml.v3"
)

// cli is the full command-line grammar. Every flag can also be set in a
// YAML config file under a section named for its command (see
// docs/configuration.md); flags given on the command line win.
type cli struct {
	Config kong.ConfigFlag `help:"Load configuration from a file (overrides the default config paths)." placeholder:"FILE"`

	TUI struct {
		DataDir string `name:"data" help:"Data directory" default:"~/.revs" type:"path"`
		Server  string `help:"Server URL (persisted to the data dir config on first use)"`
	} `cmd:"" group:"common" help:"Edit your ratings interactively"`

	Keygen struct {
	} `cmd:"" group:"common" help:"Generate a new identity keypair"`

	Server struct {
		DataDir           string        `name:"data" help:"Data directory" default:"." type:"path"`
		Listen            string        `help:"HTTP listen address" default:"${default_server_listen}"`
		CutInterval       time.Duration `help:"Epoch cut interval" default:"${default_cut_interval}"`
		SnapshotEvery     int           `help:"Publish a full snapshot every N epochs" default:"${default_snapshot_every}"`
		SnapshotChunkSize int           `help:"Snapshot chunk target size in bytes" default:"${default_snapshot_chunk_size}"`
	} `cmd:"" group:"servers" help:"Run a ratings publishing server"`

	Client struct {
		DataDir string `name:"data" help:"Data directory" default:"." type:"path"`
		Listen  string `help:"HTTP listen address" default:"${default_client_listen}"`
		Demo    bool   `help:"Insert demo data"`
	} `cmd:"" group:"servers" help:"Run the local web client"`

	ClientServer struct {
		DataDir           string        `name:"data" help:"Data directory" default:"." type:"path"`
		ServerListen      string        `help:"Server HTTP listen address" default:"${default_server_listen}"`
		ClientListen      string        `help:"Client HTTP listen address" default:"${default_client_listen}"`
		Demo              bool          `help:"Insert demo data"`
		CutInterval       time.Duration `help:"Epoch cut interval" default:"${default_cut_interval}"`
		SnapshotEvery     int           `help:"Publish a full snapshot every N epochs" default:"${default_snapshot_every}"`
		SnapshotChunkSize int           `help:"Snapshot chunk target size in bytes" default:"${default_snapshot_chunk_size}"`
	} `cmd:"" name:"clientserver" group:"servers" help:"Run the web client and a server together"`

	Sign struct {
		Input      *os.File `arg:"" help:"Input file"`
		Output     string   `arg:"" optional:"" help:"Output file"`
		PrivateKey string   `required:"" help:"Private key"`
	} `cmd:"" group:"documents" help:"Sign a ratings document"`

	Verify struct {
		Input *os.File `arg:"" optional:"" help:"Input file (default: stdin)"`
	} `cmd:"" group:"documents" help:"Check a signed document's signature"`

	Diff struct {
		Base *os.File `arg:"" help:"Base file"`
		Dest *os.File `arg:"" help:"Dest file"`
	} `cmd:"" group:"documents" help:"Create a patch from one document to another"`

	Patch struct {
		Base *os.File `arg:"" help:"Base file"`
		Diff *os.File `arg:"" help:"Diff file"`
	} `cmd:"" group:"documents" help:"Apply a patch to a document"`

	YAML struct {
		Input      *os.File `arg:"" help:"Input file"`
		PrivateKey string   `required:"" help:"Private key"`
	} `cmd:"" group:"dev" help:"Convert a YAML ratings file to a signed document"`

	Add struct {
	} `cmd:"" group:"dev" help:"Add a title to data.yaml (prints the result)"`
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
		kong.Configuration(configResolver, configPaths...),
		kong.Vars{
			"default_server_listen":       revs.DefaultServerListen,
			"default_client_listen":       revs.DefaultClientListen,
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
		requireDir(CLI.Server.DataDir)
		revs.Start2(CLI.Server.DataDir, revs.ServerOptions{
			Listen:            CLI.Server.Listen,
			CutInterval:       CLI.Server.CutInterval,
			SnapshotEvery:     CLI.Server.SnapshotEvery,
			SnapshotChunkSize: CLI.Server.SnapshotChunkSize,
		})
	case "client":
		requireDir(CLI.Client.DataDir)
		revs.Start(CLI.Client.DataDir, CLI.Client.Listen, CLI.Client.Demo)
	case "clientserver":
		requireDir(CLI.ClientServer.DataDir)
		go revs.Start2(CLI.ClientServer.DataDir, revs.ServerOptions{
			Listen:            CLI.ClientServer.ServerListen,
			CutInterval:       CLI.ClientServer.CutInterval,
			SnapshotEvery:     CLI.ClientServer.SnapshotEvery,
			SnapshotChunkSize: CLI.ClientServer.SnapshotChunkSize,
		})
		revs.Start(CLI.ClientServer.DataDir, CLI.ClientServer.ClientListen, CLI.ClientServer.Demo)
	case "tui":
		if err := tui.Run(workspace.Options{DataDir: CLI.TUI.DataDir, ServerFlag: CLI.TUI.Server}); err != nil {
			fmt.Fprintln(os.Stderr, err)
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
		pk, err := hex.DecodeString(CLI.YAML.PrivateKey)
		if err != nil {
			panic(err)
		}
		if _, err := revs.ConvertYAML(data, pk); err != nil {
			panic(err)
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

// requireDir replaces kong's existingdir flag type, whose validation is
// skipped for values supplied by config-file resolvers.
func requireDir(path string) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "revs: data directory %q does not exist\n", path)
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
