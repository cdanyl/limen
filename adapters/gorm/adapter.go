package gorm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/thecodearcher/limen"
)

// Adapter implements limen.DatabaseAdapter using GORM
type Adapter struct {
	db *gorm.DB // Regular DB connection
	tx *gorm.DB // Transaction DB (nil when not in transaction)
}

// New creates a new GORM adapter
func New(db *gorm.DB) *Adapter {
	return &Adapter{db: db}
}

// getDB returns the transaction DB if in a transaction, otherwise returns the regular DB
func (a *Adapter) getDB() *gorm.DB {
	if a.tx != nil {
		return a.tx
	}
	return a.db
}

// BeginTx starts a new transaction
func (a *Adapter) BeginTx(ctx context.Context) (limen.DatabaseTx, error) {
	tx := a.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	return &Adapter{
		db: a.db,
		tx: tx,
	}, nil
}

// Commit commits the transaction if this adapter is in transaction mode
func (a *Adapter) Commit() error {
	if a.tx == nil {
		return fmt.Errorf("not in a transaction")
	}
	err := a.tx.Commit().Error
	a.tx = nil
	return err
}

// Rollback rolls back the transaction if this adapter is in transaction mode
func (a *Adapter) Rollback() error {
	if a.tx == nil {
		return fmt.Errorf("not in a transaction")
	}
	err := a.tx.Rollback().Error
	a.tx = nil // Clear transaction state
	return err
}

func (a *Adapter) Create(ctx context.Context, tableName limen.SchemaTableName, data map[string]any) (limen.DatabaseResult, error) {
	db := a.getDB()
	result := db.WithContext(ctx).Table(string(tableName)).Create(data)
	return limen.DatabaseResult{RowsAffected: result.RowsAffected}, result.Error
}

func (a *Adapter) FindOne(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where, orderBy []limen.OrderBy) (map[string]any, error) {
	var result map[string]any
	db := a.getDB()
	query := db.WithContext(ctx).Table(string(tableName))

	query = a.applyConditions(query, conditions)

	for _, orderBy := range orderBy {
		query = query.Order(orderBy.Column + " " + string(orderBy.Direction))
	}

	err := query.Take(&result).Error
	return result, a.formatError(err)
}

func (a *Adapter) FindMany(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where, options *limen.QueryOptions) ([]map[string]any, error) {
	var results []map[string]any
	db := a.getDB()
	query := db.WithContext(ctx).Table(string(tableName))

	query = a.applyConditions(query, conditions)

	if options != nil {
		if options.Limit > 0 {
			query = query.Limit(options.Limit)
		}
		if options.Offset > 0 {
			query = query.Offset(options.Offset)
		}
		for _, orderBy := range options.OrderBy {
			query = query.Order(orderBy.Column + " " + string(orderBy.Direction))
		}
	}

	err := query.Find(&results).Error
	return results, a.formatError(err)
}

func (a *Adapter) Update(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where, updates map[string]any) (limen.DatabaseResult, error) {
	payload, err := buildUpdatePayload(updates)
	if err != nil {
		return limen.DatabaseResult{}, err
	}

	db := a.getDB()
	query := db.WithContext(ctx).Table(string(tableName))
	query = a.applyConditions(query, conditions)
	result := query.Updates(payload)
	return limen.DatabaseResult{RowsAffected: result.RowsAffected}, result.Error
}

func buildUpdatePayload(updates map[string]any) (map[string]any, error) {
	payload := make(map[string]any, len(updates))
	for column, value := range updates {
		update, ok := value.(limen.ArithmeticUpdate)
		if !ok {
			payload[column] = value
			continue
		}
		if err := update.Validate(); err != nil {
			return nil, fmt.Errorf("update column %q: %w", column, err)
		}

		operator := "+"
		amount := update.Value()
		if amount < 0 {
			operator = "-"
			amount = -amount
		}
		payload[column] = gorm.Expr(
			"? "+operator+" ?",
			clause.Column{Name: column},
			amount,
		)
	}
	return payload, nil
}

func (a *Adapter) Delete(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where) (limen.DatabaseResult, error) {
	db := a.getDB()
	query := db.WithContext(ctx).Table(string(tableName))
	query = a.applyConditions(query, conditions)
	result := query.Delete(nil)
	return limen.DatabaseResult{RowsAffected: result.RowsAffected}, result.Error
}

func (a *Adapter) Exists(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where) (bool, error) {
	var count int64
	db := a.getDB()
	query := db.WithContext(ctx).Table(string(tableName))
	query = a.applyConditions(query, conditions)
	err := query.Count(&count).Error
	return count > 0, err
}

func (a *Adapter) Count(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where) (int64, error) {
	var count int64
	db := a.getDB()
	query := db.WithContext(ctx).Table(string(tableName))
	query = a.applyConditions(query, conditions)
	err := query.Count(&count).Error
	return count, err
}

func (a *Adapter) applyConditions(query *gorm.DB, conditions []limen.Where) *gorm.DB {
	if len(conditions) == 0 {
		return query
	}

	if len(conditions) == 1 {
		clause, args := a.buildWhereClause(conditions[0])
		if clause == "" {
			return query
		}
		return query.Where(clause, args...)
	}

	groups := limen.GroupConditionsByConnector(conditions)
	for _, group := range groups {
		query = a.applyGroup(query, group)
	}
	return query
}

// applyGroup applies one group (single condition or OR of several) as one Where.
func (a *Adapter) applyGroup(query *gorm.DB, group []limen.Where) *gorm.DB {
	if len(group) == 0 {
		return query
	}
	clause, args := limen.BuildGroupClause(group, a.buildWhereClause)
	if clause == "" {
		return query
	}
	return query.Where(clause, args...)
}

func (a *Adapter) buildWhereClause(condition limen.Where) (string, []any) {
	switch condition.Operator {
	case limen.OpEq:
		return condition.Column + " = ?", []any{condition.Value}
	case limen.OpNe:
		return condition.Column + " != ?", []any{condition.Value}
	case limen.OpLt:
		return condition.Column + " < ?", []any{condition.Value}
	case limen.OpLte:
		return condition.Column + " <= ?", []any{condition.Value}
	case limen.OpGt:
		return condition.Column + " > ?", []any{condition.Value}
	case limen.OpGte:
		return condition.Column + " >= ?", []any{condition.Value}
	case limen.OpIn:
		return condition.Column + " IN ?", []any{condition.Value}
	case limen.OpNotIn:
		return condition.Column + " NOT IN ?", []any{condition.Value}
	case limen.OpContains:
		return condition.Column + " LIKE ?", []any{"%" + condition.Value.(string) + "%"}
	case limen.OpStartsWith:
		return condition.Column + " LIKE ?", []any{condition.Value.(string) + "%"}
	case limen.OpEndsWith:
		return condition.Column + " LIKE ?", []any{"%" + condition.Value.(string)}
	case limen.OpIsNull:
		return condition.Column + " IS NULL", nil
	case limen.OpIsNotNull:
		return condition.Column + " IS NOT NULL", nil
	default:
		return "", nil
	}
}

func (a *Adapter) formatError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return limen.ErrRecordNotFound
	}
	return err
}

func (a *Adapter) relatedQuery(ctx context.Context, table limen.SchemaTableName, column string, conditions []limen.Where, related limen.RelatedQuery) (*gorm.DB, error) {
	if column == "" || related.Table == "" || related.Column == "" {
		return nil, fmt.Errorf("related identifiers are required")
	}
	db := a.getDB().WithContext(ctx)
	var selected strings.Builder
	db.QuoteTo(&selected, related.Column)
	subquery := a.applyConditions(db.Table(string(related.Table)), related.Conditions).Select(selected.String())
	query := a.applyConditions(db.Table(string(table)), conditions)
	return query.Where(clause.Expr{SQL: "? IN (?)", Vars: []any{clause.Column{Name: column}, subquery}}), nil
}

func (a *Adapter) FindRelated(ctx context.Context, table limen.SchemaTableName, column string, conditions []limen.Where, related limen.RelatedQuery, options *limen.QueryOptions) ([]map[string]any, error) {
	query, err := a.relatedQuery(ctx, table, column, conditions, related)
	if err != nil {
		return nil, err
	}
	if options != nil {
		if options.Limit > 0 {
			query = query.Limit(options.Limit)
		}
		if options.Offset > 0 {
			query = query.Offset(options.Offset)
		}
		for _, order := range options.OrderBy {
			query = query.Order(clause.OrderByColumn{Column: clause.Column{Name: order.Column}, Desc: order.Direction == limen.OrderByDesc})
		}
	}
	var rows []map[string]any
	err = query.Find(&rows).Error
	return rows, a.formatError(err)
}

func (a *Adapter) CountRelated(ctx context.Context, table limen.SchemaTableName, column string, conditions []limen.Where, related limen.RelatedQuery) (int64, error) {
	query, err := a.relatedQuery(ctx, table, column, conditions, related)
	if err != nil {
		return 0, err
	}
	var total int64
	err = query.Count(&total).Error
	return total, a.formatError(err)
}
