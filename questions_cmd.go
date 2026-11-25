package main

import (
	"fmt"
	"strings"
)

// The question subcommands (add, remove, move) work the same for ticket types and forms.

// boundQuestions is one ticket type's or form's questions.
type boundQuestions interface {
	list() ([]formQuestion, error)
	add(q formQuestion) error
	remove(questionID int64) error
	reorder(ids []int64) error
}

type formQuestionStore struct{ formID int64 }

func (b formQuestionStore) list() ([]formQuestion, error) { return appForms.Questions(b.formID) }
func (b formQuestionStore) add(q formQuestion) error      { return appForms.AddQuestion(b.formID, q) }
func (b formQuestionStore) remove(id int64) error         { return appForms.RemoveQuestion(b.formID, id) }
func (b formQuestionStore) reorder(ids []int64) error {
	return appForms.ReorderQuestions(b.formID, ids)
}

type ticketQuestionStore struct{ typeID int64 }

func (b ticketQuestionStore) list() ([]formQuestion, error) { return tickets.Questions(b.typeID) }
func (b ticketQuestionStore) add(q formQuestion) error      { return tickets.AddQuestion(b.typeID, q) }
func (b ticketQuestionStore) remove(id int64) error         { return tickets.RemoveQuestion(b.typeID, id) }
func (b ticketQuestionStore) reorder(ids []int64) error {
	return tickets.ReorderQuestions(b.typeID, ids)
}

// questionTarget is what a question subcommand works on.
type questionTarget struct {
	name    string // the ticket type's or form's name
	header  bool   // it has text at the top, which takes a place on the first pop-up
	store   boundQuestions
	showCmd string // the command that lists the questions
}

func (t questionTarget) add(opts cmdOpts) (string, error) {
	existing, err := t.store.list()
	if err != nil {
		return "", err
	}
	if len(existing) >= formMaxQuestions {
		return "", userErrorf("**%s** can have at most %d questions.", t.name, formMaxQuestions)
	}
	label, _ := optString(opts, "question")
	label = strings.TrimSpace(label)
	kind, ok := optString(opts, "kind")
	if !ok {
		kind = kindShort
	}
	required, ok := optBool(opts, "required")
	if !ok {
		required = true
	}
	help, _ := optString(opts, "help")
	help = strings.TrimSpace(help)
	choicesText, _ := optString(opts, "choices")
	choices := parseChoices(choicesText)
	maxLength, _ := optInt(opts, "max_length")
	if problems := questionProblems(label, kind, help, choices, int(maxLength)); len(problems) > 0 {
		return "", userErrorf("%s", strings.Join(problems, "\n"))
	}
	q := formQuestion{Label: label, Kind: kind, Required: required, Help: help, Choices: choices, MaxLength: int(maxLength)}
	if err := t.store.add(q); err != nil {
		return "", err
	}
	count := len(existing) + 1
	return fmt.Sprintf("✅ Question %d added to **%s**: %s (%s). It now has %d question(s) on %d pop-up(s).",
		count, t.name, label, kindName(kind), count, len(formPages(append(existing, q), t.header))), nil
}

// numbered is question number (from 1).
func (t questionTarget) numbered(number int64) ([]formQuestion, formQuestion, error) {
	questions, err := t.store.list()
	if err != nil {
		return nil, formQuestion{}, err
	}
	if number < 1 || int(number) > len(questions) {
		if len(questions) == 0 {
			return nil, formQuestion{}, userErrorf("**%s** has no questions yet.", t.name)
		}
		return nil, formQuestion{}, userErrorf("**%s** has questions 1 to %d. `%s` lists them.", t.name, len(questions), t.showCmd)
	}
	return questions, questions[number-1], nil
}

func (t questionTarget) remove(opts cmdOpts) (string, error) {
	number, _ := optInt(opts, "number")
	questions, q, err := t.numbered(number)
	if err != nil {
		return "", err
	}
	if err := t.store.remove(q.ID); err != nil {
		return "", err
	}
	var order []int64
	for _, other := range questions {
		if other.ID != q.ID {
			order = append(order, other.ID)
		}
	}
	if err := t.store.reorder(order); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Removed question %d (%s) from **%s**. Answers already sent keep it.", number, q.Label, t.name), nil
}

func (t questionTarget) move(opts cmdOpts) (string, error) {
	number, _ := optInt(opts, "number")
	to, _ := optInt(opts, "to")
	questions, q, err := t.numbered(number)
	if err != nil {
		return "", err
	}
	if to < 1 || int(to) > len(questions) {
		return "", userErrorf("Pick a new number from 1 to %d.", len(questions))
	}
	if err := t.store.reorder(moveID(questions, q.ID, int(to))); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Moved \"%s\" to number %d on **%s**.", q.Label, to, t.name), nil
}

// moveID is the question IDs in order with id moved to position to (from 1).
func moveID(questions []formQuestion, id int64, to int) []int64 {
	var order []int64
	for _, q := range questions {
		if q.ID != id {
			order = append(order, q.ID)
		}
	}
	at := to - 1
	order = append(order, 0)
	copy(order[at+1:], order[at:])
	order[at] = id
	return order
}
