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

	"github.com/kjanat/udm-iptv/internal/firmware"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	err := command().ExecuteContext(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command() *cobra.Command {
	root := &cobra.Command{Use: "firmware", Short: "Manage UniFi OS test images.", SilenceUsage: true, SilenceErrors: true}
	var image, model, output string
	root.PersistentFlags().StringVar(&image, "image", os.Getenv("IMAGE"), "Container repository.")
	client := &http.Client{Timeout: 15 * time.Minute}
	pipeline := firmware.Pipeline{Runner: firmware.Commands{Log: os.Stderr}, Client: client, Log: os.Stderr}
	catalog := &cobra.Command{Use: "catalog", Short: "Discover stable firmware pairs.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
		defer cancel()
		matrix, err := firmware.Discover(ctx, client, firmware.CatalogURL, image, model, time.Now())
		if err != nil {
			return err
		}

		return writeMatrix(cmd.OutOrStdout(), output, matrix)
	}}
	catalog.Flags().StringVar(&model, "model", "all", "Router model, or all.")
	catalog.Flags().StringVar(&output, "output", "", "Append matrix to GitHub output file.")
	root.AddCommand(catalog)
	published := &cobra.Command{Use: "published", Short: "Select published firmware pairs.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		matrix, err := pipeline.Published(cmd.Context(), image)
		if err != nil {
			return err
		}

		return writeMatrix(cmd.OutOrStdout(), output, matrix)
	}}
	published.Flags().StringVar(&output, "output", "", "Append matrix to GitHub output file.")
	root.AddCommand(published)
	for _, operation := range []string{"build", "publish"} {
		var model, releases, cache string
		cmd := &cobra.Command{Use: operation, Short: operation + " firmware images.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) (result error) {
			var pair []firmware.Release
			if err := json.Unmarshal([]byte(releases), &pair); err != nil {
				return fmt.Errorf("decode firmware releases: %w", err)
			}
			engine, err := firmware.NewDocker(os.Getenv("GITHUB_ACTOR"), os.Getenv("GH_TOKEN"))
			if err != nil {
				return err
			}
			defer func() {
				if err := engine.Close(); err != nil {
					result = errors.Join(result, fmt.Errorf("close Docker client: %w", err))
				}
			}()
			pipeline.Images = engine
			if operation == "build" {
				return pipeline.Build(cmd.Context(), image, model, cache, pair)
			}

			return pipeline.Publish(cmd.Context(), image, model, pair)
		}}
		cmd.Flags().StringVar(&model, "model", os.Getenv("MODEL"), "Router model.")
		cmd.Flags().StringVar(&releases, "releases", os.Getenv("FIRMWARES"), "Firmware pair JSON.")
		if operation == "build" {
			cmd.Flags().StringVar(&cache, "cache", os.Getenv("CACHE"), "Extraction workspace.")
		}
		root.AddCommand(cmd)
	}

	return root
}

func writeMatrix(output io.Writer, filename string, matrix firmware.Matrix) (err error) {
	data, err := json.Marshal(matrix)
	if err != nil {
		return err
	}
	if filename == "" {
		_, err = fmt.Fprintln(output, string(data))

		return err
	}
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	_, err = fmt.Fprintf(file, "matrix=%s\n", data)

	return err
}
