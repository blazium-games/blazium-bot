package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// What an application form is made of: questions, their kinds and Discord's limits.
// No Discord calls here, so all of it can be tested.
//
// A form is shown as one or more pop-ups of at most formPerPage questions each. Answers
// are saved together with the question as it was asked, so changing a form later never
// changes an application already sent.

const (
	formPerPage      = 5    // questions in one pop-up
	formMaxQuestions = 20   // four pages
	formLabelMax     = 45   // a question
	formHelpMax      = 100  // the line of help under a question
	formChoicesMax   = 25   // options in a dropdown
	formChoiceMax    = 100  // one option
	formNameMax      = 45   // a form's name is a pop-up's title
	formIntroMax     = 1000 // words above the Apply button
	formHeaderMax    = 4000 // the block of text at the top of the first pop-up
	formWelcomeMax   = 1800 // the message posted in an applicant's chat
	formNoteMax      = 500  // a reviewer's note to the applicant
)

const (
	kindShort     = "short"
	kindParagraph = "paragraph"
	kindChoice    = "choice"
	kindMulti     = "multi"
)

// formKinds in the order staff pick them.
var formKinds = []struct{ Key, Name string }{
	{kindShort, "Short answer"},
	{kindParagraph, "Long answer"},
	{kindChoice, "Pick one"},
	{kindMulti, "Pick any"},
}

// formTextMax is the most a member may type, and the default.
var formTextMax = map[string]int{kindShort: 300, kindParagraph: 1000}

var formWelcomeWords = []struct{ Word, What string }{
	{"{member}", "pings the applicant"},
	{"{name}", "their name"},
	{"{form}", "the form's name"},
}

func kindName(kind string) string {
	for _, k := range formKinds {
		if k.Key == kind {
			return k.Name
		}
	}
	return kind
}

type formQuestion struct {
	ID        int64
	Label     string
	Kind      string
	Required  bool
	Help      string
	Choices   []string
	MaxLength int // 0: the kind's default
}

// Limit is how many characters a typed answer may have.
func (q formQuestion) Limit() int {
	most, ok := formTextMax[q.Kind]
	if !ok {
		most = 300
	}
	if q.MaxLength > 0 && q.MaxLength < most {
		return q.MaxLength
	}
	return most
}

// Describe is the question in one line, for staff.
func (q formQuestion) Describe() string {
	var extra []string
	if len(q.Choices) > 0 {
		extra = append(extra, strings.Join(q.Choices, " / "))
	} else if _, typed := formTextMax[q.Kind]; typed {
		extra = append(extra, fmt.Sprintf("up to %d characters", q.Limit()))
	}
	if q.Required {
		extra = append(extra, "required")
	} else {
		extra = append(extra, "optional")
	}
	return fmt.Sprintf("%s (%s; %s)", q.Label, kindName(q.Kind), strings.Join(extra, ", "))
}

// parseChoices: "Yes, No, Maybe" -> [Yes No Maybe]. A choice may not repeat (ignoring case).
func parseChoices(text string) []string {
	var found []string
	seen := map[string]bool{}
	for _, part := range strings.Split(text, ",") {
		choice := strings.TrimSpace(part)
		if choice == "" || seen[strings.ToLower(choice)] {
			continue
		}
		seen[strings.ToLower(choice)] = true
		found = append(found, choice)
	}
	return found
}

// questionProblems is why a question can't be asked as written; empty when it's fine.
func questionProblems(label, kind, help string, choices []string, maxLength int) []string {
	var found []string
	known := false
	for _, k := range formKinds {
		known = known || k.Key == kind
	}
	if !known {
		found = append(found, "Pick a kind of question from the list.")
	}
	if strings.TrimSpace(label) == "" {
		found = append(found, "The question needs some words.")
	}
	if n := len([]rune(label)); n > formLabelMax {
		found = append(found, fmt.Sprintf("Discord shows at most %d characters for a question; this one has %d. Put the rest in `help`.", formLabelMax, n))
	}
	if n := len([]rune(help)); n > formHelpMax {
		found = append(found, fmt.Sprintf("The help line can be at most %d characters; this one has %d.", formHelpMax, n))
	}
	if kind == kindChoice || kind == kindMulti {
		if len(choices) < 2 {
			found = append(found, "A pick-one or pick-any question needs at least two choices, separated by commas.")
		}
		if len(choices) > formChoicesMax {
			found = append(found, fmt.Sprintf("A question can offer at most %d choices.", formChoicesMax))
		}
		for _, c := range choices {
			if len([]rune(c)) > formChoiceMax {
				found = append(found, fmt.Sprintf("Each choice can be at most %d characters.", formChoiceMax))
				break
			}
		}
	} else if len(choices) > 0 {
		found = append(found, "Only pick-one and pick-any questions have choices. Leave `choices` empty.")
	}
	if most, typed := formTextMax[kind]; typed && maxLength != 0 && (maxLength < 1 || maxLength > most) {
		found = append(found, fmt.Sprintf("For a %s, `max_length` can be 1 to %d.", strings.ToLower(kindName(kind)), most))
	}
	return found
}

// formPages splits questions into pop-ups of at most formPerPage. A block of text at the
// top (header) takes one of the first pop-up's places, so that page holds one fewer.
func formPages(questions []formQuestion, header bool) [][]formQuestion {
	if len(questions) == 0 {
		return [][]formQuestion{{}}
	}
	var out [][]formQuestion
	rest := questions
	if header {
		n := min(formPerPage-1, len(rest))
		out = append(out, rest[:n])
		rest = rest[n:]
	}
	for len(rest) > 0 {
		n := min(formPerPage, len(rest))
		out = append(out, rest[:n])
		rest = rest[n:]
	}
	return out
}

// formAnswer is one question as it was asked, with what was entered.
type formAnswer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// formDraft is an application being filled in, page by page. Kept in memory only until it's sent.
type formDraft struct {
	FormID    int64
	GuildID   string
	UserID    string
	FormName  string
	Questions []formQuestion
	Answers   map[int64]string
	Page      int    // the next page to fill in
	Header    string // a block of text shown at the top of the first page
	Preview   bool   // staff trying the form: nothing is saved or sent
}

func (d *formDraft) pages() [][]formQuestion { return formPages(d.Questions, d.Header != "") }
func (d *formDraft) Total() int              { return len(d.pages()) }
func (d *formDraft) Done() bool              { return d.Page >= d.Total() }
func (d *formDraft) Current() []formQuestion { return d.pages()[d.Page] }

// Record saves the current page's answers (picks joined with ", ") and moves to the next page.
func (d *formDraft) Record(values map[int64][]string) {
	if d.Answers == nil {
		d.Answers = map[int64]string{}
	}
	for _, q := range d.Current() {
		d.Answers[q.ID] = strings.TrimSpace(strings.Join(values[q.ID], ", "))
	}
	d.Page++
}

// Saved is the answers as stored: each question as it was asked, with its answer.
func (d *formDraft) Saved() []formAnswer {
	out := make([]formAnswer, len(d.Questions))
	for n, q := range d.Questions {
		out[n] = formAnswer{Question: q.Label, Answer: d.Answers[q.ID]}
	}
	return out
}

func dumpAnswers(answers []formAnswer) string {
	b, _ := json.Marshal(answers)
	return string(b)
}

func loadAnswers(text string) []formAnswer {
	var out []formAnswer
	if text != "" {
		_ = json.Unmarshal([]byte(text), &out)
	}
	return out
}

// formTranscript is the whole application as plain text, for a file when it's too long for a message.
func formTranscript(title, who string, answers []formAnswer) string {
	lines := []string{title, who, ""}
	for n, a := range answers {
		answer := a.Answer
		if answer == "" {
			answer = "(no answer)"
		}
		lines = append(lines, fmt.Sprintf("%d. %s", n+1, a.Question), answer, "")
	}
	return strings.Join(lines, "\n")
}

// fillWelcome puts the applicant and form into a chat's welcome message, within Discord's 2000 characters.
func fillWelcome(text, mention, name, form string) string {
	text = strings.NewReplacer("{member}", mention, "{name}", name, "{form}", form).Replace(text)
	return truncateRunes(text, 2000)
}

func truncateRunes(text string, most int) string {
	r := []rune(text)
	if len(r) <= most {
		return text
	}
	return string(r[:most])
}

// shorten cuts text to most characters, ending with … when cut.
func shorten(text string, most int) string {
	r := []rune(text)
	if len(r) <= most {
		return text
	}
	return strings.TrimRight(string(r[:most-1]), " ") + "…"
}

// fitLines is head and as many of lines as fit in one Discord message, then more(left out) if any didn't.
func fitLines(head string, lines []string, more func(int) string) string {
	text := head
	for shown, line := range lines {
		rest := len(lines) - shown
		if len(text)+1+len(line)+1+len(more(rest)) > 2000 {
			return text + "\n" + more(rest)
		}
		text += "\n" + line
	}
	return text
}

// chatName is a valid channel name for an applicant's chat: "apply-ann".
func chatName(username, userID string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(username)) {
		switch {
		case r == ' ' || r == '-' || r == '\t':
			if !lastDash && b.Len() > 0 {
				b.WriteRune('-')
				lastDash = true
			}
		case r == '_' || ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') || r > 127:
			b.WriteRune(r)
			lastDash = false
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = userID
	}
	return truncateRunes("apply-"+name, 90)
}
