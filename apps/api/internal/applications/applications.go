package applications

import (
	"context"
	"database/sql"
	"encoding/hex"
	"strings"
)

type Input struct {
	Company string `json:"company"`
	Role    string `json:"role"`
	Status  string `json:"status"`
}

type Application struct {
	ID      string `json:"id"`
	Company string `json:"company"`
	Role    string `json:"role"`
	Status  string `json:"status"`
}

type PatchInput struct {
	Company *string `json:"company"`
	Role    *string `json:"role"`
	Status  *string `json:"status"`
}

func (i *Input) Valid() bool {
	i.Company = strings.TrimSpace(i.Company)
	i.Role = strings.TrimSpace(i.Role)
	return validText(i.Company) && validText(i.Role) && (i.Status == "saved" || i.Status == "applied")
}

func (i *PatchInput) Valid() bool {
	if i.Company == nil && i.Role == nil && i.Status == nil {
		return false
	}
	if i.Company != nil {
		*i.Company = strings.TrimSpace(*i.Company)
		if !validText(*i.Company) {
			return false
		}
	}
	if i.Role != nil {
		*i.Role = strings.TrimSpace(*i.Role)
		if !validText(*i.Role) {
			return false
		}
	}
	return i.Status == nil || *i.Status == "saved" || *i.Status == "applied"
}

func validText(value string) bool {
	return value != "" && len(value) <= 200 && !strings.ContainsRune(value, 0)
}

func ValidID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(decoded) == 16
}

type Store interface {
	Create(context.Context, string, Input) (Application, error)
	List(context.Context, string) ([]Application, error)
	Get(context.Context, string, string) (Application, error)
	Patch(context.Context, string, string, PatchInput) (Application, error)
	Delete(context.Context, string, string) (bool, error)
}

type PostgresStore struct{ DB *sql.DB }

func (s PostgresStore) Create(ctx context.Context, userID string, in Input) (Application, error) {
	var a Application
	err := s.DB.QueryRowContext(ctx, `INSERT INTO applications (user_id, company, role, status) VALUES ($1, $2, $3, $4) RETURNING id, company, role, status`, userID, in.Company, in.Role, in.Status).Scan(&a.ID, &a.Company, &a.Role, &a.Status)
	return a, err
}

func (s PostgresStore) List(ctx context.Context, userID string) ([]Application, error) {
	// shortcut: returns the newest 100 applications, add cursor pagination before supporting larger lists.
	rows, err := s.DB.QueryContext(ctx, `SELECT id, company, role, status FROM applications WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.Company, &a.Role, &a.Status); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (s PostgresStore) Get(ctx context.Context, userID, id string) (Application, error) {
	var a Application
	err := s.DB.QueryRowContext(ctx, `SELECT id, company, role, status FROM applications WHERE user_id = $1 AND id = $2`, userID, id).Scan(&a.ID, &a.Company, &a.Role, &a.Status)
	if err == sql.ErrNoRows {
		err = nil
	}
	return a, err
}

func (s PostgresStore) Patch(ctx context.Context, userID, id string, input PatchInput) (Application, error) {
	var a Application
	var company, role, status any
	if input.Company != nil {
		company = *input.Company
	}
	if input.Role != nil {
		role = *input.Role
	}
	if input.Status != nil {
		status = *input.Status
	}
	err := s.DB.QueryRowContext(ctx, `UPDATE applications SET company=COALESCE($3, company), role=COALESCE($4, role), status=COALESCE($5, status) WHERE user_id=$1 AND id=$2 RETURNING id, company, role, status`, userID, id, company, role, status).Scan(&a.ID, &a.Company, &a.Role, &a.Status)
	if err == sql.ErrNoRows {
		err = nil
	}
	return a, err
}

func (s PostgresStore) Delete(ctx context.Context, userID, id string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM applications WHERE user_id=$1 AND id=$2`, userID, id)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rowsAffected != 0, nil
}
