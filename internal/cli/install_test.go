package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/installer"
)

type installTestBackend struct {
	plans   []installer.Plan
	actions []installer.Action
}

func (b *installTestBackend) Apply(_ context.Context, action installer.Action, plan installer.Plan) error {
	b.plans = append(b.plans, plan)
	b.actions = append(b.actions, action)

	return nil
}

func TestInstallDryRunUsesWizardWithoutSystemActions(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, interactive := range []bool{false, true} {
			name := "new"
			if existing {
				name = "existing"
			}
			if interactive {
				name += "-wizard"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				var out bytes.Buffer
				application := &Application{ConfigPath: filepath.Join(dir, "config.json"), StateDir: dir, Out: &out, Err: &out}
				value := config.Default()
				value.Telemetry.Enabled = true
				if existing {
					err := config.Save(application.ConfigPath, value)
					if err != nil {
						t.Fatal(err)
					}
				}
				backend := &installTestBackend{}
				prompts := 0
				command := application.installCommandWith(installDependencies{
					load: func() (config.Config, error) {
						if existing {
							return value, nil
						}

						return config.Config{}, os.ErrNotExist
					},
					legacy:   func() (config.Config, bool, error) { return config.Config{}, false, nil },
					defaults: func() config.Config { return value },
					prompt: func(_ context.Context, v *config.Config) error {
						prompts++
						v.WAN.VLAN = 20
						return nil
					},
					suggest: func(context.Context, config.Config) error {
						t.Fatal("dry-run called external detection")
						return nil
					},
					executable: func() (string, error) { return "/tmp/preview-binary", nil },
					requireRoot: func() error {
						t.Fatal("preview checked root")
						return nil
					}, backend: backend,
				})
				root := &cobra.Command{Use: "udm-iptv"}
				root.AddCommand(command)
				application.instrumentCommands(root)
				args := []string{"install", "--dry-run"}
				if !interactive {
					args = append(args, "--non-interactive")
				}
				root.SetArgs(args)
				if err := root.Execute(); err != nil {
					t.Fatal(err)
				}
				if (prompts == 1) != interactive || len(backend.actions) != 0 || application.monitor != nil {
					t.Fatal("preview invoked wrong dependencies")
				}
				files, err := os.ReadDir(dir)
				wantFiles := 0
				if existing {
					wantFiles = 1
				}
				if err != nil || len(files) != wantFiles {
					t.Fatal("preview wrote files")
				}
				if existing {
					saved, err := config.Load(application.ConfigPath)
					if err != nil || saved.WAN.VLAN != value.WAN.VLAN {
						t.Fatal("preview changed saved configuration")
					}
				}
				if strings.Contains(out.String(), "telemetry is unavailable") {
					t.Fatal("preview initialized telemetry")
				}
				if !strings.Contains(out.String(), "Preview complete") {
					t.Fatal(out.String())
				}
			})
		}
	}
}

func TestInstallSelectionAndFailures(t *testing.T) {
	for _, mode := range []string{"existing", "legacy", "defaults", "invalid-current", "invalid-legacy", "cancel-form", "invalid-form", "not-root"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			application := &Application{ConfigPath: "/data/config.json", StateDir: "/data/iptv", Out: &out, Err: &out}
			backend := &installTestBackend{}
			value := config.Default()
			value.WAN.VLAN = 20
			deps := installDependencies{
				load: func() (config.Config, error) {
					if mode == "invalid-current" {
						return config.Config{}, errors.New("invalid current")
					}
					if mode == "existing" {
						return value, nil
					}

					return config.Config{}, os.ErrNotExist
				},
				legacy: func() (config.Config, bool, error) {
					if mode == "invalid-legacy" {
						return config.Config{}, false, errors.New("invalid legacy")
					}

					return value, mode == "legacy", nil
				},
				defaults: config.DefaultKPN,
				prompt: func(_ context.Context, v *config.Config) error {
					if mode == "cancel-form" {
						return context.Canceled
					}
					if mode == "invalid-form" {
						v.WAN.VLAN = -1
					}

					return nil
				},
				executable: func() (string, error) { return "/tmp/installer", nil },
				requireRoot: func() error {
					if mode == "not-root" {
						return os.ErrPermission
					}

					return nil
				}, backend: backend,
			}
			command := application.installCommandWith(deps)
			command.SetArgs(nil)
			err := command.Execute()
			success := mode == "existing" || mode == "legacy" || mode == "defaults"
			if (err == nil) != success {
				t.Fatalf("unexpected result: %v", err)
			}
			if !success {
				if len(backend.actions) != 0 || strings.Contains(out.String(), "has started") {
					t.Fatal("failed preparation applied installation")
				}

				return
			}
			plan := backend.plans[0]
			if plan.SaveConfig != (mode != "existing") {
				t.Fatal("wrong persistence policy")
			}
			wantVLAN := 20
			if mode == "defaults" {
				wantVLAN = 4
			}
			if plan.Config.WAN.VLAN != wantVLAN {
				t.Fatal("wrong configuration selected")
			}
		})
	}
}
