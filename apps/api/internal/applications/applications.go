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
	Source  string `json:"source"`
	Address string `json:"address"`
}

type Application struct {
	ID      string `json:"id"`
	Company string `json:"company"`
	Role    string `json:"role"`
	Status  string `json:"status"`
	Source  string `json:"source"`
	Address string `json:"address"`
}

type PatchInput struct {
	Company *string `json:"company"`
	Role    *string `json:"role"`
	Status  *string `json:"status"`
	Source  *string `json:"source"`
	Address *string `json:"address"`
}

func (i *Input) Valid() bool {
	i.Company = strings.TrimSpace(i.Company)
	i.Role = strings.TrimSpace(i.Role)
	i.Source = strings.TrimSpace(i.Source)
	i.Address = strings.TrimSpace(i.Address)
	return validText(i.Company) && validText(i.Role) && (i.Status == "saved" || i.Status == "applied") &&
		validOptionalText(i.Source) && validOptionalText(i.Address)
}

func (i *PatchInput) Valid() bool {
	if i.Company == nil && i.Role == nil && i.Status == nil && i.Source == nil && i.Address == nil {
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
	if i.Source != nil {
		*i.Source = strings.TrimSpace(*i.Source)
		if !validOptionalText(*i.Source) {
			return false
		}
	}
	if i.Address != nil {
		*i.Address = strings.TrimSpace(*i.Address)
		if !validOptionalText(*i.Address) {
			return false
		}
	}
	return i.Status == nil || *i.Status == "saved" || *i.Status == "applied"
}

func validText(value string) bool {
	return value != "" && len(value) <= 200 && !strings.ContainsRune(value, 0)
}

// validOptionalText allows a blank value (the field is optional) while still
// rejecting overlong input or PostgreSQL-incompatible NUL characters. Trimming
// already happened, so an all-whitespace value becomes "" and is accepted.
func validOptionalText(value string) bool {
	return len(value) <= 200 && !strings.ContainsRune(value, 0)
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
	err := s.DB.QueryRowContext(ctx, `INSERT INTO applications (user_id, company, role, status, source, address) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, company, role, status, source, address`, userID, in.Company, in.Role, in.Status, in.Source, in.Address).Scan(&a.ID, &a.Company, &a.Role, &a.Status, &a.Source, &a.Address)
	return a, err
}

func (s PostgresStore) List(ctx context.Context, userID string) ([]Application, error) {
	// shortcut: returns the newest 100 applications, add cursor pagination before supporting larger lists.
	rows, err := s.DB.QueryContext(ctx, `SELECT id, company, role, status, COALESCE(source, ''), COALESCE(address, '') FROM applications WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.Company, &a.Role, &a.Status, &a.Source, &a.Address); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (s PostgresStore) Get(ctx context.Context, userID, id string) (Application, error) {
	var a Application
	err := s.DB.QueryRowContext(ctx, `SELECT id, company, role, status, COALESCE(source, ''), COALESCE(address, '') FROM applications WHERE user_id = $1 AND id = $2`, userID, id).Scan(&a.ID, &a.Company, &a.Role, &a.Status, &a.Source, &a.Address)
	if err == sql.ErrNoRows {
		err = nil
	}
	return a, err
}

func (s PostgresStore) Patch(ctx context.Context, userID, id string, input PatchInput) (Application, error) {
	var a Application
	var company, role, status, source, address any
	if input.Company != nil {
		company = *input.Company
	}
	if input.Role != nil {
		role = *input.Role
	}
	if input.Status != nil {
		status = *input.Status
	}
	if input.Source != nil {
		source = *input.Source
	}
	if input.Address != nil {
		address = *input.Address
	}
	err := s.DB.QueryRowContext(ctx, `UPDATE applications SET company=COALESCE($3, company), role=COALESCE($4, role), status=COALESCE($5, status), source=COALESCE($6, source), address=COALESCE($7, address) WHERE user_id=$1 AND id=$2 RETURNING id, company, role, status, COALESCE(source, ''), COALESCE(address, '')`, userID, id, company, role, status, source, address).Scan(&a.ID, &a.Company, &a.Role, &a.Status, &a.Source, &a.Address)
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
