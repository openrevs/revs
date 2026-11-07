package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kalafut/dmrs"
)

// displayReviews returns the underlying review indexes in display
// order: newest date first, undated last, later entries winning ties.
func displayReviews(t dmrs.Title2) []int {
	idxs := make([]int, len(t.Reviews))
	for i := range idxs {
		idxs[i] = i
	}
	date := func(i int) string {
		if t.Reviews[i].Date != nil {
			return *t.Reviews[i].Date
		}
		return ""
	}
	sort.SliceStable(idxs, func(a, b int) bool {
		da, db := date(idxs[a]), date(idxs[b])
		if da != db {
			return da > db
		}
		return idxs[a] > idxs[b]
	})
	return idxs
}

func (a *app) detailTitle() (dmrs.Title2, bool) {
	t, err := a.ws.Title(a.detailIdx)
	if err != nil {
		return dmrs.Title2{}, false
	}
	return t, true
}

func (a *app) updateDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return a, nil
	}

	t, alive := a.detailTitle()
	if !alive {
		a.screen = screenList
		return a, nil
	}
	order := displayReviews(t)

	switch key.String() {
	case "esc", "q":
		a.refreshList()
		a.screen = screenList
	case "j", "down":
		if a.reviewCur < len(order)-1 {
			a.reviewCur++
		}
	case "k", "up":
		if a.reviewCur > 0 {
			a.reviewCur--
		}
	case "r":
		a.reviewForm = newReviewForm(a.detailIdx, -1, nil, a.ws.User())
		a.screen = screenReviewForm
		return a, textinputBlink()
	case "e":
		if len(order) > 0 {
			ri := order[a.reviewCur]
			a.reviewForm = newReviewForm(a.detailIdx, ri, &t.Reviews[ri], a.ws.User())
			a.screen = screenReviewForm
			return a, textinputBlink()
		}
	case "t":
		a.titleForm = newTitleForm(a.detailIdx, &t)
		a.screen = screenTitleForm
		return a, textinputBlink()
	case "d":
		if len(order) > 0 {
			ri := order[a.reviewCur]
			idx := a.detailIdx
			a.confirm = &confirmState{
				prompt: fmt.Sprintf("Delete this review of %q?", t.Name),
				do: func() (string, error) {
					if err := a.ws.DeleteReview(idx, ri); err != nil {
						return "", err
					}
					return "review deleted", nil
				},
				back: screenDetail,
			}
			a.screen = screenConfirm
		}
	}
	return a, nil
}

func (a *app) viewDetail() string {
	t, alive := a.detailTitle()
	if !alive {
		return ""
	}

	var b strings.Builder
	head := t.Name
	if t.Year != nil {
		head += fmt.Sprintf(" (%d)", *t.Year)
	}
	if t.Type != nil && *t.Type != "" {
		head += "  [" + *t.Type + "]"
	}
	b.WriteString(styleTitle.Render(head) + "\n")
	if len(t.Refs) > 0 {
		refs := []string{}
		for k, v := range t.Refs {
			refs = append(refs, k+":"+v)
		}
		sort.Strings(refs)
		b.WriteString(styleDim.Render(strings.Join(refs, "  ")) + "\n")
	}
	b.WriteString("\n")

	order := displayReviews(t)
	if len(order) == 0 {
		b.WriteString(styleDim.Render("no reviews yet — press r to rate") + "\n")
	}
	if a.reviewCur >= len(order) && len(order) > 0 {
		a.reviewCur = len(order) - 1
	}
	for pos, ri := range order {
		r := t.Reviews[ri]
		cursor := "  "
		if pos == a.reviewCur {
			cursor = styleCursor.Render("> ")
		}

		line := []string{}
		if r.Date != nil {
			line = append(line, *r.Date)
		} else {
			line = append(line, "(undated)")
		}
		if r.Rating != nil {
			line = append(line, fmt.Sprintf("rated %d", *r.Rating))
		} else {
			line = append(line, "no rating")
		}
		b.WriteString(cursor + strings.Join(line, "  ·  ") + "\n")
		if r.Comment != nil && *r.Comment != "" {
			b.WriteString("    " + styleDim.Render(*r.Comment) + "\n")
		}
	}

	b.WriteString("\n" + styleHelp.Render("r rate · e edit · d delete review · t edit title · j/k move · esc back"))
	return styleDocFrame.Render(b.String())
}
