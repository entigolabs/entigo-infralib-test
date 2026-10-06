package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/entigolabs/entigo-infralib-test/env"
)

func loadConfig(root string) (*env.Config, error) {
	if root == "" {
		var err error
		root, err = env.Root()
		if err != nil {
			return nil, err
		}
	}
	return env.Load(root)
}

func selectEnvironments(config *env.Config, names []string) ([]*env.Environment, error) {
	if len(names) == 0 {
		return config.All(), nil
	}
	var result []*env.Environment
	for _, name := range names {
		e, err := config.Get(name)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, nil
}

func envsCommand(args []string) error {
	fs := flag.NewFlagSet("envs", flag.ContinueOnError)
	root := fs.String("root", "", "repository root (default: INFRALIB_ROOT or the parent holding environments/)")
	cloud := fs.String("cloud", "", "only environments of this cloud")
	names := fs.Bool("names", false, "print names only, one per line")
	tsv := fs.Bool("tsv", false, "print one tab separated line per environment: name, cloud, prefix, region, zone, project, compartment_id, cluster")
	if err := fs.Parse(args); err != nil {
		return exitError{code: 2}
	}
	config, err := loadConfig(*root)
	if err != nil {
		return err
	}
	if *tsv {
		for _, e := range config.All() {
			if *cloud == "" || e.Cloud == *cloud {
				fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, e.Cloud, e.Prefix, e.Region, e.Zone, e.Project, e.CompartmentID, config.ClusterName(e))
			}
		}
		return nil
	}
	if *names {
		for _, e := range config.All() {
			if *cloud == "" || e.Cloud == *cloud {
				fmt.Println(e.Name)
			}
		}
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCLOUD\tPREFIX\tREGION\tCLUSTER\tSTEPS (modules: local/external)")
	for _, e := range config.All() {
		if *cloud != "" && e.Cloud != *cloud {
			continue
		}
		var steps []string
		for _, s := range e.Steps {
			local, external := 0, 0
			for _, ref := range s.Modules {
				m, err := config.Resolve(ref)
				if err != nil {
					return err
				}
				if m.External {
					external++
				} else {
					local++
				}
			}
			steps = append(steps, fmt.Sprintf("%s(%d/%d)", s.Name, local, external))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, e.Cloud, e.Prefix, e.Region, config.ClusterName(e), strings.Join(steps, " "))
	}
	return w.Flush()
}
