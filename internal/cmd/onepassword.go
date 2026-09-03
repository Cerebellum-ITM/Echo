package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// errOPItemMissing distinguishes "the vault has no such item" from a real
// failure. Degrading every error to "missing" would turn a network blip
// into a duplicate item.
var errOPItemMissing = errors.New("item not found in the vault")

// opAvailable reports whether the 1Password CLI is installed and holds an
// unlocked session. Callers run it BEFORE changing anything: a locked
// vault discovered after the reset would leave a fresh password installed
// and nowhere to store it.
func opAvailable(ctx context.Context) error {
	if _, err := exec.LookPath("op"); err != nil {
		return errors.New("the 1Password CLI (op) is not installed — see https://developer.1password.com/docs/cli/get-started/")
	}
	if _, err := opRun(ctx, nil, "whoami"); err != nil {
		return fmt.Errorf("no 1Password session — run `op signin` first: %w", err)
	}
	return nil
}

// opSaveLogin stores username/password under title, updating the item
// when one already carries that title and creating it otherwise. url is
// attached as the item's website when non-empty. created reports which of
// the two paths ran.
func opSaveLogin(ctx context.Context, vault, title, username, password, url string) (created bool, err error) {
	existing, err := opItemGet(ctx, vault, title)
	switch {
	case errors.Is(err, errOPItemMissing):
		return true, opItemCreate(ctx, vault, title, username, password, url)
	case err != nil:
		return false, err
	}
	return false, opItemEdit(ctx, vault, existing, password, url)
}

// opItemGet returns the raw JSON of the item titled title.
func opItemGet(ctx context.Context, vault, title string) ([]byte, error) {
	args := []string{"item", "get", title, "--format", "json"}
	if vault != "" {
		args = append(args, "--vault", vault)
	}
	out, err := opRun(ctx, nil, args...)
	if err != nil {
		// The CLI has no exit code for this case, so the message is the
		// only signal (verified against op 2.34).
		if strings.Contains(err.Error(), "isn't an item") {
			return nil, errOPItemMissing
		}
		return nil, err
	}
	return out, nil
}

func opItemCreate(ctx context.Context, vault, title, username, password, url string) error {
	body, err := opCreateBody(title, username, password, url)
	if err != nil {
		return err
	}
	args := []string{"item", "create", "-"}
	if vault != "" {
		args = append(args, "--vault", vault)
	}
	if url != "" {
		args = append(args, "--url", url)
	}
	_, err = opRun(ctx, body, args...)
	return err
}

// opCreateBody builds the Login item template `op item create -` reads
// from stdin. Only the item's own structure and the secret go here; the
// website is declared with --url, the flag both create and edit expose,
// so the same call shape works on either path. An address is not a
// secret — the password is, and that is what stdin exists for.
//
// op only recognizes the `-` template when stdin is a pipe; a regular
// file redirected in makes it demand --category instead. os/exec always
// gives the child a pipe here, so this holds by construction.
func opCreateBody(title, username, password, url string) ([]byte, error) {
	item := map[string]any{
		"title":    title,
		"category": "LOGIN",
		"fields": []map[string]any{
			{"id": "username", "type": "STRING", "purpose": "USERNAME", "label": "username", "value": username},
			{"id": "password", "type": "CONCEALED", "purpose": "PASSWORD", "label": "password", "value": password},
		},
	}
	return json.Marshal(item)
}

// opItemEdit rewrites the password (and website) of an item the vault
// already holds. The whole item is patched and sent back so the sections,
// notes and custom fields someone added by hand survive the update.
func opItemEdit(ctx context.Context, vault string, current []byte, password, url string) error {
	patched, id, existingURL, err := patchOPItem(current, password)
	if err != nil {
		return err
	}
	if url == "" {
		url = existingURL
	}
	args := []string{"item", "edit", id}
	if vault != "" {
		args = append(args, "--vault", vault)
	}
	if url != "" {
		args = append(args, "--url", url)
	}
	_, err = opRun(ctx, patched, args...)
	return err
}

// patchOPItem rewrites the password on an item's JSON, leaving every
// other member untouched, and reports the website the item already
// carries so the caller can re-declare it through --url. The `urls`
// member is dropped from the body for the reason opCreateBody explains.
func patchOPItem(current []byte, password string) (patched []byte, id, existingURL string, err error) {
	var raw map[string]any
	if err := json.Unmarshal(current, &raw); err != nil {
		return nil, "", "", fmt.Errorf("read the 1Password item: %w", err)
	}
	id, _ = raw["id"].(string)
	if id == "" {
		return nil, "", "", errors.New("the 1Password item carries no id")
	}
	existingURL = primaryURL(raw)
	delete(raw, "urls")

	fields, _ := raw["fields"].([]any)
	found := false
	for _, f := range fields {
		field, ok := f.(map[string]any)
		if !ok {
			continue
		}
		if field["purpose"] == "PASSWORD" || field["id"] == "password" {
			field["value"] = password
			found = true
		}
	}
	if !found {
		fields = append(fields, map[string]any{
			"id": "password", "type": "CONCEALED", "purpose": "PASSWORD",
			"label": "password", "value": password,
		})
	}
	raw["fields"] = fields

	patched, err = json.Marshal(raw)
	return patched, id, existingURL, err
}

// primaryURL returns the item's primary website, falling back to the
// first one listed.
func primaryURL(raw map[string]any) string {
	urls, _ := raw["urls"].([]any)
	first := ""
	for _, u := range urls {
		entry, ok := u.(map[string]any)
		if !ok {
			continue
		}
		href, _ := entry["href"].(string)
		if href == "" {
			continue
		}
		if entry["primary"] == true {
			return href
		}
		if first == "" {
			first = href
		}
	}
	return first
}

// opRun invokes the 1Password CLI, feeding stdin when given. Sensitive
// values travel through stdin and never through argv, which `op item
// create --help` warns is visible to other processes.
func opRun(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "op", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("op %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("op %s: %w", args[0], err)
	}
	return out, nil
}
