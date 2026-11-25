package main

import (
	"database/sql"
	"encoding/json"
)

// Intake questions are stored the same way for ticket types and forms: one table each,
// with an owner column (type_id or form_id). These helpers serve both.

func pgQuestions(db *sql.DB, table, owner string, ownerID int64) ([]formQuestion, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT id, label, kind, required, help, choices, max_length FROM `+
		table+` WHERE `+owner+` = $1 ORDER BY position, id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []formQuestion{}
	for rows.Next() {
		var q formQuestion
		var choices []byte
		if err := rows.Scan(&q.ID, &q.Label, &q.Kind, &q.Required, &q.Help, &choices, &q.MaxLength); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(choices, &q.Choices)
		out = append(out, q)
	}
	return out, rows.Err()
}

func pgAddQuestion(db *sql.DB, table, owner string, ownerID int64, q formQuestion) error {
	ctx, cancel := dbCtx()
	defer cancel()
	choices, _ := json.Marshal(append([]string{}, q.Choices...))
	_, err := db.ExecContext(ctx, `INSERT INTO `+table+` (`+owner+`, position, label, kind, required, help, choices, max_length)
		VALUES ($1, (SELECT COALESCE(MAX(position) + 1, 0) FROM `+table+` WHERE `+owner+` = $1), $2, $3, $4, $5, $6, $7)`,
		ownerID, q.Label, q.Kind, q.Required, q.Help, string(choices), q.MaxLength)
	return err
}

func pgRemoveQuestion(db *sql.DB, table, owner string, ownerID, questionID int64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE id = $1 AND `+owner+` = $2`, questionID, ownerID)
	return err
}

func pgReorderQuestions(db *sql.DB, table, owner string, ownerID int64, ids []int64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for position, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET position = $1 WHERE id = $2 AND `+owner+` = $3`, position, id, ownerID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// questionLists keeps questions in memory for the test stores. The caller holds the lock.
type questionLists map[int64][]formQuestion

func (l questionLists) get(owner int64) []formQuestion {
	return append([]formQuestion(nil), l[owner]...)
}

func (l questionLists) add(owner int64, q formQuestion) {
	l[owner] = append(l[owner], q)
}

func (l questionLists) remove(owner, questionID int64) {
	kept := []formQuestion{}
	for _, q := range l[owner] {
		if q.ID != questionID {
			kept = append(kept, q)
		}
	}
	l[owner] = kept
}

func (l questionLists) reorder(owner int64, ids []int64) {
	byID := map[int64]formQuestion{}
	for _, q := range l[owner] {
		byID[q.ID] = q
	}
	ordered := []formQuestion{}
	for _, id := range ids {
		if q, ok := byID[id]; ok {
			ordered = append(ordered, q)
			delete(byID, id)
		}
	}
	for _, q := range l[owner] { // any not named keep their place at the end
		if _, left := byID[q.ID]; left {
			ordered = append(ordered, q)
		}
	}
	l[owner] = ordered
}

func (l questionLists) drop(owner int64) { delete(l, owner) }
