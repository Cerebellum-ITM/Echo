package cmd

import (
	"os"
	"path/filepath"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/theme"
)

const (
	ResetGlobal  = "global"
	ResetAll     = "all"
	ResetProject = "project"
)

// ResetResult describes what was wiped.
type ResetResult struct {
	Scope    string // "global" | "project" | "all"
	Removed  []string
}

// RunReset asks the user what to wipe and removes the matching files.
// projectKey is the SHA-256 of the current project root, used when the
// user picks the per-project scope. Returns ErrCancelled if the user
// declines the confirmation.
func RunReset(projectKey string, palette theme.Palette) (*ResetResult, error) {
	huhTheme := BuildHuhTheme(palette)

	var scope string
	scopeForm := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(IconDatabase + "  Reset config").
			Description("Pick what to wipe").
			Options(
				huh.NewOption("Global only (theme, logo, compose flavor)", ResetGlobal),
				huh.NewOption("Per-project only (this project's config)", ResetProject),
				huh.NewOption("Everything (global + all projects)", ResetAll),
			).
			Value(&scope),
	)).WithTheme(huhTheme).WithInput(os.Stdin).WithOutput(os.Stdout)

	if err := scopeForm.Run(); err != nil {
		return nil, err
	}

	var confirmed bool
	confirm := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Are you sure?").
			Description("This cannot be undone.").
			Affirmative("Wipe").
			Negative("Cancel").
			Value(&confirmed),
	)).WithTheme(huhTheme).WithInput(os.Stdin).WithOutput(os.Stdout)

	if err := confirm.Run(); err != nil {
		return nil, err
	}
	if !confirmed {
		return nil, ErrCancelled
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".config", "echo")

	result := &ResetResult{Scope: scope}

	switch scope {
	case ResetAll:
		if err := os.RemoveAll(root); err != nil {
			return nil, err
		}
		result.Removed = append(result.Removed, root)
	case ResetGlobal:
		path := filepath.Join(root, "global.toml")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		result.Removed = append(result.Removed, path)
	case ResetProject:
		path := filepath.Join(root, "projects", projectKey+".toml")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		result.Removed = append(result.Removed, path)
	}

	return result, nil
}
