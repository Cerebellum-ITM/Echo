package cmd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

// moduleOpts is a local project whose Odoo subprocess cannot start: every
// case below must fail before it, so a missing binary never matters.
func moduleOpts(t *testing.T, root string, args ...string) ModulesOpts {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return ModulesOpts{
		Cfg: &config.Config{
			ProjectKey:    "k",
			DBName:        "demo",
			DBContainer:   "db",
			OdooContainer: "odoo",
			ComposeCmd:    "/nonexistent-echo-test-binary",
		},
		Root:      root,
		Args:      args,
		StreamOut: func(string) {},
		OnResolve: func([]string) { t.Fatal("the run reached Odoo") },
	}
}

func TestModuleCommandsRejectUsageErrors(t *testing.T) {
	type run func(context.Context, ModulesOpts) ([]string, error)
	cases := []struct {
		name string
		run  run
		args []string
	}{
		{"install unknown flag", RunInstall, []string{"sale", "--json"}},
		{"install bad level", RunInstall, []string{"sale", "--level", "loud"}},
		{"install bare level", RunInstall, []string{"sale", "--level"}},
		{"update unknown flag", RunUpdate, []string{"sale", "--json"}},
		{"update valueless --from", RunUpdate, []string{"sale", "--from"}},
		{"update last with modules", RunUpdate, []string{"--last", "sale"}},
		{"update last without record", RunUpdate, []string{"--last"}},
		{"uninstall unknown flag", RunUninstall, []string{"sale", "--with-demo"}},
		{"test unknown flag", RunTest, []string{"sale", "--whatever"}},
		{"test tags without value", RunTest, []string{"sale", "--tags"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.run(context.Background(), moduleOpts(t, t.TempDir(), tc.args...))
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("err = %v, want ErrUsage", err)
			}
		})
	}
}

func TestUsageSentinelsKeepTheirIdentity(t *testing.T) {
	for _, sentinel := range []error{ErrInvalidLogLevel, ErrNoLastUpdate, ErrLastExclusive} {
		if !errors.Is(sentinel, ErrUsage) {
			t.Errorf("%v does not wrap ErrUsage", sentinel)
		}
	}
	_, _, err := extractLevel([]string{"--level=loud"})
	if !errors.Is(err, ErrInvalidLogLevel) || !errors.Is(err, ErrUsage) {
		t.Errorf("extractLevel err = %v, want ErrInvalidLogLevel and ErrUsage", err)
	}
}

func stubDatabaseModules(t *testing.T, names []string, err error) *int {
	t.Helper()
	calls := 0
	orig := databaseModuleNames
	databaseModuleNames = func(context.Context, ModulesOpts) ([]string, error) {
		calls++
		return names, err
	}
	t.Cleanup(func() { databaseModuleNames = orig })
	return &calls
}

func TestValidateModuleNames(t *testing.T) {
	root := nestedRepo(t, "addons/sale_custom", "addons/stock_custom")
	ctx := context.Background()

	t.Run("project addon passes without asking the database", func(t *testing.T) {
		calls := stubDatabaseModules(t, nil, errors.New("unreachable"))
		if err := validateModuleNames(ctx, moduleOpts(t, root), []string{"sale_custom"}); err != nil {
			t.Fatalf("err = %v", err)
		}
		if *calls != 0 {
			t.Errorf("database queried %d times", *calls)
		}
	})

	t.Run("core module known to the database passes", func(t *testing.T) {
		stubDatabaseModules(t, []string{"base", "sale"}, nil)
		if err := validateModuleNames(ctx, moduleOpts(t, root), []string{"sale_custom", "sale"}); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("typo is a usage error with close names", func(t *testing.T) {
		stubDatabaseModules(t, []string{"base", "sale"}, nil)
		err := validateModuleNames(ctx, moduleOpts(t, root), []string{"sale_custon"})
		if !errors.Is(err, ErrUsage) {
			t.Fatalf("err = %v, want ErrUsage", err)
		}
		var hinted interface{ Hint() string }
		if !errors.As(err, &hinted) || !strings.Contains(hinted.Hint(), "sale_custom") {
			t.Fatalf("hint = %v, want sale_custom named", err)
		}
		if !strings.Contains(err.Error(), `"sale_custon"`) {
			t.Errorf("message %q does not name the module", err)
		}
	})

	t.Run("no close name points at the module list", func(t *testing.T) {
		stubDatabaseModules(t, []string{"base"}, nil)
		var hinted interface{ Hint() string }
		err := validateModuleNames(ctx, moduleOpts(t, root), []string{"zzzzzzzz"})
		if !errors.As(err, &hinted) || !strings.Contains(hinted.Hint(), "modules") {
			t.Fatalf("err = %v, want the modules hint", err)
		}
	})

	t.Run("unreadable database skips the check", func(t *testing.T) {
		stubDatabaseModules(t, nil, errors.New("db down"))
		if err := validateModuleNames(ctx, moduleOpts(t, root), []string{"nope"}); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("no discovered addons skips the check", func(t *testing.T) {
		calls := stubDatabaseModules(t, []string{"base"}, nil)
		if err := validateModuleNames(ctx, moduleOpts(t, t.TempDir()), []string{"nope"}); err != nil {
			t.Fatalf("err = %v", err)
		}
		if *calls != 0 {
			t.Errorf("database queried %d times", *calls)
		}
	})
}

func TestModuleCommandsRejectUnknownModules(t *testing.T) {
	root := nestedRepo(t, "addons/sale_custom")
	stubDatabaseModules(t, []string{"base", "sale"}, nil)
	type run func(context.Context, ModulesOpts) ([]string, error)
	for name, fn := range map[string]run{"install": RunInstall, "update": RunUpdate, "test": RunTest} {
		t.Run(name, func(t *testing.T) {
			_, err := fn(context.Background(), moduleOpts(t, root, "sale_custon"))
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("err = %v, want ErrUsage", err)
			}
		})
	}
}

func TestCloseModuleNames(t *testing.T) {
	candidates := []string{"sale", "sale_stock", "sale_management", "sale_custom", "stock", "account"}
	cases := []struct {
		name string
		want []string
	}{
		{"sael", []string{"sale"}},
		{"stok", []string{"stock"}},
		{"sale", []string{"sale_stock", "sale_custom", "sale_management"}},
		{"custom", []string{"sale_custom"}},
		{"zzz", []string{}},
	}
	for _, tc := range cases {
		if got := closeModuleNames(tc.name, candidates); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("closeModuleNames(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"sale", "sale", 0},
		{"sale", "sael", 2},
		{"stock", "stok", 1},
		{"", "abc", 3},
	}
	for _, tc := range cases {
		if got := editDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestTestTags(t *testing.T) {
	if got := TestTags([]string{"sale"}, []string{"sale", "stock"}); got != "/sale,/stock" {
		t.Errorf("default tags = %q", got)
	}
	if got := TestTags([]string{"sale", "--tags", ":TestX"}, []string{"sale"}); got != ":TestX" {
		t.Errorf("explicit tags = %q", got)
	}
}
