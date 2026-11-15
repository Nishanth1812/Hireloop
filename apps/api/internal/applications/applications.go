package applications

import (
	"context"
	"database/sql"
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

func (i *Input) Valid() bool {
	i.Company = strings.TrimSpace(i.Company)
	i.Role = strings.TrimSpace(i.Role)
	return i.Company != "" && len(i.Company) <= 200 && i.Role != "" && len(i.Role) <= 200 && (i.Status == "saved" || i.Status == "applied")
}

type Store interface {
	Create(context.Context, string, Input) (Application, error)
	List(context.Context, string) ([]Application, error)
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
