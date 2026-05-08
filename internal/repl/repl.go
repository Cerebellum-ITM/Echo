package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/pascualchavez/echo/internal/banner"
	"github.com/pascualchavez/echo/internal/theme"
)

// Line is a single piece of styled output.
type Line struct {
	Kind string // out, dim, faint, info, ok, warn, err, accent, label
	Text string
}

type session struct {
	styles     theme.Styles
	palette    theme.Palette
	bannerOpts banner.Opts
	project    string
	id         string
	stage      theme.Stage
	version    string
}

// Start renders the header and enters the interactive prompt loop.
func Start(s theme.Styles, p theme.Palette, project, id string, stage theme.Stage, version, themeName, username, cwd string) {
	opts := banner.Opts{
		Version:  "0.1.0",
		Username: username,
		Theme:    themeName,
		Stage:    string(stage),
		Path:     cwd,
	}

	sess := &session{
		styles:     s,
		palette:    p,
		bannerOpts: opts,
		project:    project,
		id:         id,
		stage:      stage,
		version:    version,
	}

	fmt.Println(banner.Render(s, p, opts))

	ctx := context.Background()
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print(sess.renderPrompt())

		input, err := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if err == io.EOF {
			fmt.Println()
			break
		}
		if err != nil {
			sess.print(Line{Kind: "err", Text: "read error: " + err.Error()})
			break
		}

		if input == "" {
			continue
		}

		if input == "exit" || input == "quit" {
			break
		}

		sess.dispatch(ctx, input)
	}

	fmt.Println(s.Dim.Render("Goodbye!"))
}

func (sess *session) renderPrompt() string {
	s := sess.styles
	projectID := sess.project + "-" + sess.id
	return s.Project.Render(projectID) +
		s.Out.Render(" [") +
		s.Bracket.Render(string(sess.stage)+"/"+sess.version+".0") +
		s.Out.Render("]:") +
		s.Tilde.Render("~") +
		s.Dollar.Render("$ ")
}

func (sess *session) dispatch(ctx context.Context, input string) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return
	}

	cmd, args := parts[0], parts[1:]

	switch cmd {
	case "ls":
		sess.runLS(ctx, args)
	case "clear":
		fmt.Print("\033[H\033[2J")
		fmt.Println(banner.Render(sess.styles, sess.palette, sess.bannerOpts))
	default:
		sess.print(Line{Kind: "warn", Text: "unknown command: " + cmd + " — try help"})
	}
}

func (sess *session) runLS(ctx context.Context, args []string) {
	display := "ls -la"
	if len(args) > 0 {
		display += " " + strings.Join(args, " ")
	}
	sess.print(Line{Kind: "info", Text: "$ " + display})

	cmdArgs := append([]string{"-la"}, args...)
	out, err := exec.CommandContext(ctx, "ls", cmdArgs...).Output()
	if err != nil {
		sess.print(Line{Kind: "err", Text: err.Error()})
		return
	}

	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		sess.print(Line{Kind: "out", Text: line})
	}
}

func (sess *session) print(l Line) {
	s := sess.styles
	var text string
	switch l.Kind {
	case "out":
		text = s.Out.Render(l.Text)
	case "dim":
		text = s.Dim.Render(l.Text)
	case "faint":
		text = s.Faint.Render(l.Text)
	case "info":
		text = s.Info.Render(l.Text)
	case "ok":
		text = s.Ok.Render(l.Text)
	case "warn":
		text = s.Warn.Render(l.Text)
	case "err":
		text = s.Err.Render(l.Text)
	case "accent":
		text = s.Accent.Render(l.Text)
	case "label":
		text = s.Label.Render(l.Text)
	default:
		text = l.Text
	}
	fmt.Println(text)
}
