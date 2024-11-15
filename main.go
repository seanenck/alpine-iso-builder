// Package main is the ISO generator wrapper
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/BurntSushi/toml"
)

type (
	// Command are config commands that require a call and arguments
	Command struct {
		Call      string
		Arguments []string
	}
	// Config handles input ISO build configurations
	Config struct {
		Tags       []string
		Repository struct {
			URL          string
			Repositories []string
		}
		Architecture string
		Name         string
		Source       struct {
			Remote    string
			Directory string
			Template  string
			Arguments []string
		}
		Variables  map[string]Command
		PreProcess []Command
	}
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	inConfig := flag.String("config", "", "configuration file")
	debug := flag.Bool("debug", false, "enable debugging")
	output := flag.String("output", "", "output directory for ISO artifacts")
	flag.Parse()
	b, err := os.ReadFile(*inConfig)
	if err != nil {
		return err
	}
	cfg := Config{}
	decoder := toml.NewDecoder(bytes.NewReader(b))
	md, err := decoder.Decode(&cfg)
	if err != nil {
		return err
	}
	undecoded := md.Undecoded()
	if len(undecoded) > 0 {
		return fmt.Errorf("undecoded fields: %v", undecoded)
	}
	did := false
	isDebug := *debug
	to := *output
	tmp, err := os.MkdirTemp("", "alpine-iso.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for idx := range cfg.Tags {
		did = true
		if err := cfg.run(idx, isDebug, tmp, to); err != nil {
			return err
		}
	}
	if !did {
		return errors.New("no tags processed")
	}
	return nil
}

func (cfg Config) run(idx int, debug bool, dir, to string) error {
	first := idx == 0
	var tag string
	tag = cfg.Tags[idx]
	rawTag := tag
	if strings.Contains(rawTag, ".") {
		parts := strings.Split(tag, ".")
		if len(parts) != 3 {
			return fmt.Errorf("version must be X.Y.Z (have: %s)", parts)
		}
		for _, p := range parts {
			if _, err := strconv.Atoi(p); err != nil {
				return fmt.Errorf("invalid version, non-numeric? %v (%w)", parts, err)
			}
		}
		tag = fmt.Sprintf("v%s", strings.Join(parts[0:2], "."))
	} else {
		if rawTag != "edge" {
			return fmt.Errorf("unknown version/not edge: %s", rawTag)
		}
	}
	cmds := make(map[string][]string)
	if cfg.Variables != nil {
		for n, c := range cfg.Variables {
			t, err := exec.Command(c.Call, c.Arguments...).Output()
			if err != nil {
				return err
			}
			cmds[n] = strings.Split(string(t), "\n")
		}
	}
	obj := struct {
		Name     string
		Arch     string
		Tag      string
		Commands map[string][]string
	}{cfg.Name, cfg.Architecture, tag, cmds}
	t, err := template.New("t").Parse(cfg.Source.Template)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, obj); err != nil {
		return err
	}
	if debug {
		fmt.Printf("profile: %s", buf.String())
	}
	var repositories []string
	url := cfg.Repository.URL
	for _, repo := range cfg.Repository.Repositories {
		r, err := template.New("t").Parse(fmt.Sprintf("%s/%s", url, repo))
		if err != nil {
			return err
		}
		var text bytes.Buffer
		if err := r.Execute(&text, obj); err != nil {
			return err
		}

		repositories = append(repositories, "--repository", text.String())
	}

	clone := filepath.Join(dir, "aports")
	if first {
		cmd := exec.Command("git", "clone", "--depth=1", cfg.Source.Remote, clone)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	root := filepath.Join(clone, cfg.Source.Directory)
	profile := filepath.Join(root, fmt.Sprintf("mkimg.%s.sh", cfg.Name))
	if err := os.WriteFile(profile, buf.Bytes(), 0o755); err != nil {
		return err
	}
	for _, c := range cfg.PreProcess {
		args := c.Arguments
		args = append(args, clone)
		cmd := exec.Command(c.Call, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}

	args := []string{
		filepath.Join(root, "mkimage.sh"),
		"--outdir", to,
		"--arch", cfg.Architecture,
		"--profile", cfg.Name,
		"--tag", rawTag,
	}
	args = append(args, repositories...)
	args = append(args, cfg.Source.Arguments...)
	if debug {
		fmt.Printf("mkimage arguments: %v\n", args)
	}
	cmd := exec.Command("sh", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
