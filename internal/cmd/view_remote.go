package cmd

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/odoo"
)

// remoteView carries a resolved remote target for browsing a module's
// deployed files over SSH. It is the shared handle for remote `view`
// (Unit 79) and `compare` (Unit 80): both list/read module files from the
// server through the same transport, so they build one of these once and
// pass it to the remote* helpers below.
type remoteView struct {
	rsc remoteShellContext
}

// displayFrom is the label used in log frames for a remote run: the named
// connect target when there is one (`--from prod`), else the ssh host (a
// bare `--remote` resolves the link binding, which has no name).
func (rv remoteView) displayFrom() string {
	if rv.rsc.fromName != "" {
		return rv.rsc.fromName
	}
	return rv.rsc.sshHost
}

// resolveRemoteView resolves the remote target and its Echo profile — the
// preamble every remote file read needs. It reuses resolveRemoteShell
// (target + profile + conn), so the module base/list/read helpers get the
// server's composeCmd/OdooContainer/AddonsPaths without a prod gate: view
// and compare only ever cat/find/test files.
func resolveRemoteView(ctx context.Context, opts ViewOpts, from string) (remoteView, error) {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, nil)
	if err != nil {
		return remoteView{}, err
	}
	return remoteView{rsc: rsc}, nil
}

// remoteModuleBase locates the addons path holding <module>/__manifest__.py
// on the remote target, mirroring moduleBase's host/container split for a
// deployment reached over SSH. It probes in two passes:
//
//  1. The remote host filesystem (host-mode remote — the deploy assumption,
//     code pulled server-side under remotePath): each relative addons path
//     is joined under remotePath and probed with `ssh <host> test -f …`.
//  2. The container (conf-mode remote — addons live only in the image):
//     each absolute addons path is probed with `compose exec test -f …`.
//
// The first hit fixes the transport (inContainer) for the subsequent
// find/cat of the same invocation.
func remoteModuleBase(ctx context.Context, rv remoteView, module string) (base string, inContainer bool, err error) {
	hostPaths := rv.rsc.prof.AddonsPaths
	if len(hostPaths) == 0 {
		hostPaths = []string{".", "addons", "custom"}
	}
	for _, b := range hostPaths {
		if path.IsAbs(b) {
			continue // absolute → a container path, tried in the second pass
		}
		p := path.Join(rv.rsc.remotePath, b, module, "__manifest__.py")
		if _, e := runSSH(ctx, rv.rsc.sshHost, "test -f "+shellQuote(p), nil); e == nil {
			return b, false, nil
		}
	}
	for _, b := range rv.rsc.prof.AddonsPaths {
		if !path.IsAbs(b) {
			continue
		}
		p := strings.TrimRight(b, "/") + "/" + module + "/__manifest__.py"
		if _, e := runSSH(ctx, rv.rsc.sshHost,
			remoteContainerCmd(rv.rsc.remotePath, rv.rsc.target, odoo.Cmd{"test", "-f", p}), nil); e == nil {
			return b, true, nil
		}
	}
	return "", false, fmt.Errorf("module %q not found on remote target", module)
}

// remoteModuleDir returns the module's directory on the winning transport:
// joined under remotePath for the host filesystem, or the absolute base for
// the container.
func remoteModuleDir(rv remoteView, base, module string, inContainer bool) string {
	if inContainer {
		return strings.TrimRight(base, "/") + "/" + module
	}
	return path.Join(rv.rsc.remotePath, base, module)
}

// remoteModuleFiles lists a remote module's files as module-relative,
// sorted paths — `find <dir> -type f` over the base's transport, trimmed
// and filtered through skipViewPath exactly like the local moduleFiles.
func remoteModuleFiles(ctx context.Context, rv remoteView, base, module string, inContainer bool) ([]string, error) {
	dir := remoteModuleDir(rv, base, module, inContainer)
	var (
		out []byte
		err error
	)
	if inContainer {
		out, err = runSSH(ctx, rv.rsc.sshHost,
			remoteContainerCmd(rv.rsc.remotePath, rv.rsc.target, odoo.Cmd{"find", dir, "-type", "f"}), nil)
	} else {
		out, err = runSSH(ctx, rv.rsc.sshHost, "find "+shellQuote(dir)+" -type f", nil)
	}
	if err != nil {
		return nil, err
	}

	prefix := dir + "/"
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		rel := strings.TrimPrefix(strings.TrimSpace(line), prefix)
		if rel == "" || skipViewPath(rel) {
			continue
		}
		files = append(files, rel)
	}
	sort.Strings(files)
	return files, nil
}

// remoteReadModuleFile returns the contents of a module-relative file on
// the remote target, over the base's transport.
func remoteReadModuleFile(ctx context.Context, rv remoteView, base, module string, inContainer bool, rel string) (string, error) {
	dir := remoteModuleDir(rv, base, module, inContainer)
	p := dir + "/" + rel
	var (
		out []byte
		err error
	)
	if inContainer {
		out, err = runSSH(ctx, rv.rsc.sshHost,
			remoteContainerCmd(rv.rsc.remotePath, rv.rsc.target, odoo.Cmd{"cat", p}), nil)
	} else {
		out, err = runSSH(ctx, rv.rsc.sshHost, "cat "+shellQuote(p), nil)
	}
	if err != nil {
		return "", err
	}
	return string(out), nil
}
