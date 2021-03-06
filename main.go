// Copyright 2020 Carleton University Library.
// All rights reserved.
// Use of this source code is governed by the MIT
// license that can be found in the LICENSE.txt file.

// Command almatoolkit is a set of commands which run against the Alma API.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/cu-library/overridefromenv"

	"github.com/cu-library/almatoolkit/api"
	"github.com/cu-library/almatoolkit/subcommand"
	"github.com/cu-library/almatoolkit/subcommand/bibs/cleanupcallnumbers"
	"github.com/cu-library/almatoolkit/subcommand/bibs/items/cancelrequests"
	"github.com/cu-library/almatoolkit/subcommand/bibs/items/requests"
	"github.com/cu-library/almatoolkit/subcommand/bibs/items/scanin"
	"github.com/cu-library/almatoolkit/subcommand/conf/dump"
)

const (
	// ProjectName is the name of the executable, as displayed to the user in usage and version messages.
	ProjectName = "The Alma Toolkit"

	// EnvPrefix is the prefix for environment variables which override unset flags.
	EnvPrefix = "ALMATOOLKIT_"
)

// A version flag, which should be overwritten when building using ldflags.
var version = "devel"

func main() {
	// Set the prefix of the default logger to the empty string.
	log.SetFlags(0)

	// Define the command line flags
	key := flag.String("key", "", "The Alma API key. You can manage your API keys here: https://developers.exlibrisgroup.com/manage/keys/. Required.")
	host := flag.String("host", api.DefaultAlmaAPIHost, "The Alma API host domain name to use.")
	threshold := flag.Int("threshold", api.DefaultThreshold, "The minimum number of API calls remaining before the tool automatically stops working.")
	printVersion := flag.Bool("version", false, "Print the version then exit.")
	printHelp := flag.Bool("help", false, "Print help documentation then exit.")

	// Subcommands this tool understands.
	registry := subcommand.Registry{}
	registry.Register(dump.Config(EnvPrefix))
	registry.Register(cleanupcallnumbers.Config(EnvPrefix))
	registry.Register(requests.Config(EnvPrefix))
	registry.Register(cancelrequests.Config(EnvPrefix))
	registry.Register(scanin.Config(EnvPrefix))

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "%v\n", ProjectName)
		fmt.Fprintf(flag.CommandLine.Output(), "Version %v\n", version)
		fmt.Fprintf(flag.CommandLine.Output(), "%v [FLAGS] subcommand [SUBCOMMAND FLAGS]\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintln(flag.CommandLine.Output(), "  Environment variables read when flag is unset:")
		flag.VisitAll(func(f *flag.Flag) {
			fmt.Fprintf(flag.CommandLine.Output(), "  %v%v\n", EnvPrefix, strings.ToUpper(f.Name))
		})
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Subcommands:")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		for _, sub := range registry {
			sub.FlagSet.Usage()
		}
	}

	// Process the flags.
	flag.Parse()

	// Quick exit for help and version flags
	if *printVersion {
		fmt.Printf("%v - Version %v.\n", ProjectName, version)
		os.Exit(0)
	}
	if *printHelp {
		flag.CommandLine.SetOutput(os.Stdout)
		flag.Usage()
		os.Exit(0)
	}

	// If any flags have not been set, see if there are
	// environment variables that set them.
	err := overridefromenv.Override(flag.CommandLine, EnvPrefix)
	if err != nil {
		log.Fatalf("FATAL: %v.\n", err)
	}

	// Check that required flags are set.
	if *key == "" {
		log.Fatalln("FATAL: An Alma API key is required.")
	}

	// Was a subcommand provided? Was it valid?
	if len(flag.Args()) == 0 {
		log.Fatalln("FATAL: A subcommand is required.")
	}
	subName := flag.Args()[0]
	sub, valid := registry[subName]
	if !valid {
		log.Fatalf("FATAL: \"%v\" is not a valid subcommand.\n", subName)
	}

	// Ignore errors; FlagSets are all set for ExitOnError.
	_ = sub.FlagSet.Parse(flag.Args()[1:])
	// If any flags have not been set, see if there are
	// environment variables that set them.
	err = overridefromenv.Override(sub.FlagSet, subcommand.EnvPrefix(EnvPrefix, subName))
	if err != nil {
		log.Fatalf("FATAL: %v.\n", err)
	}
	if sub.ValidateFlags != nil {
		err = sub.ValidateFlags()
		if err != nil {
			log.Fatalf("FATAL: %v.\n", err)
		}
	}

	// Keep track of child goroutines.
	var wg sync.WaitGroup

	// Our base context, used to derive all other contexts and propigrate cancel signals.
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the base context if SIGINT or SIGTERM are recieved.
	wg.Add(1)
	go func() {
		defer wg.Done()
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		select {
		case <-sigs:
			log.Println("Cancelling...")
			cancel()
		case <-ctx.Done():
		}
	}()

	// Initialize the API client.
	c := api.NewClient(*host, *key, *threshold)

	// Ensure the provided key can access the API endpoints it needs to for the requested subcommand.
	err = c.CheckAPIandKey(ctx, sub.ReadAccess, sub.WriteAccess)
	if err != nil {
		cancel()
		wg.Wait()
		log.Fatalf("FATAL: API access check failed, %v.\n", err)
	}

	// Run the subcommand.
	err = sub.Run(ctx, c)
	if err != nil {
		cancel()
		wg.Wait()
		log.Fatalf("FATAL: %v.\n", err)
	}

	// No errors, cancel the context, wait on the WaitGroup.
	cancel()
	wg.Wait()
}
