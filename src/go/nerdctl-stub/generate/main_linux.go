// package main produces stubs for the nerdctl subcommands (and their
// options); this is expected to be overridden for options that involve paths.
// All options generated this will have their values ignored.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"github.com/sirupsen/logrus"
)

// nerdctl contains the path to the nerdctl binary to run.
var nerdctl = "/usr/local/bin/nerdctl"

// outputPath is the file we should generate.
var outputPath = "../nerdctl_commands_generated.go"

type helpData struct {
	// Commands lists the subcommands available
	Commands []string
	// options available for this command; the key is the long option
	// (`--version`) or the short option (`-v`), and the value is whether the
	// option takes an argument.
	Options map[string]bool
	// mergedOptions includes local options plus inherited options.
	mergedOptions map[string]struct{}
}

// prologueTemplate describes the file header for the generated file.
const prologueTemplate = `
// Code generated by {{ .package }} - DO NOT EDIT.

// package main implements a stub for nerdctl
package main

// commands supported by nerdctl; the key here is a space-separated subcommand
// path to reach the given subcommand (where the root command is empty).
var commands = map[string]commandDefinition {
`

// epilogueTemplate describes the file trailer for the generated file.
const epilogueTemplate = `
}
`

func main() {
	verbose := flag.Bool("verbose", false, "extra logging")
	flag.Parse()
	if *verbose {
		logrus.SetLevel(logrus.TraceLevel)
	}

	output, err := os.Create(outputPath)
	if err != nil {
		logrus.WithError(err).WithField("path", outputPath).Fatal("error creating output")
	}
	defer output.Close()
	//nolint:dogsled // we only require the file name; we can also ignore `ok`, as
	// on failure we just have no useful file name.
	_, filename, _, _ := runtime.Caller(0)
	data := map[string]interface{}{
		"package": filename,
	}
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		data["package"] = buildInfo.Main.Path
	}
	err = template.Must(template.New("").Parse(prologueTemplate)).Execute(output, data)
	if err != nil {
		logrus.WithError(err).Fatal("could not execute prologue")
	}
	err = buildSubcommand([]string{}, helpData{}, output)
	if err != nil {
		logrus.WithError(err).Fatal("could not build subcommands")
	}
	err = template.Must(template.New("").Parse(epilogueTemplate)).Execute(output, data)
	if err != nil {
		logrus.WithError(err).Fatal("could not execute epilogue")
	}
}

// buildSubcommand generates the option parser data for a given subcommand.
// args provides the list of arguments to get to the subcommand; the last
// element in the slice is the name of the subcommand.
// writer is the file to write to for the result; it is expected that `go fmt`
// will be run on it eventually.
func buildSubcommand(args []string, parentData helpData, writer io.Writer) error {
	logrus.WithField("args", args).Trace("building subcommand")
	help, err := getHelp(args)
	if err != nil {
		return fmt.Errorf("Error getting help for %v: %w", args, err)
	}
	subcommands, err := parseHelp(args, help, parentData)
	if err != nil {
		return fmt.Errorf("Error parsing help for %v: %w", args, err)
	}

	err = emitCommand(args, subcommands, writer)
	if err != nil {
		return err
	}

	for _, subcommand := range subcommands.Commands {
		newArgs := make([]string, 0, len(args))
		newArgs = append(newArgs, args...)
		newArgs = append(newArgs, subcommand)
		err := buildSubcommand(newArgs, subcommands, writer)
		if err != nil {
			return err
		}
	}

	return nil
}

// getHelp runs `nerdctl <args...> -help` and returns the result.
func getHelp(args []string) (string, error) {
	newArgs := make([]string, 0, len(args)+1)
	newArgs = append(newArgs, args...)
	newArgs = append(newArgs, "--help")
	cmd := exec.Command(nerdctl, newArgs...)
	cmd.Stderr = os.Stderr
	result, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(result), nil
}

const (
	STATE_OTHER = iota
	STATE_COMMANDS
	STATE_OPTIONS
)

// parseHelp consumes the output of `nerdctl help` (possibly for a subcommand)
// and returns the available subcommands and options.
func parseHelp(args []string, help string, parentData helpData) (helpData, error) {
	result := helpData{Options: make(map[string]bool), mergedOptions: make(map[string]struct{})}
	for k := range parentData.mergedOptions {
		result.mergedOptions[k] = struct{}{}
	}
	state := STATE_OTHER
	for _, line := range strings.Split(help, "\n") {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if line == "" {
			// Skip empty lines (don't switch state either)
			continue
		}
		if !strings.HasPrefix(line, " ") {
			// Line does not start with a space; it's a section header.
			if strings.HasSuffix(strings.ToUpper(line), "COMMANDS:") {
				state = STATE_COMMANDS
			} else if strings.HasSuffix(strings.ToUpper(line), "FLAGS:") {
				state = STATE_OPTIONS
			} else {
				state = STATE_OTHER
			}
			continue
		}
		line = strings.TrimLeftFunc(line, unicode.IsSpace)
		if state == STATE_COMMANDS {
			parts := strings.SplitN(line, "  ", 2)
			if len(parts) < 2 {
				// This line does not contain a command.
				continue
			}
			words := strings.Split(strings.TrimSpace(parts[0]), ", ")
			result.Commands = append(result.Commands, words...)
		} else if state == STATE_OPTIONS {
			parts := strings.SplitN(line, "  ", 2)
			if len(parts) < 2 {
				// This line does not contain an option.
				continue
			}
			// The flags help has the format: `-f, --foo string   Description`
			// In order to figure out if the option takes arguments, we need to
			// parse the whole line first.
			var words []string
			hasOptions := false
			for _, word := range strings.Split(strings.TrimSpace(parts[0]), ", ") {
				spaceIndex := strings.Index(word, " ")
				if spaceIndex > -1 {
					hasOptions = true
					word = word[:spaceIndex]
				}
				words = append(words, word)
			}
			// We may find an inherited flag; skip if the long option exists in
			// the parent
			if len(words) < 1 {
				continue
			}
			if _, ok := parentData.mergedOptions[words[len(words)-1]]; !ok {
				for _, word := range words {
					result.Options[word] = hasOptions
					result.mergedOptions[word] = struct{}{}
				}
			}
		}
	}
	sort.Strings(result.Commands)
	return result, nil
}

// commandTemplate is the text/template template for a single subcommand.
const commandTemplate = `
	{{ printf "%q" .Args }}: {
		commandPath: {{ printf "%q" .Args }},
		subcommands: map[string]struct{} {
			{{- range .Data.Commands }}
				{{ printf "%q" . }}: {},
			{{- end }}
		},
		options: map[string]argHandler {
			{{ range $k, $v := .Data.Options }}
				{{- printf "%q" $k -}}: {{ if $v -}} ignoredArgHandler {{- else -}} nil {{- end -}},
			{{ end }}
		},
	},
`

// commandTemplateInput describes the data that will be fed to commandTemplate.
type commandTemplateInput struct {
	Args string
	Data helpData
}

// emitCommand outputs the golang code to the given writer.  args indicates the
// arguments to reach this subcommand, and data is the parsed help output.
func emitCommand(args []string, data helpData, writer io.Writer) error {
	templateData := commandTemplateInput{
		Args: strings.Join(args, " "),
		Data: data,
	}

	tmpl := template.Must(template.New("").Parse(commandTemplate))
	err := tmpl.Execute(writer, templateData)
	if err != nil {
		return err
	}
	return nil
}
