// Command firmware manages UniFi OS images for CI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/firmware"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := command().ExecuteContext(ctx); err != nil {
		return fmt.Errorf("run firmware command: %w", err)
	}

	return nil
}

const (
	catalogClientTimeout  = 15 * time.Minute
	catalogRequestTimeout = 45 * time.Second
	// operationBuild names the subcommand that builds firmware test images.
	operationBuild = "build"
)

var errUnknownTrack = errors.New("unknown firmware track")

func command() *cobra.Command {
	root := &cobra.Command{Use: "firmware", Short: "Manage UniFi OS test images.", SilenceUsage: true, SilenceErrors: true}
	var image, model, output string
	root.PersistentFlags().StringVar(&image, "image", os.Getenv("IMAGE"), "Container repository.")
	client := &http.Client{Timeout: catalogClientTimeout}
	pipeline := firmware.Pipeline{Runner: firmware.Commands{Log: os.Stderr}, Client: client, Log: os.Stderr}
	configureTrack(root, &pipeline)
	catalog := &cobra.Command{Use: "catalog", Short: "Discover firmware pairs on the selected track.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), catalogRequestTimeout)
		defer cancel()
		matrix, err := firmware.Discover(ctx, client, firmware.CatalogURL, image, model, time.Now(), pipeline.Track)
		if err != nil {
			return fmt.Errorf("discover firmware pairs: %w", err)
		}

		return writeMatrix(cmd.OutOrStdout(), output, matrix)
	}}
	catalog.Flags().StringVar(&model, "model", "all", "Router model, or all.")
	catalog.Flags().StringVar(&output, "output", "", "Append matrix to GitHub output file.")
	root.AddCommand(catalog)
	published := &cobra.Command{Use: "published", Short: "Select published firmware pairs.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		matrix, err := pipeline.Published(cmd.Context(), image)
		if err != nil {
			return fmt.Errorf("select published firmware pairs: %w", err)
		}

		return writeMatrix(cmd.OutOrStdout(), output, matrix)
	}}
	published.Flags().StringVar(&output, "output", "", "Append matrix to GitHub output file.")
	root.AddCommand(published)
	for _, operation := range []string{operationBuild, "publish"} {
		var model, releases, cache string
		cmd := &cobra.Command{Use: operation, Short: operation + " firmware images.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) (result error) {
			var pair []firmware.Release
			if err := json.Unmarshal([]byte(releases), &pair); err != nil {
				return fmt.Errorf("decode firmware releases: %w", err)
			}
			engine, err := firmware.NewDocker(os.Getenv("GITHUB_ACTOR"), os.Getenv("GH_TOKEN"))
			if err != nil {
				return fmt.Errorf("create Docker image client: %w", err)
			}
			defer func() { result = errors.Join(result, engine.Close()) }()
			pipeline.Images = engine
			if operation == operationBuild {
				return pipeline.Build(cmd.Context(), image, model, cache, pair)
			}

			return pipeline.Publish(cmd.Context(), image, model, pair)
		}}
		cmd.Flags().StringVar(&model, "model", os.Getenv("MODEL"), "Router model.")
		cmd.Flags().StringVar(&releases, "releases", os.Getenv("FIRMWARES"), "Firmware pair JSON.")
		if operation == operationBuild {
			cmd.Flags().StringVar(&cache, "cache", os.Getenv("CACHE"), "Extraction workspace.")
		}
		root.AddCommand(cmd)
	}

	return root
}

func configureTrack(root *cobra.Command, pipeline *firmware.Pipeline) {
	var name string
	root.PersistentFlags().StringVar(&name, "track", "release", "Firmware track: release, beta or pinned.")
	root.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		track, ok := firmware.TrackNamed(name)
		if !ok {
			return fmt.Errorf("%w %q: use release, beta or pinned", errUnknownTrack, name)
		}
		pipeline.Track = track
		return nil
	}
}

func writeMatrix(output io.Writer, filename string, matrix firmware.Matrix) (err error) {
	data, err := json.Marshal(matrix)
	if err != nil {
		return fmt.Errorf("encode firmware matrix: %w", err)
	}
	if filename == "" {
		if _, err := fmt.Fprintln(output, string(data)); err != nil {
			return fmt.Errorf("write firmware matrix: %w", err)
		}

		return nil
	}
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open GitHub output file %s: %w", filename, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if _, err := fmt.Fprintf(file, "matrix=%s\n", data); err != nil {
		return fmt.Errorf("append firmware matrix to %s: %w", filename, err)
	}

	return nil
}
