package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/thecodearcher/limen"
)

func (a *Adapter) Create(ctx context.Context, tableName limen.SchemaTableName, data map[string]any) (limen.DatabaseResult, error) {
	if len(data) == 0 {
		return limen.DatabaseResult{}, fmt.Errorf("create: no data")
	}
	payload, err := normalizeWriteMap(data)
	if err != nil {
		return limen.DatabaseResult{}, err
	}
	cols := sortedKeys(payload)
	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = a.quoteIdent(c)
	}
	// Named params: INSERT INTO t (c1, c2) VALUES (:c1, :c2)
	namedPlaceholders := make([]string, len(cols))
	for i, c := range cols {
		namedPlaceholders[i] = ":" + c
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		a.quoteIdent(string(tableName)),
		strings.Join(quotedCols, ", "),
		strings.Join(namedPlaceholders, ", "))
	result, err := a.getNamed().NamedExecContext(ctx, query, payload)
	if err != nil {
		return limen.DatabaseResult{}, err
	}
	rowsAffected, _ := result.RowsAffected()
	return limen.DatabaseResult{RowsAffected: rowsAffected}, nil
}

func (a *Adapter) FindOne(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where, orderBy []limen.OrderBy) (map[string]any, error) {
	whereSQL, args := a.buildWhere(conditions)
	query := "SELECT * FROM " + a.quoteIdent(string(tableName))
	if whereSQL != "" {
		query += " WHERE " + whereSQL
	}
	if len(orderBy) > 0 {
		parts := make([]string, len(orderBy))
		for i, o := range orderBy {
			parts[i] = a.quoteIdent(o.Column) + " " + string(o.Direction)
		}
		query += " ORDER BY " + strings.Join(parts, ", ")
	}
	query += " LIMIT 1"
	query = a.rebind(query)

	row := a.getQueryer().QueryRowxContext(ctx, query, args...)
	dest := make(map[string]any)
	err := row.MapScan(dest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, limen.ErrRecordNotFound
	}
	if err != nil {
		return nil, err
	}

	normalizeRow(dest)

	return dest, nil
}

func (a *Adapter) FindMany(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where, options *limen.QueryOptions) ([]map[string]any, error) {
	whereSQL, args := a.buildWhere(conditions)
	return a.findMany(ctx, tableName, whereSQL, args, options)
}

func (a *Adapter) findMany(ctx context.Context, tableName limen.SchemaTableName, whereSQL string, args []any, options *limen.QueryOptions) ([]map[string]any, error) {
	query := "SELECT * FROM " + a.quoteIdent(string(tableName))
	if whereSQL != "" {
		query += " WHERE " + whereSQL
	}
	if options != nil && len(options.OrderBy) > 0 {
		parts := make([]string, len(options.OrderBy))
		for i, o := range options.OrderBy {
			parts[i] = a.quoteIdent(o.Column) + " " + string(o.Direction)
		}
		query += " ORDER BY " + strings.Join(parts, ", ")
	}
	if options != nil && options.Limit > 0 {
		query += " LIMIT " + fmt.Sprintf("%d", options.Limit)
	}
	if options != nil && options.Offset > 0 {
		query += " OFFSET " + fmt.Sprintf("%d", options.Offset)
	}
	query = a.rebind(query)

	rows, err := a.getQueryer().QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		m := make(map[string]any)
		if err := rows.MapScan(m); err != nil {
			return nil, err
		}
		normalizeRow(m)
		results = append(results, m)
	}
	return results, rows.Err()
}

func (a *Adapter) Update(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where, updates map[string]any) (limen.DatabaseResult, error) {
	if len(updates) == 0 {
		return limen.DatabaseResult{}, nil
	}

	if len(conditions) == 0 {
		return limen.DatabaseResult{}, fmt.Errorf("update: conditions required to prevent accidental table-wide update")
	}
	setParts := make([]string, 0, len(updates))
	setArgs := make([]any, 0, len(updates))
	for _, k := range sortedKeys(updates) {
		part, arg, err := a.buildUpdateAssignment(k, updates[k])
		if err != nil {
			return limen.DatabaseResult{}, err
		}
		setParts = append(setParts, part)
		setArgs = append(setArgs, arg)
	}
	whereSQL, whereArgs := a.buildWhere(conditions)
	args := append(setArgs, whereArgs...)
	query := "UPDATE " + a.quoteIdent(string(tableName)) + " SET " + strings.Join(setParts, ", ") + " WHERE " + whereSQL
	query = a.rebind(query)
	result, err := a.getExt().ExecContext(ctx, query, args...)
	if err != nil {
		return limen.DatabaseResult{}, err
	}
	rowsAffected, _ := result.RowsAffected()
	return limen.DatabaseResult{RowsAffected: rowsAffected}, nil
}

func (a *Adapter) buildUpdateAssignment(column string, value any) (string, any, error) {
	quotedColumn := a.quoteIdent(column)
	update, ok := value.(limen.ArithmeticUpdate)
	if !ok {
		normalized, err := normalizeWriteValue(value)
		if err != nil {
			return "", nil, fmt.Errorf("update column %q: %w", column, err)
		}
		return quotedColumn + " = ?", normalized, nil
	}
	if err := update.Validate(); err != nil {
		return "", nil, fmt.Errorf("update column %q: %w", column, err)
	}

	delta := update.Value()
	if delta < 0 {
		return quotedColumn + " = " + quotedColumn + " - ?", -delta, nil
	}
	return quotedColumn + " = " + quotedColumn + " + ?", delta, nil
}

func (a *Adapter) Delete(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where) (limen.DatabaseResult, error) {
	if len(conditions) == 0 {
		return limen.DatabaseResult{}, fmt.Errorf("delete: conditions required to prevent accidental table-wide delete")
	}
	whereSQL, args := a.buildWhere(conditions)
	query := "DELETE FROM " + a.quoteIdent(string(tableName)) + " WHERE " + whereSQL
	query = a.rebind(query)
	result, err := a.getExt().ExecContext(ctx, query, args...)
	if err != nil {
		return limen.DatabaseResult{}, err
	}
	rowsAffected, _ := result.RowsAffected()
	return limen.DatabaseResult{RowsAffected: rowsAffected}, nil
}

func (a *Adapter) Exists(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where) (bool, error) {
	whereSQL, args := a.buildWhere(conditions)
	query := "SELECT 1 FROM " + a.quoteIdent(string(tableName))
	if whereSQL != "" {
		query += " WHERE " + whereSQL
	}
	query += " LIMIT 1"
	query = a.rebind(query)

	var dummy int
	err := a.getQueryer().QueryRowContext(ctx, query, args...).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *Adapter) Count(ctx context.Context, tableName limen.SchemaTableName, conditions []limen.Where) (int64, error) {
	whereSQL, args := a.buildWhere(conditions)
	return a.count(ctx, tableName, whereSQL, args)
}

func (a *Adapter) count(ctx context.Context, tableName limen.SchemaTableName, whereSQL string, args []any) (int64, error) {
	query := "SELECT COUNT(*) FROM " + a.quoteIdent(string(tableName))
	if whereSQL != "" {
		query += " WHERE " + whereSQL
	}
	query = a.rebind(query)

	var n int64
	err := a.getQueryer().QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

func (a *Adapter) relatedWhere(column string, conditions []limen.Where, related limen.RelatedQuery) (string, []any, error) {
	if column == "" || related.Table == "" || related.Column == "" {
		return "", nil, fmt.Errorf("related identifiers are required")
	}
	where, args := a.buildWhere(conditions)
	relation, relationArgs := a.buildWhere(related.Conditions)
	selectSQL := "SELECT " + a.quoteIdent(related.Column) + " FROM " + a.quoteIdent(string(related.Table))
	if relation != "" {
		selectSQL += " WHERE " + relation
	}
	predicate := a.quoteIdent(column) + " IN (" + selectSQL + ")"
	if where != "" {
		predicate = "(" + where + ") AND " + predicate
	}
	return predicate, append(args, relationArgs...), nil
}

func (a *Adapter) FindRelated(ctx context.Context, table limen.SchemaTableName, column string, conditions []limen.Where, related limen.RelatedQuery, options *limen.QueryOptions) ([]map[string]any, error) {
	where, args, err := a.relatedWhere(column, conditions, related)
	if err != nil {
		return nil, err
	}
	return a.findMany(ctx, table, where, args, options)
}

func (a *Adapter) CountRelated(ctx context.Context, table limen.SchemaTableName, column string, conditions []limen.Where, related limen.RelatedQuery) (int64, error) {
	where, args, err := a.relatedWhere(column, conditions, related)
	if err != nil {
		return 0, err
	}
	return a.count(ctx, table, where, args)
}
