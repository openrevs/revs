package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kalafut/dmrs"
	"github.com/kalafut/dmrs/client"
	jsonlines "github.com/kalafut/sjsonl"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Draft is the unsigned working copy of the user's ratingset. Raw line
// bytes are the source of truth — the parsed structs are display views
// only, so unknown fields and refs on lines the user never edits
// round-trip untouched.
type Draft struct {
	UserRaw []byte
	User    dmrs.User2
	Titles  []TitleEntry // underlying order = file order
}

type TitleEntry struct {
	Raw   []byte
	Title dmrs.Title2
}

// TitleRef pairs a title view with its stable underlying index, the
// handle every mutator takes (the sorted view reorders freely).
type TitleRef struct {
	Index int
	Title dmrs.Title2
}

// LoadReport carries non-fatal problems found in a draft.
type LoadReport struct {
	Warnings []string
}

// content assembles the pushable form: unsigned data lines, one
// trailing newline, no control line — exactly client.Push's contract.
func (d *Draft) content() []byte {
	var buf bytes.Buffer
	buf.Write(d.UserRaw)
	buf.WriteByte('\n')
	for _, t := range d.Titles {
		buf.Write(t.Raw)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// LoadDraft (re)loads and validates the draft file. The file may have
// been hand-edited; hard errors are returned (draft left unloaded),
// recoverable oddities are reported as warnings.
func (w *Workspace) LoadDraft() (*LoadReport, error) {
	data, err := os.ReadFile(w.path(draftFile))
	if err != nil {
		return nil, err
	}

	report := &LoadReport{}
	draft, err := w.parseDraft(data, report)
	if err != nil {
		return nil, err
	}

	if !w.metaFound {
		report.Warnings = append(report.Warnings,
			"sync state missing (draft.meta.json) — draft treated as never pushed")
	}

	w.draft = draft
	w.recomputeDirty()
	return report, nil
}

// NewEmptyDraft starts a fresh collection: just a user line. The line
// is written in the canonical form Push produces (compact, keys
// sorted) so the first push round-trips to a byte-identical head.
func (w *Workspace) NewEmptyDraft() error {
	if w.key == nil {
		return ErrNoKey
	}
	line := fmt.Sprintf(`{"id":%q,"max_rating":5,"min_rating":0}`, w.UserID())

	draft, err := w.parseDraft([]byte(line+"\n"), &LoadReport{})
	if err != nil {
		return err
	}
	w.draft = draft
	if err := w.saveDraftFile(); err != nil {
		return err
	}
	w.recomputeDirty()
	return nil
}

// SeedFromHead replaces the draft with a pulled head, verbatim: the
// control line is stripped and the data lines are kept byte-for-byte,
// which is what makes the dirty hash-check exact. Any existing draft
// file is backed up first.
func (w *Workspace) SeedFromHead(doc *client.Document) error {
	idx := bytes.IndexByte(doc.Data, '\n')
	if idx < 0 {
		return errors.New("workspace: malformed document: no control line")
	}

	report := &LoadReport{}
	draft, err := w.parseDraft(doc.Data[idx+1:], report)
	if err != nil {
		return fmt.Errorf("workspace: pulled document is not a usable draft: %w", err)
	}

	if err := w.backupDraft(); err != nil {
		return err
	}

	w.draft = draft
	if err := w.saveDraftFile(); err != nil {
		return err
	}
	w.meta.BaseRev = doc.Rev
	w.meta.BaseHash = doc.Hash
	if err := w.saveMeta(); err != nil {
		return err
	}
	w.recomputeDirty()
	return nil
}

// backupDraft copies an existing draft file aside before a destructive
// replacement.
func (w *Workspace) backupDraft() error {
	data, err := os.ReadFile(w.path(draftFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s.bak-%s", draftFile, time.Now().Format("20060102-150405"))
	return writeFileAtomic(w.path(name), data, 0o644)
}

func (w *Workspace) parseDraft(data []byte, report *LoadReport) (*Draft, error) {
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil, errors.New("draft is empty")
	}

	draft := &Draft{}
	seen := map[string]int{}
	userLine := 0

	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		raw := []byte(line)

		if strings.TrimSpace(line) == "" {
			return nil, fmt.Errorf("line %d: empty line", n)
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("line %d: invalid JSON", n)
		}
		if gjson.GetBytes(raw, "sjsonl").Exists() {
			return nil, fmt.Errorf("line %d: signed-document control line — the draft must be unsigned data lines only", n)
		}
		if prev, dup := seen[line]; dup {
			return nil, fmt.Errorf("line %d: duplicate of line %d (identical lines are not allowed)", n, prev)
		}
		seen[line] = n

		if gjson.GetBytes(raw, "id").Exists() {
			if userLine != 0 {
				return nil, fmt.Errorf("line %d: second user line (line %d already has \"id\")", n, userLine)
			}
			userLine = n
			if err := json.Unmarshal(raw, &draft.User); err != nil {
				return nil, fmt.Errorf("line %d: bad user line: %w", n, err)
			}
			if w.key != nil && draft.User.ID != w.UserID() {
				return nil, fmt.Errorf("line %d: draft id %s does not match this workspace's key (user %s)",
					n, draft.User.ID, w.UserID())
			}
			draft.UserRaw = raw
			continue
		}

		var t dmrs.Title2
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("line %d: bad title line: %w", n, err)
		}
		if t.Name == "" {
			return nil, fmt.Errorf("line %d: title missing name", n)
		}
		draft.Titles = append(draft.Titles, TitleEntry{Raw: raw, Title: t})
	}

	if userLine == 0 {
		return nil, errors.New(`no user line (a line with "id") found`)
	}

	warnTitles(draft, report)
	return draft, nil
}

// warnTitles reports recoverable data oddities: out-of-bounds ratings
// and unparseable dates.
func warnTitles(d *Draft, report *LoadReport) {
	if d.User.MaxRating <= d.User.MinRating {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("user line: max_rating %d <= min_rating %d", d.User.MaxRating, d.User.MinRating))
	}
	for _, t := range d.Titles {
		for i, r := range t.Title.Reviews {
			if r.Rating != nil && (*r.Rating < d.User.MinRating || *r.Rating > d.User.MaxRating) {
				report.Warnings = append(report.Warnings,
					fmt.Sprintf("%s: review %d rating %d outside %d..%d",
						t.Title.Name, i+1, *r.Rating, d.User.MinRating, d.User.MaxRating))
			}
			if r.Date != nil {
				if _, err := time.Parse("2006-01-02", *r.Date); err != nil {
					report.Warnings = append(report.Warnings,
						fmt.Sprintf("%s: review %d date %q is not YYYY-MM-DD", t.Title.Name, i+1, *r.Date))
				}
			}
		}
	}
}

func (w *Workspace) saveDraftFile() error {
	return writeFileAtomic(w.path(draftFile), w.draft.content(), 0o644)
}

// Titles returns a name-sorted view; each ref carries the stable
// underlying index that mutators take.
func (w *Workspace) Titles() []TitleRef {
	if w.draft == nil {
		return nil
	}
	refs := make([]TitleRef, len(w.draft.Titles))
	for i, t := range w.draft.Titles {
		refs[i] = TitleRef{Index: i, Title: t.Title}
	}
	sort.SliceStable(refs, func(a, b int) bool {
		na, nb := strings.ToLower(refs[a].Title.Name), strings.ToLower(refs[b].Title.Name)
		if na != nb {
			return na < nb
		}
		ya, yb := 0, 0
		if refs[a].Title.Year != nil {
			ya = *refs[a].Title.Year
		}
		if refs[b].Title.Year != nil {
			yb = *refs[b].Title.Year
		}
		return ya < yb
	})
	return refs
}

// Title returns the current view of one title by underlying index.
func (w *Workspace) Title(idx int) (dmrs.Title2, error) {
	if w.draft == nil {
		return dmrs.Title2{}, ErrNoDraft
	}
	if idx < 0 || idx >= len(w.draft.Titles) {
		return dmrs.Title2{}, fmt.Errorf("workspace: no title at index %d", idx)
	}
	return w.draft.Titles[idx].Title, nil
}

func (w *Workspace) User() dmrs.User2 {
	if w.draft == nil {
		return dmrs.User2{}
	}
	return w.draft.User
}

// AddTitle appends a new title line. New lines are freshly marshaled
// (there are no unknown fields to preserve yet).
func (w *Workspace) AddTitle(name string, year *int, typ *string) error {
	if w.draft == nil {
		return ErrNoDraft
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("title name is required")
	}
	raw, err := json.Marshal(dmrs.Title2{Name: name, Year: year, Type: typ})
	if err != nil {
		return err
	}
	return w.commitTitle(-1, raw)
}

// UpdateTitle rewrites name/year/type on an existing line surgically,
// leaving refs and unknown fields untouched.
func (w *Workspace) UpdateTitle(idx int, name string, year *int, typ *string) error {
	raw, err := w.titleRaw(idx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("title name is required")
	}
	if raw, err = sjson.SetBytes(raw, "name", name); err != nil {
		return err
	}
	if raw, err = setOrDelete(raw, "year", year); err != nil {
		return err
	}
	if raw, err = setOrDelete(raw, "type", typ); err != nil {
		return err
	}
	return w.commitTitle(idx, raw)
}

func (w *Workspace) DeleteTitle(idx int) error {
	if _, err := w.titleRaw(idx); err != nil {
		return err
	}
	w.draft.Titles = slices.Delete(w.draft.Titles, idx, idx+1)
	return w.afterMutation()
}

// AddReview appends a review to a title's reviews array.
func (w *Workspace) AddReview(idx int, r dmrs.Review2) error {
	raw, err := w.titleRaw(idx)
	if err != nil {
		return err
	}
	rv, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if !gjson.GetBytes(raw, "reviews").Exists() {
		if raw, err = sjson.SetRawBytes(raw, "reviews", []byte("[]")); err != nil {
			return err
		}
	}
	if raw, err = sjson.SetRawBytes(raw, "reviews.-1", rv); err != nil {
		return err
	}
	return w.commitTitle(idx, raw)
}

// UpdateReview rewrites date/rating/comment of one review in place,
// preserving any unknown fields inside the review object.
func (w *Workspace) UpdateReview(idx, ri int, r dmrs.Review2) error {
	raw, err := w.titleRaw(idx)
	if err != nil {
		return err
	}
	if err := w.checkReviewIndex(idx, ri); err != nil {
		return err
	}
	prefix := fmt.Sprintf("reviews.%d.", ri)
	if raw, err = setOrDelete(raw, prefix+"date", r.Date); err != nil {
		return err
	}
	if raw, err = setOrDelete(raw, prefix+"rating", r.Rating); err != nil {
		return err
	}
	if raw, err = setOrDelete(raw, prefix+"comment", r.Comment); err != nil {
		return err
	}
	return w.commitTitle(idx, raw)
}

func (w *Workspace) DeleteReview(idx, ri int) error {
	raw, err := w.titleRaw(idx)
	if err != nil {
		return err
	}
	if err := w.checkReviewIndex(idx, ri); err != nil {
		return err
	}
	if raw, err = sjson.DeleteBytes(raw, fmt.Sprintf("reviews.%d", ri)); err != nil {
		return err
	}
	if gjson.GetBytes(raw, "reviews.#").Int() == 0 {
		if raw, err = sjson.DeleteBytes(raw, "reviews"); err != nil {
			return err
		}
	}
	return w.commitTitle(idx, raw)
}

func (w *Workspace) titleRaw(idx int) ([]byte, error) {
	if w.draft == nil {
		return nil, ErrNoDraft
	}
	if idx < 0 || idx >= len(w.draft.Titles) {
		return nil, fmt.Errorf("workspace: no title at index %d", idx)
	}
	return slices.Clone(w.draft.Titles[idx].Raw), nil
}

func (w *Workspace) checkReviewIndex(idx, ri int) error {
	reviews := w.draft.Titles[idx].Title.Reviews
	if ri < 0 || ri >= len(reviews) {
		return fmt.Errorf("workspace: no review at index %d", ri)
	}
	return nil
}

// setOrDelete sets path to *v, or removes it when v is nil (or an
// empty string), so cleared optional fields drop off the line.
func setOrDelete[T any](raw []byte, path string, v *T) ([]byte, error) {
	if v == nil {
		return sjson.DeleteBytes(raw, path)
	}
	if s, ok := any(*v).(string); ok && s == "" {
		return sjson.DeleteBytes(raw, path)
	}
	return sjson.SetBytes(raw, path, *v)
}

// commitTitle validates an edited/new title line, installs it (idx -1
// appends), and persists. Duplicate byte-identical lines would make
// the document unsignable, so they are rejected here.
func (w *Workspace) commitTitle(idx int, raw []byte) error {
	var t dmrs.Title2
	if err := json.Unmarshal(raw, &t); err != nil {
		return err
	}
	if t.Name == "" {
		return errors.New("title name is required")
	}

	if bytes.Equal(raw, w.draft.UserRaw) {
		return errors.New("edit collides with the user line")
	}
	for i, other := range w.draft.Titles {
		if i != idx && bytes.Equal(other.Raw, raw) {
			return fmt.Errorf("an identical entry for %q already exists", t.Name)
		}
	}

	entry := TitleEntry{Raw: raw, Title: t}
	if idx == -1 {
		w.draft.Titles = append(w.draft.Titles, entry)
	} else {
		w.draft.Titles[idx] = entry
	}
	return w.afterMutation()
}

func (w *Workspace) afterMutation() error {
	if err := w.saveDraftFile(); err != nil {
		return err
	}
	w.recomputeDirty()
	return nil
}

// recomputeDirty updates the cached dirty flag. Dirty is computed, not
// tracked: sign a candidate of the current lines with the revision
// pinned to BaseRev and compare doc hashes — the same check
// client.Push uses to decide "unchanged". Hand edits and edit-then-
// revert therefore resolve correctly.
func (w *Workspace) recomputeDirty() {
	w.dirty = w.computeDirty()
}

func (w *Workspace) computeDirty() bool {
	if w.draft == nil {
		return false
	}
	if w.key == nil || w.meta.BaseRev == 0 || w.meta.BaseHash == "" {
		return true
	}

	userRaw := w.draft.UserRaw
	// Touch the revision field only when its value differs, so a
	// verbatim-seeded user line stays byte-identical.
	if rev := gjson.GetBytes(userRaw, "revision"); !rev.Exists() || rev.Int() != int64(w.meta.BaseRev) {
		var err error
		userRaw, err = sjson.SetBytes(slices.Clone(userRaw), "revision", w.meta.BaseRev)
		if err != nil {
			return true
		}
	}

	cand := &Draft{UserRaw: userRaw, Titles: w.draft.Titles}
	_, info, err := jsonlines.Sign(cand.content(), w.key)
	if err != nil {
		return true
	}
	return info.Hash != w.meta.BaseHash
}

// LatestReview picks the review shown in list views: the one with the
// greatest date (YYYY-MM-DD sorts lexically), undated reviews losing
// to dated ones; ties go to the later entry.
func LatestReview(t dmrs.Title2) *dmrs.Review2 {
	var best *dmrs.Review2
	bestDate := ""
	for i := range t.Reviews {
		r := &t.Reviews[i]
		d := ""
		if r.Date != nil {
			d = *r.Date
		}
		if best == nil || d >= bestDate {
			best, bestDate = r, d
		}
	}
	return best
}
