package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// runDeploySetGitBranch is `deploy --set-git-branch <name>`: name the branch a
// git-deploy target's code lives on. Named after the config key it writes, both
// because that is this repo's convention for setters and because `promote
// --set-branch` already means something else — the local accumulation branch,
// on a different machine.
//
// Config-only by default, like the other setters: the branch is persisted and
// the next deploy's bootstrap creates it at the checkout's current HEAD and
// switches to it, leaving the working tree and the dirty overlay untouched.
// --rename does the remote half now, so the old branch is not left behind
// pointing at the last deployed SHA.
func runDeploySetGitBranch(ctx context.Context, opts DeployOpts, p deployArgs) (DeployResult, error) {
	name := strings.TrimSpace(p.setGitBranch)
	sshHost, remotePath, fromName, err := resolveDeployRemote(opts, p.from)
	if err != nil {
		return DeployResult{}, err
	}
	g := resolveGitDeploy(opts.Cfg, fromName, sshHost, remotePath)
	if !g.enabled {
		return DeployResult{}, fmt.Errorf("%w: --set-git-branch needs a git-deploy target (set git_deploy on it)", ErrUsage)
	}
	if g.branch == name {
		opts.log("INFO", "git", "deploy branch unchanged", "", [2]string{"branch", name})
		return DeployResult{Target: fromName, JSON: p.jsonOut}, nil
	}

	if err := saveGitBranch(opts, fromName, sshHost, remotePath, name); err != nil {
		return DeployResult{}, err
	}
	opts.log("INFO", "git", "deploy branch set", "",
		[2]string{"branch", name}, [2]string{"prev", g.branch})

	if !p.rename {
		opts.log("INFO", "git", "the next deploy creates it on the server at the current HEAD", "",
			[2]string{"hint", "pass --rename to move the existing branch instead"})
		return DeployResult{Target: fromName, JSON: p.jsonOut}, nil
	}

	absDir := absGitDir(remotePath, g.path)
	if !gitRemoteBranchExists(ctx, sshHost, absDir, g.branch) {
		opts.log("INFO", "git", "nothing to rename — the server has no such branch yet", "",
			[2]string{"branch", g.branch})
		return DeployResult{Target: fromName, JSON: p.jsonOut}, nil
	}
	if gitRemoteBranchExists(ctx, sshHost, absDir, name) {
		return DeployResult{}, fmt.Errorf("%w: %s already exists on the server — rename or delete it there first", ErrUsage, name)
	}
	if !p.force {
		if err := confirmRenameBranch(opts.Palette, sshHost, g.branch, name); err != nil {
			return DeployResult{}, err
		}
	}
	if err := gitRenameBranch(ctx, sshHost, absDir, g.branch, name); err != nil {
		return DeployResult{}, err
	}
	opts.log("INFO", "git", "deploy branch renamed on the server", "",
		[2]string{"from", g.branch}, [2]string{"to", name})
	return DeployResult{Target: fromName, JSON: p.jsonOut}, nil
}

// saveGitBranch persists the new branch where the target's git config lives: a
// named connect target's own entry, or the project's [connect] binding.
func saveGitBranch(opts DeployOpts, fromName, sshHost, remotePath, branch string) error {
	if opts.Cfg != nil {
		for _, t := range opts.Cfg.ConnectTargets {
			if t.Name == fromName || (t.GitDeploy && t.SSHHost == sshHost && t.RemotePath == remotePath) {
				t.GitBranch = branch
				if err := config.SaveConnectTarget(t); err != nil {
					return fmt.Errorf("save deploy branch on target %s: %w", t.Name, err)
				}
				for i := range opts.Cfg.ConnectTargets {
					if opts.Cfg.ConnectTargets[i].Name == t.Name {
						opts.Cfg.ConnectTargets[i].GitBranch = branch
					}
				}
				return nil
			}
		}
		if opts.Cfg.ConnectGitDeploy {
			cfgCopy := *opts.Cfg
			cfgCopy.ConnectGitBranch = branch
			if err := config.SaveProject(&cfgCopy); err != nil {
				return fmt.Errorf("save deploy branch on the project binding: %w", err)
			}
			opts.Cfg.ConnectGitBranch = branch
			return nil
		}
	}
	return fmt.Errorf("%w: could not tell which target to write — pass --from <target>", ErrUsage)
}

// confirmRenameBranch gates the remote rename. The rename itself is safe (git
// moves the ref, not the working tree), but it is still a change to a live
// server's checkout.
func confirmRenameBranch(palette theme.Palette, host, from, to string) error {
	if err := requireTTY("pass --force to rename without a prompt"); err != nil {
		return err
	}
	accent := lipgloss.NewStyle().Foreground(palette.Accent).Bold(true)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Rename " + accent.Render(from) + " to " + accent.Render(to) + " on " + host).
			Description("The ref moves; the working tree, the index and the dirty overlay stay as they are.").
			Affirmative("Rename").
			Negative("Cancel").
			Value(&confirmed),
	)).
		WithTheme(BuildHuhTheme(palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return err
	}
	if !confirmed {
		return ErrCancelled
	}
	return nil
}
