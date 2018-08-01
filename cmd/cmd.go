// Copyright © 2017 The VirusTotal CLI authors. All Rights Reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/plusvic/go-ansi"

	"github.com/fatih/color"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/VirusTotal/vt-cli/utils"
	"github.com/VirusTotal/vt-cli/yaml"
	"github.com/VirusTotal/vt-go/vt"
)

var colorScheme = yaml.Colors{
	KeyColor:     color.New(color.FgYellow),
	ValueColor:   color.New(color.FgHiGreen),
	CommentColor: color.New(color.Faint)}

func addAPIKeyFlag(flags *pflag.FlagSet) {
	flags.StringP(
		"apikey", "k", "",
		"api key")
}

func addHostFlag(flags *pflag.FlagSet) {
	flags.String(
		"host", "www.virustotal.com",
		"API host name")
	flags.MarkHidden("host")
}

func addIncludeExcludeFlags(flags *pflag.FlagSet) {
	flags.StringSliceP(
		"include", "i", []string{"**"},
		"include fields matching the provided pattern")

	flags.StringSliceP(
		"exclude", "x", []string{},
		"exclude fields matching the provided pattern")
}

func addThreadsFlag(flags *pflag.FlagSet) {
	flags.IntP(
		"threads", "t", 5,
		"number of threads working in parallel")
}

func addIDOnlyFlag(flags *pflag.FlagSet) {
	flags.BoolP(
		"identifiers-only", "I", false,
		"print identifiers only")
}

func addLimitFlag(flags *pflag.FlagSet) {
	flags.IntP(
		"limit", "n", 10,
		"maximum number of results")
}

func addCursorFlag(flags *pflag.FlagSet) {
	flags.StringP(
		"cursor", "c", "",
		"cursor")
}

func addOutputFlag(flags *pflag.FlagSet) {
	flags.StringP(
		"output", "o", ".",
		"directory where downloaded files are put")
}

func addFilterFlag(flags *pflag.FlagSet) {
	flags.StringP(
		"filter", "f", "",
		"filter")
}

func addVerboseFlag(flags *pflag.FlagSet) {
	flags.BoolP(
		"verbose", "v", false,
		"verbose output")
}

func addYAMLFlag(flags *pflag.FlagSet) {
	flags.BoolP(
		"yaml", "y", false,
		"output in YAML format")
}

// ReadFile reads the specified file and returns its content. If filename is "-"
// the data is read from stdin.
func ReadFile(filename string) ([]byte, error) {
	if filename == "-" {
		return ioutil.ReadAll(os.Stdin)
	}
	return ioutil.ReadFile(filename)
}

// PrintCommandLineWithCursor prints the same command-line that was used for
// executing the program but adding or replacing the --cursor flag with
// the current cursor for the given iterator.
func PrintCommandLineWithCursor(cmd *cobra.Command, it *vt.Iterator) {
	if cursor := it.Cursor(); cursor != "" {
		args := cmd.Flags().Args()
		for i, arg := range args {
			args[i] = fmt.Sprintf("'%s'", arg)
		}
		flags := make([]string, 0)
		cmd.Flags().Visit(func(flag *pflag.Flag) {
			if flag.Name != "cursor" {
				flags = append(flags, fmt.Sprintf("--%s=%v", flag.Name, flag.Value.String()))
			}
		})
		flags = append(flags, fmt.Sprintf("--cursor=%s", cursor))
		color.New(color.Faint).Fprintf(
			ansi.NewAnsiStderr(), "\nMORE WITH:\n%s %s %s\n",
			cmd.CommandPath(), strings.Join(args, " "), strings.Join(flags, " "))
	}
}

// NewAPIClient returns a new utils.APIClient with the API key specified via
// command-line argument or config file.
func NewAPIClient() (*utils.APIClient, error) {
	apikey := viper.GetString("apikey")
	if apikey == "" {
		return nil, errors.New("An API key is needed. Either use the --apikey flag or run \"vt init\" to set up your API key")
	}
	return utils.NewAPIClient(apikey, fmt.Sprintf("vt-cli %s", Version))
}

// ObjectPrinter ...
type ObjectPrinter struct {
	client *utils.APIClient
	cmd    *cobra.Command
	w      *bufio.Writer
}

// NewObjectPrinter ...
func NewObjectPrinter(cmd *cobra.Command) (*ObjectPrinter, error) {
	client, err := NewAPIClient()
	if err != nil {
		return nil, err
	}
	return &ObjectPrinter{
		client: client,
		cmd:    cmd,
		w:      bufio.NewWriter(ansi.NewAnsiStdout())}, nil
}

// Print ...
func (p *ObjectPrinter) Print(objType string, args []string, argRe *regexp.Regexp) error {

	var r utils.StringReader

	if len(args) == 1 && args[0] == "-" {
		r = utils.NewStringIOReader(os.Stdin)
	} else {
		r = utils.NewStringArrayReader(args)
	}

	if argRe != nil {
		r = utils.NewFilteredStringReader(r, argRe)
	}

	filteredArgs := make([]string, 0)
	for s, err := r.ReadString(); s != "" || err == nil; s, err = r.ReadString() {
		filteredArgs = append(filteredArgs, s)
	}

	objectsCh := make(chan *vt.Object)
	errorsCh := make(chan error, len(filteredArgs))

	go p.client.RetrieveObjects(objType, filteredArgs, objectsCh, errorsCh)

	objs := make([]*vt.Object, 0)

	for obj := range objectsCh {
		if viper.GetBool("identifiers-only") {
			fmt.Printf("%s\n", obj.ID)
		} else {
			objs = append(objs, obj)
		}
	}

	if len(objs) > 0 {
		if err := p.PrintObjects(objs); err != nil {
			return err
		}
	}

	for err := range errorsCh {
		fmt.Fprintln(os.Stderr, err)
	}

	return nil
}

func (p *ObjectPrinter) PrintCollection(collection *url.URL) error {
	it, err := p.client.Iterator(collection,
		vt.IteratorOptions{
			Limit:  viper.GetInt("limit"),
			Cursor: viper.GetString("cursor"),
			Filter: viper.GetString("filter"),
		})
	if err != nil {
		return err
	}
	return p.PrintIter(it)
}

func (p *ObjectPrinter) PrintIter(it *vt.Iterator) error {

	objs := make([]*vt.Object, 0)
	for it.Next() {
		obj := it.Get()
		if viper.GetBool("identifiers-only") {
			fmt.Printf("%s\n", obj.ID)
		} else {
			objs = append(objs, obj)
		}
	}

	if err := it.Error(); err != nil {
		return err
	}

	if len(objs) > 0 {
		if err := p.PrintObjects(objs); err != nil {
			return err
		}
	}

	PrintCommandLineWithCursor(p.cmd, it)
	return nil
}

func (p *ObjectPrinter) PrintObject(obj *vt.Object) error {
	objs := make([]*vt.Object, 1)
	objs[0] = obj
	return p.PrintObjects(objs)
}

func (p *ObjectPrinter) PrintObjects(objs []*vt.Object) error {

	list := make([]map[string]interface{}, 0)

	for _, obj := range objs {
		m := obj.Attributes
		if viper.IsSet("include") && viper.IsSet("exclude") {
			m = utils.FilterMap(
				m, viper.GetStringSlice("include"), viper.GetStringSlice("exclude"))
		}
		for name, r := range obj.Relationships {
			if r.IsOneToOne && len(r.RelatedObjects) > 0 {
				m[name] = r.RelatedObjects[0].ID
			} else {
				l := make([]string, 0)
				for _, obj := range r.RelatedObjects {
					l = append(l, obj.ID)
				}
				m[name] = l
			}
		}
		key := fmt.Sprintf("%s <%s>", obj.Type, obj.ID)
		list = append(list, map[string]interface{}{key: m})
	}

	if err := yaml.NewColorEncoder(p.w, colorScheme).Encode(list); err != nil {
		return err
	}

	return p.w.Flush()
}
