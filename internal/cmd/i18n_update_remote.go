package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/pascualchavez/echo/internal/odoo"
)

// runI18nUpdateRemote loads the local <mod>/i18n/<lang>.po into a REMOTE
// database: the file is streamed into the odoo container over SSH and
// imported there with overwrite (on series below 19 by a script piped into
// `odoo shell`), then removed. The target's stage
// gates it like any remote write.
func runI18nUpdateRemote(ctx context.Context, opts I18nOpts, from string) error {
	parsed, err := parseI18nArgs(opts.Args, false, true, true)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if parsed.module == "" {
		picked, err := pickModuleSingle(opts.Cfg, opts.Root, opts.Palette, "Module to update")
		if err != nil {
			return err
		}
		parsed.module = picked
	}
	addonsDir, err := resolveModuleDir(opts.Cfg, opts.Root, parsed.module)
	if err != nil {
		return err
	}
	src := defaultExportDest(addonsDir, parsed.module, parsed.lang)
	po, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrTranslationMissing, src)
	}
	if err != nil {
		return err
	}

	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, nil)
	if err != nil {
		return err
	}
	if err := confirmRemoteProd(opts.Palette, "i18n-update", rsc, opts.Args); err != nil {
		return err
	}
	t := rsc.target
	tmp := tmpPathInContainer()
	write := odoo.Cmd{"sh", "-c", "cat > " + tmp}
	if _, err := runSSH(ctx, rsc.sshHost, remoteContainerCmd(rsc.remotePath, t, write), po); err != nil {
		return fmt.Errorf("copy %s into the container: %w", src, err)
	}
	addonsPath := ""
	if t.confPath == "" {
		addonsPath = remoteAddonsPath(ctx, rsc.sshHost, rsc.remotePath, t, rsc.prof.ConfPath, rsc.prof.AddonsPaths)
	}
	confPath, cleanup, err := remoteI18nConf(ctx, rsc.sshHost, rsc.remotePath, t, rsc.conn, addonsPath)
	if err != nil {
		_, _ = runSSH(ctx, rsc.sshHost, remoteContainerCmd(rsc.remotePath, t, odoo.Cmd{"rm", "-f", tmp}), nil)
		return err
	}
	cleanup = append(cleanup, tmp)
	defer func() { _, _ = runSSH(ctx, rsc.sshHost, remoteContainerCmd(rsc.remotePath, t, cleanup), nil) }()
	argv := odoo.UpdateI18n(rsc.conn, t.odooVersion, parsed.module, parsed.lang, tmp, confPath)
	if opts.StreamOut != nil {
		opts.StreamOut("→ " + src + " into " + targetLabel(rsc))
	}
	stdin := odoo.UpdateI18nStdin(t.odooVersion, parsed.lang, tmp)
	return runSSHStream(ctx, rsc.sshHost, remoteContainerCmd(rsc.remotePath, t, argv), stdin, opts.StreamOut)
}
