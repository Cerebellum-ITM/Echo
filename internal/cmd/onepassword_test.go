package cmd

import (
	"encoding/json"
	"testing"
)

// A hand-edited item carries sections, notes and custom fields Echo knows
// nothing about. The update must rewrite the password and leave the rest
// standing, or a reset silently destroys whatever the user wrote there.
func TestPatchOPItemPreservesTheRest(t *testing.T) {
	current := []byte(`{
	  "id": "abc123",
	  "title": "Odoo fuentebuena (fuentebuena_prod)",
	  "category": "LOGIN",
	  "vault": {"id": "v1", "name": "Private"},
	  "sections": [{"id": "s1", "label": "Notes"}],
	  "fields": [
	    {"id": "username", "purpose": "USERNAME", "value": "admin"},
	    {"id": "password", "purpose": "PASSWORD", "value": "old"},
	    {"id": "custom1", "section": {"id": "s1"}, "label": "ticket", "value": "OPS-42"}
	  ],
	  "urls": [{"label": "website", "primary": true, "href": "https://old.example.com"}],
	  "tags": ["manual"]
	}`)

	patched, id, existingURL, existingTags, err := patchOPItem(current, "newpassword")
	if err != nil {
		t.Fatalf("patchOPItem: %v", err)
	}
	if id != "abc123" {
		t.Errorf("id = %q, want abc123", id)
	}
	if existingURL != "https://old.example.com" {
		t.Errorf("existingURL = %q, want the item's own href", existingURL)
	}
	if len(existingTags) != 1 || existingTags[0] != "manual" {
		t.Errorf("existingTags = %v, want the item's own tags", existingTags)
	}

	var got map[string]any
	if err := json.Unmarshal(patched, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["sections"] == nil {
		t.Error("sections were dropped")
	}
	if got["title"] != "Odoo fuentebuena (fuentebuena_prod)" {
		t.Errorf("title = %v, want it untouched", got["title"])
	}

	fields, _ := got["fields"].([]any)
	if len(fields) != 3 {
		t.Fatalf("got %d fields, want the original 3", len(fields))
	}
	byID := map[string]map[string]any{}
	for _, f := range fields {
		field := f.(map[string]any)
		byID[field["id"].(string)] = field
	}
	if byID["password"]["value"] != "newpassword" {
		t.Errorf("password = %v, want newpassword", byID["password"]["value"])
	}
	if byID["custom1"]["value"] != "OPS-42" {
		t.Errorf("custom field = %v, want it preserved", byID["custom1"]["value"])
	}
	if byID["custom1"]["section"] == nil {
		t.Error("the custom field lost its section")
	}

	// The website is declared with --url, so it must not linger in the
	// body and re-appear as a stale second address.
	if _, ok := got["urls"]; ok {
		t.Error("the patched body still carries urls")
	}
	if _, ok := got["tags"]; ok {
		t.Error("the patched body still carries tags")
	}
}

// An edit re-declares the whole tag list, so the tags someone added by
// hand have to survive alongside the ones Echo applies.
func TestMergeTagsKeepsTheOnesAlreadyThere(t *testing.T) {
	got := mergeTags([]string{"manual", "echo"}, []string{"echo", "odoo", "do-mx-01"})
	want := []string{"manual", "echo", "odoo", "do-mx-01"}
	if len(got) != len(want) {
		t.Fatalf("mergeTags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mergeTags = %v, want %v", got, want)
		}
	}
}

// The primary href is what gets re-declared when the instance reports no
// usable web.base.url, so an update must never blank an item's website.
func TestPatchOPItemReportsThePrimaryURL(t *testing.T) {
	current := []byte(`{"id":"x","fields":[{"id":"password","purpose":"PASSWORD","value":"old"}],
	  "urls":[{"label":"other","href":"https://second.example.com"},
	          {"label":"website","primary":true,"href":"https://kept.example.com"}]}`)

	_, _, existingURL, _, err := patchOPItem(current, "new")
	if err != nil {
		t.Fatalf("patchOPItem: %v", err)
	}
	if existingURL != "https://kept.example.com" {
		t.Errorf("existingURL = %q, want the primary one", existingURL)
	}
}

func TestPatchOPItemAddsMissingPasswordField(t *testing.T) {
	patched, _, _, _, err := patchOPItem([]byte(`{"id":"x","fields":[{"id":"username","value":"admin"}]}`), "new")
	if err != nil {
		t.Fatalf("patchOPItem: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(patched, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fields, _ := got["fields"].([]any)
	if len(fields) != 2 {
		t.Fatalf("got %d fields, want the username plus a new password", len(fields))
	}
	added := fields[1].(map[string]any)
	if added["value"] != "new" || added["purpose"] != "PASSWORD" {
		t.Errorf("added field = %v, want a PASSWORD field holding the new value", added)
	}
}

func TestPatchOPItemRejectsAnItemWithoutID(t *testing.T) {
	if _, _, _, _, err := patchOPItem([]byte(`{"fields":[]}`), "new"); err == nil {
		t.Error("want an error for an item with no id")
	}
}

func TestIsLocalBaseURL(t *testing.T) {
	cases := map[string]bool{
		"http://localhost:8069":    true,
		"http://127.0.0.1:8069":    true,
		"https://0.0.0.0":          true,
		"https://odoo.example.com": false,
		"https://localhost.mx":     false,
	}
	for url, want := range cases {
		if got := isLocalBaseURL(url); got != want {
			t.Errorf("isLocalBaseURL(%q) = %v, want %v", url, got, want)
		}
	}
}
