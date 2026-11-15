package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openrevs/revs/client"
	"github.com/openrevs/revs/collection"
	"github.com/openrevs/revs/collection/localdb"
)

// ---- server list ----

type serversScreen struct {
	servers []localdb.Server
	cur     cursor
}

func (s *serversScreen) title() string { return "Servers" }

func (s *serversScreen) load(a *app) error {
	var err error
	s.servers, err = a.c.Servers(a.ctx)
	return err
}

func (s *serversScreen) help() string {
	if len(s.servers) == 0 {
		return "a add server"
	}
	return "enter open · a add server · d remove"
}

func (s *serversScreen) update(a *app, msg tea.Msg) tea.Cmd {
	if s.cur.move(msg, len(s.servers), 5) {
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "enter":
		if len(s.servers) > 0 {
			a.push(&serverScreen{id: s.servers[s.cur.i].ID})
		}
	case "a":
		return a.ask("Server URL:", "https://", func(a *app, v string) tea.Cmd {
			srv, err := a.c.AddServer(a.ctx, v)
			if err != nil {
				a.fail(err)
				return nil
			}
			a.info("Server added. Press u to pull its reviews, or p to push yours.")
			a.push(&serverScreen{id: srv.ID})
			return nil
		})
	case "d":
		if len(s.servers) > 0 {
			return confirmRemoveServer(a, s.servers[s.cur.i], false)
		}
	}
	return nil
}

func confirmRemoveServer(a *app, srv localdb.Server, pop bool) tea.Cmd {
	a.confirmThen("Remove "+srv.Url+"? Your published reviews stay on it.", func(a *app) tea.Cmd {
		if err := a.c.RemoveServer(a.ctx, srv.ID); err != nil {
			a.fail(err)
			return nil
		}
		a.info("Removed " + srv.Url + ".")
		if pop {
			a.pop()
		} else {
			a.reload()
		}
		return nil
	})
	return nil
}

func (s *serversScreen) view(a *app, width, height int) string {
	intro := styleDim.Render(wrap("Push your collection to openrevs servers, and pull everyone's published reviews from them.", width)) + "\n\n"
	if len(s.servers) == 0 {
		return intro + "No servers yet. Press a to add one."
	}
	urlW := max(min(40, width-50), 16)
	return intro + s.cur.render(len(s.servers), height-3, func(i int, sel bool) string {
		srv := s.servers[i]
		u := pad(srv.Url, urlW)
		if sel {
			u = styleSelected.Render(u)
		}
		line := u + "  " + styleDim.Render("pushed "+collection.TimeAgo(srv.LastPushAt)+", pulled "+collection.TimeAgo(srv.LastPullAt))
		if srv.LastError != nil {
			line += "  " + styleError.Render("error")
		}
		return line
	})
}

// ---- one server ----

// serverProblem is a sync problem needing the user's decision.
type serverProblem int

const (
	noProblem serverProblem = iota
	problemConflict
	problemDiverged
	problemKeyChanged
)

type serverScreen struct {
	id       int64
	st       *collection.ServerStatus
	cur      cursor
	problem  serverProblem
	conflict int // the server's revision, for problemConflict
}

func (s *serverScreen) title() string {
	if s.st == nil {
		return "Server"
	}
	return s.st.Server.Url
}

func (s *serverScreen) load(a *app) error {
	var err error
	s.st, err = a.c.ServerStatus(a.ctx, s.id)
	return err
}

func (s *serverScreen) help() string {
	h := "p push · u pull"
	if s.st != nil && s.st.Mine != nil {
		h += " · m merge my published copy"
	}
	if s.st != nil && len(s.st.Users) > 0 {
		h += " · enter view user"
	}
	return h + " · b re-download · R discard pulled data · D remove"
}

func (s *serverScreen) update(a *app, msg tea.Msg) tea.Cmd {
	if s.st == nil {
		return nil
	}
	if s.cur.move(msg, len(s.st.Users), 5) {
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "p":
		if s.problem == problemConflict {
			a.confirmThen("Replace the server's copy with your local collection?", func(a *app) tea.Cmd {
				return s.push(a)
			})
			return nil
		}
		return s.push(a)
	case "u":
		return s.pull(a, false)
	case "b":
		a.confirmThen("Re-download everything from the server's latest snapshot?", func(a *app) tea.Cmd {
			return s.pull(a, true)
		})
	case "m":
		rev, stats, err := a.c.ImportMine(a.ctx, s.id)
		if err != nil {
			a.fail(err)
			return nil
		}
		if s.problem == problemConflict {
			s.problem = noProblem
		}
		a.info(fmt.Sprintf("Merged revision %d. %s", rev, stats))
	case "R":
		a.confirmThen("Discard everything pulled from this server, including its pinned key?", func(a *app) tea.Cmd {
			if err := a.c.ResetServer(a.ctx, s.id); err != nil {
				a.fail(err)
				return nil
			}
			s.problem = noProblem
			a.info("Discarded everything pulled from this server, including its pinned key. Pull to start fresh.")
			a.reload()
			return nil
		})
	case "D":
		return confirmRemoveServer(a, s.st.Server, true)
	case "enter":
		if len(s.st.Users) > 0 {
			a.push(&serverUserScreen{serverID: s.id, uid: s.st.Users[s.cur.i].Userid})
		}
	}
	return nil
}

func (s *serverScreen) push(a *app) tea.Cmd {
	if !a.c.HasKey(a.ctx) {
		a.fail(collection.ErrNoKey)
		return nil
	}
	id := s.id
	return a.run("pushing", func(ctx context.Context) func(*app) tea.Cmd {
		res, err := a.c.Push(ctx, id)
		return func(a *app) tea.Cmd {
			var stale *client.StaleError
			switch {
			case errors.As(err, &stale):
				s.problem, s.conflict = problemConflict, stale.LatestRev
				a.fail(fmt.Errorf("the server has a newer revision (%d) than you last pushed", stale.LatestRev))
			case err != nil:
				a.fail(fmt.Errorf("push failed: %w", err))
			default:
				s.problem = noProblem
				a.info(collection.PushMessage(res))
			}
			a.reload()
			return nil
		}
	})
}

func (s *serverScreen) pull(a *app, rebootstrap bool) tea.Cmd {
	id := s.id
	return a.run("pulling", func(ctx context.Context) func(*app) tea.Cmd {
		res, err := a.c.Pull(ctx, id, rebootstrap)
		return func(a *app) tea.Cmd {
			var diverged *client.DivergedError
			var keyChanged *client.KeyChangedError
			switch {
			case errors.As(err, &diverged):
				s.problem = problemDiverged
				a.fail(errors.New("pull failed: this server's history no longer matches your copy"))
			case errors.As(err, &keyChanged):
				s.problem = problemKeyChanged
				a.fail(errors.New("pull failed: the server is signing with a different key"))
			case err != nil:
				a.fail(fmt.Errorf("pull failed: %w", err))
			default:
				if s.problem != problemConflict {
					s.problem = noProblem
				}
				a.info(collection.PullMessage(res))
			}
			a.reload()
			return nil
		}
	})
}

func (s *serverScreen) banner(width int) string {
	var text string
	switch s.problem {
	case problemConflict:
		text = fmt.Sprintf("The server has a newer revision (%d) than you last pushed. You probably pushed from another device.\n\n"+
			"To keep both, press u to pull, then m to merge the server's copy into your collection, then p to push. "+
			"Or press p now to push anyway, replacing the server's copy with yours.\n\n"+
			"A just-pushed revision only appears after the server publishes its next epoch, so the merge may have to wait.", s.conflict)
	case problemDiverged:
		text = "Your copy of this server's data no longer matches what it publishes. No published update applies to it, which can mean local corruption or that the server rewrote its history.\n\n" +
			"Press b to re-download from a snapshot."
	case problemKeyChanged:
		text = "The server is signing with a different key than the one pinned on first contact. Either the server's identity changed or something is intercepting it.\n\n" +
			"Only if you trust the change, press R to forget pulled data and re-pin its key."
	default:
		return ""
	}
	return styleBanner.Width(min(width-2, 90)).Render(styleWarn.Render(text)) + "\n\n"
}

func (s *serverScreen) view(a *app, width, height int) string {
	if s.st == nil {
		return ""
	}
	st := s.st
	var b strings.Builder
	b.WriteString(styleTitle.Render(st.Server.Url) + "\n\n")
	b.WriteString(s.banner(width))

	kv := func(k, v string) { b.WriteString(styleDim.Render(pad(k, 20)) + v + "\n") }
	push := collection.TimeAgo(st.Server.LastPushAt)
	if st.Server.LastPushRev != nil {
		push += fmt.Sprintf(" (revision %d)", *st.Server.LastPushRev)
	}
	kv("Last push", push)
	kv("Last pull", fmt.Sprintf("%s (epoch %d)", collection.TimeAgo(st.Server.LastPullAt), st.Epoch))
	if st.Server.LastError != nil {
		kv("Last error", styleError.Render(truncate(*st.Server.LastError, width-20)))
	}
	if st.Mine != nil {
		kv("Your published copy", fmt.Sprintf("revision %d, %d movies", st.Mine.Rev, st.Mine.TitleCount))
	}

	b.WriteString("\n" + styleHeading.Render("Users on this server") + "\n")
	if len(st.Users) == 0 {
		b.WriteString(styleDim.Render("  Nothing pulled yet. Press u to pull."))
		return b.String()
	}
	used := strings.Count(b.String(), "\n")
	b.WriteString(s.cur.render(len(st.Users), height-used, func(i int, sel bool) string {
		u := st.Users[i]
		id := pad(collection.ShortID(u.Userid), 14)
		if sel {
			id = styleSelected.Render(id)
		}
		line := id + fmt.Sprintf("  %4d movies  revision %d", u.TitleCount, u.Rev)
		if u.Userid == st.MyID {
			line += "  " + styleTag.Render("you")
		}
		return line
	}))
	return b.String()
}

// ---- another user's collection ----

type serverUserScreen struct {
	serverID int64
	uid      string
	lines    []string
	top      int
	isMe     bool
}

func (s *serverUserScreen) title() string { return "User " + collection.ShortID(s.uid) }

func (s *serverUserScreen) help() string { return "↑/↓ scroll" }

func (s *serverUserScreen) load(a *app) error {
	u, titles, err := a.c.ServerUser(a.ctx, s.serverID, s.uid)
	if err != nil {
		return err
	}
	s.isMe = s.uid == a.c.UserID(a.ctx)
	s.lines = []string{
		styleTitle.Render(s.uid),
		styleDim.Render(fmt.Sprintf("Revision %d, %d movies", u.Rev, u.TitleCount)),
		"",
	}
	for _, t := range titles {
		s.lines = append(s.lines, styleTitle.Render(movieTitle(t.Name, t.Year)))
		for _, r := range t.Reviews {
			line := "  " + pad(collection.Deref(r.ViewedOn), 10) + "  " + styleStars.Render(collection.Stars(r.Rating))
			s.lines = append(s.lines, line)
			if c := collection.Deref(r.Comment); c != "" {
				s.lines = append(s.lines, strings.Split(styleDim.Render(indent(c, "      ")), "\n")...)
			}
		}
	}
	return nil
}

func (s *serverUserScreen) update(a *app, msg tea.Msg) tea.Cmd {
	var c cursor
	c.i = s.top
	if c.move(msg, len(s.lines), a.height/2) {
		s.top = c.i
	}
	return nil
}

func (s *serverUserScreen) view(a *app, width, height int) string {
	head := ""
	if s.isMe {
		head = styleTag.Render("you") + "\n"
		height--
	}
	s.top = min(s.top, max(len(s.lines)-height, 0))
	end := min(len(s.lines), s.top+height)
	var out []string
	for _, l := range s.lines[s.top:end] {
		out = append(out, truncate(l, width))
	}
	return head + strings.Join(out, "\n")
}
